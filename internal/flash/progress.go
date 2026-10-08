package flash

import (
	"sync"
	"time"

	"flasher/internal/plan"
)

type sample struct {
	at    time.Time
	bytes int64
}

// reporter turns byte counts into periodic Progress callbacks with a speed
// averaged over the last few seconds.
type reporter struct {
	cb   func(Progress)
	plan *plan.Plan

	mu      sync.Mutex
	stage   Stage
	bytes   int64
	total   int64 // for the current stage; -1 when unknown
	samples []sample

	done chan struct{}
	wg   sync.WaitGroup
}

const speedWindow = 3 * time.Second

func newReporter(cb func(Progress), p *plan.Plan) *reporter {
	r := &reporter{cb: cb, plan: p, total: p.Total, done: make(chan struct{})}
	if cb == nil {
		return r
	}
	r.wg.Go(func() {
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				r.cb(r.snapshot())
			case <-r.done:
				return
			}
		}
	})
	return r
}

func (r *reporter) add(n int64) {
	r.mu.Lock()
	r.bytes += n
	r.mu.Unlock()
}

func (r *reporter) setStage(s Stage, total int64) {
	r.mu.Lock()
	r.stage, r.bytes, r.total, r.samples = s, 0, total, nil
	r.mu.Unlock()
	if r.cb != nil {
		r.cb(r.snapshot())
	}
}

func (r *reporter) stop() {
	close(r.done)
	r.wg.Wait()
}

func (r *reporter) snapshot() Progress {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	r.samples = append(r.samples, sample{now, r.bytes})
	for len(r.samples) > 2 && now.Sub(r.samples[0].at) > speedWindow {
		r.samples = r.samples[1:]
	}

	p := Progress{Stage: r.stage, Bytes: r.bytes}
	switch {
	case r.stage == Done:
		p.Fraction = 1
	case r.total > 0:
		p.Fraction = float64(r.bytes) / float64(r.total)
	case r.stage == Writing && r.plan.Fraction != nil:
		p.Fraction = r.plan.Fraction()
	}
	p.Fraction = min(max(p.Fraction, 0), 1)

	if first := r.samples[0]; len(r.samples) > 1 {
		if dt := now.Sub(first.at).Seconds(); dt > 0 {
			p.BytesPerSec = float64(r.bytes-first.bytes) / dt
		}
	}
	if p.BytesPerSec > 0 && p.Fraction > 0.01 && p.Fraction < 1 {
		// Estimate the total from the fraction, which also covers
		// compressed images whose size is unknown.
		total := float64(r.bytes) / p.Fraction
		p.ETA = time.Duration((total - float64(r.bytes)) / p.BytesPerSec * float64(time.Second))
	}
	return p
}
