package output

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Steps shows a change as it happens, one line per stage:
//
//	✓ Ordered web-2                 NGN 15,040.00
//	✓ Payment taken                 2s
//	⠼ Provisioning                  0:41
//
// In a terminal the current stage spins with its elapsed time; piped or in CI
// each stage is a plain line when it starts and when it ends.
type Steps struct {
	p       *Printer
	mu      sync.Mutex
	label   string
	started time.Time
	stop    chan struct{}
	stopped chan struct{}
}

const labelWidth = 30

var frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Steps starts a run of stages. Nothing is shown with --json or --quiet.
func (p *Printer) Steps() *Steps { return &Steps{p: p} }

func (s *Steps) silent() bool { return s.p.JSON || s.p.Quiet }

// Start begins a stage, ending any stage still running without a mark.
func (s *Steps) Start(label string) {
	s.halt()
	s.mu.Lock()
	s.label, s.started = label, time.Now()
	s.mu.Unlock()
	if s.silent() {
		return
	}
	if !s.p.live {
		fmt.Fprintf(s.p.Err, "  … %s\n", label)
		return
	}
	s.stop, s.stopped = make(chan struct{}), make(chan struct{})
	go s.spin(s.stop, s.stopped)
}

// Done ends the current stage with ✓. An empty label keeps the stage's own;
// an empty detail shows how long it took.
func (s *Steps) Done(label, detail string) { s.end(green, "✓", label, detail) }

func (s *Steps) Fail(label, detail string) { s.end(red, "✗", label, detail) }

// Mark records a stage that happened at once, with ✓ and a detail.
func (s *Steps) Mark(label, detail string) {
	s.halt()
	s.mu.Lock()
	s.label, s.started = label, time.Now()
	s.mu.Unlock()
	s.end(green, "✓", label, detail)
}

func (s *Steps) end(style, glyph, label, detail string) {
	s.halt()
	s.mu.Lock()
	if label == "" {
		label = s.label
	}
	if detail == "" && !s.started.IsZero() {
		detail = Elapsed(time.Since(s.started))
	}
	s.label, s.started = "", time.Time{}
	s.mu.Unlock()
	if s.silent() {
		return
	}
	fmt.Fprintf(s.p.Err, "  %s %s%s\n", paint(s.p.errColor, style, glyph), pad(label), paint(s.p.errColor, dim, detail))
}

func (s *Steps) halt() {
	if s.stop == nil {
		return
	}
	close(s.stop)
	<-s.stopped
	s.stop = nil
	fmt.Fprint(s.p.Err, "\r\x1b[K")
}

func (s *Steps) spin(stop, stopped chan struct{}) {
	defer close(stopped)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for i := 0; ; i++ {
		s.mu.Lock()
		label, started := s.label, s.started
		s.mu.Unlock()
		fmt.Fprintf(s.p.Err, "\r\x1b[K  %s %s%s", paint(s.p.errColor, yellow, frames[i%len(frames)]), pad(label), paint(s.p.errColor, dim, Elapsed(time.Since(started))))
		select {
		case <-stop:
			return
		case <-tick.C:
		}
	}
}

func pad(label string) string {
	if n := labelWidth - visibleWidth(label); n > 0 {
		return label + strings.Repeat(" ", n)
	}
	return label + "  "
}

// Elapsed is a short duration: 4s, then 1:12.
func Elapsed(d time.Duration) string {
	sec := int(d.Round(time.Second).Seconds())
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}
