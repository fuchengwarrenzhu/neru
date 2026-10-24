// Package heldrepeat drives the repeat-while-held loop shared by both hotkey paths.
package heldrepeat

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/y3owk1n/neru/internal/config"
	"github.com/y3owk1n/neru/internal/domain/action"
	"github.com/y3owk1n/neru/internal/domain/motion"
)

// Stream reports whether a repeat has sped a scroll up, which is what decides
// whether letting the key go leaves smooth scroll to drop: a fast stream banks
// distance it has not posted yet, and a scroll that stops with the key has to
// throw that away. The zero value is ready; a nil *Stream reports false.
type Stream struct {
	scrolled atomic.Bool
}

// Scrolled reports whether an accelerated scroll has repeated at least once.
// A key let go before its first repeat is a tap and keeps its whole step.
func (s *Stream) Scrolled() bool {
	return s != nil && s.scrolled.Load()
}

// Run blocks, repeating actions until ctx is canceled. The dispatch on key-down
// belongs to the caller: the two call sites disagree about whether the press has
// already fired.
//
// A binding whose action held_repeat.accel_targets names repeats faster the
// longer it is held, on the ramp the cursor glide climbs (config.RampFor), so
// one set of accel options tunes both. stream, which may be nil, learns when an
// accelerated scroll repeats. The caller drops the smooth scroll in flight where
// it cancels ctx, in the same step, so that no press handled after the release
// can lose its own scroll to the drop.
func Run(
	ctx context.Context,
	cfg config.HeldRepeatConfig,
	actions []string,
	dispatch func([]string),
	stream *Stream,
) {
	pressed := time.Now()

	initialTimer := time.NewTimer(time.Duration(cfg.InitialDelay) * time.Millisecond)
	defer initialTimer.Stop()

	select {
	case <-ctx.Done():
		return
	case <-initialTimer.C:
	}

	name := action.Name(config.HeldRepeatActionName(actions))
	if cfg.AccelAppliesTo(string(name)) {
		var scrolled *atomic.Bool
		if stream != nil && action.IsScrollSubAction(string(name)) {
			scrolled = &stream.scrolled
		}

		runAccelerated(ctx, cfg.RampFor(name).ParamsFor(1), pressed, actions, dispatch, scrolled)

		return
	}

	ticker := time.NewTicker(time.Duration(cfg.Interval) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			dispatch(actions)
		}
	}
}

// runAccelerated repeats at the rate params allow for how long the key has
// been down — params are in repeats per second, one repeat being the step a
// ramp is resolved for — and sets repeated, when it is not nil, once it has
// dispatched.
//
// It waits out each gap after the previous dispatch returns rather than on a
// ticker, so a dispatch slower than the gap holds the next one back instead
// of queueing it: the rate yields to the cost of a repeat and never runs
// ahead of it.
func runAccelerated(
	ctx context.Context,
	params motion.Params,
	pressed time.Time,
	actions []string,
	dispatch func([]string),
	repeated *atomic.Bool,
) {
	gap := func() time.Duration {
		return time.Duration(float64(time.Second) / params.SpeedAfter(time.Since(pressed)))
	}

	timer := time.NewTimer(gap())
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		// A gap of a few milliseconds makes a release racing the timer likely;
		// the key that let go is never owed one more repeat.
		if ctx.Err() != nil {
			return
		}

		dispatch(actions)

		if repeated != nil {
			repeated.Store(true)
		}

		timer.Reset(gap())
	}
}
