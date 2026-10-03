package render

import (
	"os"
	"syscall"

	"github.com/chromedp/chromedp"
)

// browserProcess returns the operating-system process behind sess.
func browserProcess(s *session) *os.Process {
	c := chromedp.FromContext(s.ctx)
	if c == nil || c.Browser == nil {
		return nil
	}
	return c.Browser.Process()
}

func syscallZero() os.Signal { return syscall.Signal(0) }
