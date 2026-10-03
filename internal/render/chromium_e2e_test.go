package render

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ledongthuc/pdf"
)

// These tests drive a real Chromium. They run only when PRESSROOM_E2E=1 so a
// plain `go test ./...` stays fast and needs no browser. CHROME_PATH selects the
// binary and CHROMIUM_NO_SANDBOX=true disables the sandbox where the host
// cannot provide one (containers, most CI runners).

func e2eConfig(t *testing.T) ChromiumConfig {
	t.Helper()
	if os.Getenv("PRESSROOM_E2E") != "1" {
		t.Skip("set PRESSROOM_E2E=1 to run the browser tests")
	}
	return ChromiumConfig{
		ExecPath:    os.Getenv("CHROME_PATH"),
		NoSandbox:   os.Getenv("CHROMIUM_NO_SANDBOX") == "true",
		Concurrency: 2,
		Queue:       2,
	}
}

func newE2E(t *testing.T, mutate func(*ChromiumConfig)) *Chromium {
	t.Helper()
	cfg := e2eConfig(t)
	if mutate != nil {
		mutate(&cfg)
	}
	c := NewChromium(cfg)
	t.Cleanup(c.Close)
	return c
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func e2eOptions() Options {
	o := defaultOptions()
	return o
}

func render(t *testing.T, c *Chromium, html string, o Options) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b, err := c.Render(ctx, Request{HTML: html, Options: o})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return b
}

// pdfPages returns the text of every page.
func pdfPages(t *testing.T, b []byte) []string {
	t.Helper()
	if !bytes.HasPrefix(b, []byte("%PDF-")) {
		t.Fatalf("output is not a PDF: %q", b[:min(len(b), 16)])
	}
	r, err := pdf.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("read pdf: %v", err)
	}
	var pages []string
	for i := 1; i <= r.NumPage(); i++ {
		txt, err := r.Page(i).GetPlainText(nil)
		if err != nil {
			t.Fatalf("page %d text: %v", i, err)
		}
		pages = append(pages, txt)
	}
	return pages
}

func TestE2ESampleRendersThreePages(t *testing.T) {
	c := newE2E(t, nil)
	html := fixture(t, "sample.html")

	cold := time.Now()
	first := render(t, c, html, e2eOptions())
	t.Logf("cold render (browser start included): %s", time.Since(cold).Round(time.Millisecond))

	warm := time.Now()
	second := render(t, c, html, e2eOptions())
	took := time.Since(warm)
	t.Logf("warm render: %s, %d bytes", took.Round(time.Millisecond), len(second))
	if took > 3*time.Second {
		t.Errorf("warm render took %s, want under 3s", took)
	}

	for name, b := range map[string][]byte{"first": first, "second": second} {
		pages := pdfPages(t, b)
		if len(pages) != 3 {
			t.Errorf("%s: %d pages, want 3", name, len(pages))
			continue
		}
		if !hasTwoGlyphsAfter(pages[1], "3–12") {
			t.Errorf("%s: page 2 lost the Han characters of the first table row: %q", name, pages[1])
		}
		if !strings.Contains(pages[0], "mẫu") {
			t.Errorf("%s: page 1 lost its Vietnamese text: %q", name, pages[0])
		}
	}
}

// hasTwoGlyphsAfter reports whether the line after the one holding marker has
// two characters, the pair 壬午 in the sample. The PDF reader maps Han glyphs
// back to Unicode on some systems and returns raw glyph codes on others (CID
// fonts), so the exact characters cannot be compared everywhere; a dropped
// pair would leave the line empty. That the characters are real glyphs from the
// CJK font is checked on the built image with a different extractor.
func hasTwoGlyphsAfter(text, marker string) bool {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == marker && i+1 < len(lines) {
			next := lines[i+1]
			if next == "壬午" {
				return true
			}
			return len([]rune(next)) == 2
		}
	}
	return false
}

func TestE2EFooterShowsPageNumbers(t *testing.T) {
	c := newE2E(t, nil)
	o := e2eOptions()
	o.FooterHTML = `<div style="font-size:9px;width:100%;text-align:center"><span class="pageNumber"></span> of <span class="totalPages"></span></div>`
	pages := pdfPages(t, render(t, c, fixture(t, "sample.html"), o))
	if len(pages) != 3 {
		t.Fatalf("%d pages, want 3", len(pages))
	}
	// The extractor puts each span on its own line, so compare without spaces.
	for i, p := range pages {
		want := fmt.Sprintf("%dof3", i+1)
		if got := strings.Join(strings.Fields(p), ""); !strings.Contains(got, want) {
			t.Errorf("page %d footer: text %q does not contain %q", i+1, got, want)
		}
	}
}

func TestE2EHostilePageIsContained(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	c := newE2E(t, func(cfg *ChromiumConfig) {
		cfg.OnBlocked = func(u string) {
			mu.Lock()
			seen[u] = true
			mu.Unlock()
		}
	})
	b := render(t, c, fixture(t, "hostile.html"), e2eOptions())
	pages := pdfPages(t, b)
	if len(pages) == 0 {
		t.Fatal("the hostile page produced no pages")
	}
	// /etc/hosts always names localhost; none of it may reach the PDF.
	if strings.Contains(strings.Join(pages, " "), "localhost") {
		t.Error("the content of a local file reached the PDF")
	}

	mu.Lock()
	defer mu.Unlock()
	var urls []string
	for u := range seen {
		urls = append(urls, u)
	}
	sort.Strings(urls)
	t.Logf("refused requests:\n  %s", strings.Join(urls, "\n  "))

	for _, want := range []string{
		"http://css.pressroom-test.invalid/style.css",
		"http://bg.pressroom-test.invalid/bg.png",
		"http://pixel.pressroom-test.invalid/pixel.png",
		"http://frame.pressroom-test.invalid/frame.html",
	} {
		if !seen[want] {
			t.Errorf("request to %s was not intercepted", want)
		}
	}
	for u := range seen {
		if strings.Contains(u, "fetch.") || strings.Contains(u, "onload.") {
			t.Errorf("a script ran: %s was requested", u)
		}
		if strings.HasPrefix(u, "data:") {
			t.Errorf("an inline data: image was refused: %.40s", u)
		}
	}
}

