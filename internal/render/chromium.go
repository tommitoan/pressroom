package render

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"

	"github.com/tommitoan/pressroom/internal/limits"
)

// ChromiumConfig configures the headless Chromium renderer.
type ChromiumConfig struct {
	// ExecPath is the browser binary; empty lets chromedp look one up.
	ExecPath string
	// NoSandbox disables the Chromium sandbox. Only for containers that cannot
	// grant the sandbox its privileges; the default keeps it on.
	NoSandbox bool
	// Concurrency is the number of renders that run at once (default 2).
	Concurrency int
	// Queue is how many more renders may wait for a free slot (default 0).
	Queue int
	// StartTimeout bounds launching the browser (default 15s).
	StartTimeout time.Duration
	Logger       *slog.Logger
	// OnBlocked is called with the URL of every request the page tried to make.
	// It runs on the browser event goroutine and must be safe for concurrent use.
	OnBlocked func(url string)
}

// Chromium prints documents with one shared headless Chromium process. The
// process starts on first use and is restarted when it dies. Every render gets
// its own tab, closed afterwards, and runs with scripts disabled and every
// network request refused, so a page has nothing to store or fetch. Separate
// browser contexts per render are not used: Chrome's new headless mode refuses
// to open a tab in one.
type Chromium struct {
	cfg     ChromiumConfig
	limiter *limits.Limiter
	log     *slog.Logger

	lock    chan struct{} // guards sess; a channel so waiters can honour ctx
	sess    *session
	stopped bool
}

type session struct {
	ctx         context.Context
	cancel      context.CancelFunc
	cancelAlloc context.CancelFunc
}

// NewChromium returns a renderer. It does not start the browser.
func NewChromium(cfg ChromiumConfig) *Chromium {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 2
	}
	if cfg.Queue < 0 {
		cfg.Queue = 0
	}
	if cfg.StartTimeout <= 0 {
		cfg.StartTimeout = 15 * time.Second
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Chromium{
		cfg:     cfg,
		limiter: limits.New(cfg.Concurrency, cfg.Queue),
		log:     log,
		lock:    make(chan struct{}, 1),
	}
}

// Render prints req.HTML to a PDF.
func (c *Chromium) Render(ctx context.Context, req Request) ([]byte, error) {
	params, err := printParams(req.Options)
	if err != nil {
		return nil, err
	}

	release, err := c.limiter.Acquire(ctx)
	if err != nil {
		if errors.Is(err, limits.ErrFull) {
			return nil, ErrBusy
		}
		return nil, err
	}
	defer release()

	sess, err := c.session(ctx)
	if err != nil {
		return nil, err
	}

	pdf, blocked, err := c.print(ctx, sess, req.HTML, params)
	if blocked > 0 {
		c.log.Debug("blocked page requests", "count", blocked)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !alive(sess) {
			c.discard(sess)
			return nil, fmt.Errorf("%w: browser exited during render", ErrUnavailable)
		}
		return nil, fmt.Errorf("print: %w", err)
	}
	return pdf, nil
}

// Ready starts the browser if needed and reports whether it answers.
func (c *Chromium) Ready(ctx context.Context) error {
	sess, err := c.session(ctx)
	if err != nil {
		return err
	}
	if !alive(sess) {
		c.discard(sess)
		return fmt.Errorf("%w: browser not responding", ErrUnavailable)
	}
	return nil
}

// Close stops the browser. Renders in flight fail.
func (c *Chromium) Close() {
	c.lock <- struct{}{}
	defer func() { <-c.lock }()
	c.stopped = true
	c.stopLocked()
}

// session returns the running browser, launching it when there is none or the
// previous one has died.
func (c *Chromium) session(ctx context.Context) (*session, error) {
	select {
	case c.lock <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.lock }()

	if c.stopped {
		return nil, fmt.Errorf("%w: shutting down", ErrUnavailable)
	}
	if c.sess != nil && c.sess.ctx.Err() == nil {
		return c.sess, nil
	}
	c.stopLocked()

	sess, err := c.launch(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		c.log.Error("browser start failed", "err", err.Error())
		return nil, fmt.Errorf("%w: browser did not start", ErrUnavailable)
	}
	c.sess = sess
	c.log.Info("browser started")
	return sess, nil
}

func (c *Chromium) launch(ctx context.Context) (*session, error) {
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts,
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-dev-shm-usage", true),
	)
	if c.cfg.NoSandbox {
		opts = append(opts, chromedp.NoSandbox)
	}
	if c.cfg.ExecPath != "" {
		opts = append(opts, chromedp.ExecPath(c.cfg.ExecPath))
	}

	// The browser outlives the request that happened to start it, so it must
	// not hang off ctx.
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	sess := &session{ctx: browserCtx, cancel: cancelBrowser, cancelAlloc: cancelAlloc}

	started := make(chan error, 1)
	go func() { started <- chromedp.Run(browserCtx) }()

	timer := time.NewTimer(c.cfg.StartTimeout)
	defer timer.Stop()
	select {
	case err := <-started:
		if err != nil {
			stop(sess)
			return nil, err
		}
		return sess, nil
	case <-timer.C:
		stop(sess)
		return nil, fmt.Errorf("no answer within %s", c.cfg.StartTimeout)
	case <-ctx.Done():
		stop(sess)
		return nil, ctx.Err()
	}
}

// discard stops sess if it is still the current browser, so a render that
// found it dead does not tear down a replacement another render started.
func (c *Chromium) discard(sess *session) {
	c.lock <- struct{}{}
	defer func() { <-c.lock }()
	if c.sess == sess {
		c.stopLocked()
	} else {
		stop(sess)
	}
}

func (c *Chromium) stopLocked() {
	if c.sess != nil {
		stop(c.sess)
		c.sess = nil
	}
}

func stop(s *session) {
	// Cancel waits for the browser process to exit.
	_ = chromedp.Cancel(s.ctx)
	s.cancel()
	s.cancelAlloc()
}

// alive asks the browser for its version; a dead process cannot answer.
func alive(s *session) bool {
	if s.ctx.Err() != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
	defer cancel()
	c := chromedp.FromContext(s.ctx)
	if c == nil || c.Browser == nil {
		return false
	}
	_, _, _, _, _, err := browser.GetVersion().Do(cdp.WithExecutor(ctx, c.Browser))
	return err == nil
}