func TestE2ERequestTimeoutReturnsDeadlineExceeded(t *testing.T) {
	c := newE2E(t, nil)
	html := fixture(t, "sample.html")
	render(t, c, html, e2eOptions()) // start the browser first

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	_, err := c.Render(ctx, Request{HTML: html, Options: e2eOptions()})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want DeadlineExceeded", err)
	}

	// The browser must be fine after an abandoned render.
	if got := len(pdfPages(t, render(t, c, html, e2eOptions()))); got != 3 {
		t.Errorf("render after a timeout produced %d pages, want 3", got)
	}
}

func TestE2EFullQueueReturnsBusy(t *testing.T) {
	c := newE2E(t, func(cfg *ChromiumConfig) { cfg.Concurrency, cfg.Queue = 1, 0 })
	hold, err := c.limiter.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Render(context.Background(), Request{HTML: "<p>x</p>", Options: e2eOptions()})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("error = %v, want ErrBusy", err)
	}
	hold()
	render(t, c, "<p>x</p>", e2eOptions())
}

func TestE2EConcurrentRendersStayApart(t *testing.T) {
	c := newE2E(t, nil)
	const n = 4
	results := make([][]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			html := fmt.Sprintf("<!doctype html><meta charset=utf-8><p>document-%d-marker</p>", i)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			b, err := c.Render(ctx, Request{HTML: html, Options: e2eOptions()})
			if err != nil {
				t.Errorf("render %d: %v", i, err)
				return
			}
			results[i] = pdfPages(t, b)
		}(i)
	}
	wg.Wait()
	for i, pages := range results {
		if len(pages) != 1 || !strings.Contains(pages[0], fmt.Sprintf("document-%d-marker", i)) {
			t.Errorf("document %d came back as %q", i, pages)
		}
		for j := 0; j < n && len(pages) == 1; j++ {
			if j != i && strings.Contains(pages[0], fmt.Sprintf("document-%d-marker", j)) {
				t.Errorf("document %d contains the text of document %d", i, j)
			}
		}
	}
}

func TestE2ERecoversWhenTheBrowserDies(t *testing.T) {
	c := newE2E(t, nil)
	html := fixture(t, "sample.html")
	render(t, c, html, e2eOptions())

	sess, err := c.session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	proc := browserProcess(sess)
	if proc == nil {
		t.Fatal("no browser process to kill")
	}
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = proc.Wait()

	// The first call after the crash may report it; the service must then
	// heal without a restart of the process.
	var failures []error
	for attempt := 0; attempt < 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		b, err := c.Render(ctx, Request{HTML: html, Options: e2eOptions()})
		cancel()
		if err == nil {
			if len(pdfPages(t, b)) != 3 {
				t.Fatal("recovered render has the wrong page count")
			}
			for _, f := range failures {
				if !errors.Is(f, ErrUnavailable) {
					t.Errorf("a failure during recovery was %v, want ErrUnavailable", f)
				}
			}
			t.Logf("recovered after %d failed attempt(s)", len(failures))
			return
		}
		failures = append(failures, err)
	}
	t.Fatalf("no render succeeded after the browser was killed: %v", failures)
}

func TestE2ECloseStopsTheBrowser(t *testing.T) {
	c := newE2E(t, nil)
	render(t, c, "<p>x</p>", e2eOptions())
	sess, err := c.session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	proc := browserProcess(sess)

	c.Close()
	if _, err := c.Render(context.Background(), Request{HTML: "<p>x</p>", Options: e2eOptions()}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("render after Close: error = %v, want ErrUnavailable", err)
	}
	if err := c.Ready(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Ready after Close: error = %v, want ErrUnavailable", err)
	}
	// Signal 0 only probes: it fails once the process is gone.
	if proc != nil && proc.Signal(syscallZero()) == nil {
		t.Error("the browser process is still running after Close")
	}
}

func TestE2EMarginsAreApplied(t *testing.T) {
	c := newE2E(t, nil)
	const html = "<!doctype html><meta charset=utf-8><body style='margin:0'><p>margin probe</p>"
	for _, leftMM := range []float64{12, 50} {
		o := e2eOptions()
		o.PreferCSSPageSize = false
		o.Margins.Left = leftMM
		b := render(t, c, html, o)

		r, err := pdf.NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatal(err)
		}
		minX := 1e9
		for _, txt := range r.Page(1).Content().Text {
			if txt.X < minX {
				minX = txt.X
			}
		}
		wantPt := leftMM / 25.4 * 72
		if minX < wantPt-1 || minX > wantPt+6 {
			t.Errorf("left margin %.0f mm: text starts at %.1f pt, want about %.1f pt", leftMM, minX, wantPt)
		}
	}
}