// print renders html in a fresh browser context and returns the PDF and the
// number of page requests that were refused.
func (c *Chromium) print(ctx context.Context, sess *session, html string, params *page.PrintToPDFParams) ([]byte, int, error) {
	tabCtx, cancelTab := chromedp.NewContext(sess.ctx)
	var tabID target.ID
	defer func() {
		cancelTab()
		closeTab(sess, tabID)
	}()

	// Create the tab before the request's cancellation is attached. If the
	// create call is cancelled in flight, the browser still opens the tab but
	// chromedp never learns its id and so never closes it; every cancelled or
	// timed-out request would then leak a tab. Creation is local and fast, so
	// it only needs a bound of its own.
	creating := time.AfterFunc(tabCreateTimeout, cancelTab)
	err := chromedp.Run(tabCtx)
	creating.Stop()
	if err != nil {
		return nil, 0, err
	}
	tabID = chromedp.FromContext(tabCtx).Target.TargetID
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	// From here, ending the request closes the tab, which is what stops
	// Chromium working on a request that timed out.
	stopWatch := context.AfterFunc(ctx, cancelTab)
	defer stopWatch()

	var (
		mu      sync.Mutex
		blocked int
	)
	// loaded is signalled when the page reports its load event for the content
	// set below; earlier events belong to the blank page and are ignored.
	var awaitingLoad atomic.Bool
	loaded := make(chan struct{}, 1)
	chromedp.ListenTarget(tabCtx, func(ev interface{}) {
		if e, ok := ev.(*page.EventLifecycleEvent); ok {
			if e.Name == "load" && awaitingLoad.Load() {
				select {
				case loaded <- struct{}{}:
				default:
				}
			}
			return
		}
		e, ok := ev.(*fetch.EventRequestPaused)
		if !ok {
			return
		}
		mu.Lock()
		blocked++
		mu.Unlock()
		if c.cfg.OnBlocked != nil {
			c.cfg.OnBlocked(e.Request.URL)
		}
		go func() {
			target := chromedp.FromContext(tabCtx)
			if target == nil || target.Target == nil {
				return
			}
			// Failing the request is best effort: if the tab is already gone
			// there is nothing left to fail.
			_ = fetch.FailRequest(e.RequestID, network.ErrorReasonBlockedByClient).
				Do(cdp.WithExecutor(tabCtx, target.Target))
		}()
	})

	var pdf []byte
	err = chromedp.Run(tabCtx,
		emulation.SetScriptExecutionDisabled(true),
		fetch.Enable().WithPatterns([]*fetch.RequestPattern{{URLPattern: "*"}}),
		chromedp.Navigate("about:blank"),
		chromedp.ActionFunc(func(ctx context.Context) error {
			tree, err := page.GetFrameTree().Do(ctx)
			if err != nil {
				return err
			}
			awaitingLoad.Store(true)
			if err := page.SetDocumentContent(tree.Frame.ID, html).Do(ctx); err != nil {
				return err
			}
			// Print only once the page has finished loading what it asked for.
			// Every request is refused at once, so this is quick; without it a
			// fast machine can print before the parser has issued the requests.
			select {
			case <-loaded:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			pdf, _, err = params.Do(ctx)
			return err
		}),
	)

	mu.Lock()
	defer mu.Unlock()
	return pdf, blocked, err
}

// closeTab makes sure the tab is gone. Cancelling the tab context already
// asks the browser to close it, but a tab closed right after it was created
// can be left open without any error, so the result is checked and the close
// repeated. Each render pays one cheap call; a tab that is already closed
// answers with an error, which ends the loop.
func closeTab(sess *session, id target.ID) {
	if id == "" {
		return
	}
	c := chromedp.FromContext(sess.ctx)
	if c == nil || c.Browser == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	exec := cdp.WithExecutor(ctx, c.Browser)
	for attempt := 0; attempt < 5; attempt++ {
		if _, err := target.GetTargetInfo().WithTargetID(id).Do(exec); err != nil {
			return // the tab, or the whole browser, is gone
		}
		_ = target.CloseTarget(id).Do(exec)
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// paperInches maps a paper name to its size in inches (width, height).
var paperInches = map[string][2]float64{
	"A4":     {8.27, 11.69},
	"A3":     {11.69, 16.54},
	"Letter": {8.5, 11},
	"Legal":  {8.5, 14},
}

const emptyTemplate = "<span></span>"

// tabCreateTimeout bounds opening a tab in a browser that is not answering.
const tabCreateTimeout = 10 * time.Second

// printParams maps validated options onto Chromium's print parameters.
func printParams(o Options) (*page.PrintToPDFParams, error) {
	size, ok := paperInches[o.Paper]
	if !ok {
		return nil, fmt.Errorf("unsupported paper %q", o.Paper)
	}
	scale := o.Scale
	if scale == 0 {
		scale = 1
	}
	const mmPerInch = 25.4

	p := page.PrintToPDF().
		WithPaperWidth(size[0]).
		WithPaperHeight(size[1]).
		WithLandscape(o.Landscape).
		WithMarginTop(o.Margins.Top / mmPerInch).
		WithMarginRight(o.Margins.Right / mmPerInch).
		WithMarginBottom(o.Margins.Bottom / mmPerInch).
		WithMarginLeft(o.Margins.Left / mmPerInch).
		WithScale(scale).
		WithPrintBackground(o.PrintBackground).
		WithPreferCSSPageSize(o.PreferCSSPageSize)

	// Chromium prints a default date and title banner when either template is
	// missing, so an unused one is replaced by an empty element.
	if o.HeaderHTML != "" || o.FooterHTML != "" {
		header, footer := o.HeaderHTML, o.FooterHTML
		if header == "" {
			header = emptyTemplate
		}
		if footer == "" {
			footer = emptyTemplate
		}
		p = p.WithDisplayHeaderFooter(true).WithHeaderTemplate(header).WithFooterTemplate(footer)
	}
	return p, nil
}
