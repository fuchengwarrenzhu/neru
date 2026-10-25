package heldrepeat_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/y3owk1n/neru/internal/app/heldrepeat"
	"github.com/y3owk1n/neru/internal/config"
)

const (
	testBinding       = "action move_mouse_relative --dx=10 --dy=0"
	testScrollBinding = "action scroll_down"
	testCellBinding   = "action move_cell --dx=1"
)

type collector struct {
	mu    sync.Mutex
	ticks [][]string
}

func (c *collector) dispatch(actions []string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.ticks = append(c.ticks, append([]string(nil), actions...))
}

func (c *collector) snapshot() [][]string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([][]string(nil), c.ticks...)
}

func runUntilIdle(t *testing.T, cfg config.HeldRepeatConfig, hold time.Duration) [][]string {
	t.Helper()

	ticks, _ := holdBinding(t, cfg, testBinding, hold)

	return ticks
}

// holdBinding holds binding for hold, lets go, and returns everything Run
// dispatched plus whether its stream reports a sped-up scroll.
func holdBinding(
	t *testing.T,
	cfg config.HeldRepeatConfig,
	binding string,
	hold time.Duration,
) ([][]string, bool) {
	t.Helper()

	var stream heldrepeat.Stream

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var col collector

	done := make(chan struct{})

	go func() {
		defer close(done)

		heldrepeat.Run(ctx, cfg, []string{binding}, col.dispatch, &stream)
	}()

	time.Sleep(hold)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancellation")
	}

	return col.snapshot(), stream.Scrolled()
}

func TestRunDispatchesNothingBeforeInitialDelay(t *testing.T) {
	cfg := config.HeldRepeatConfig{
		Enabled:      true,
		InitialDelay: 500,
		Interval:     5,
	}

	if ticks := runUntilIdle(t, cfg, 20*time.Millisecond); len(ticks) != 0 {
		t.Errorf("expected no repeats during initial_delay, got %d", len(ticks))
	}
}

func TestRunRepeatsTheBindingUnchanged(t *testing.T) {
	cfg := config.HeldRepeatConfig{
		Enabled:      true,
		InitialDelay: 1,
		Interval:     5,
	}

	ticks := runUntilIdle(t, cfg, 60*time.Millisecond)
	if len(ticks) < 3 {
		t.Fatalf("expected several ticks, got %d", len(ticks))
	}

	for idx, tick := range ticks {
		if tick[0] != testBinding {
			t.Fatalf("tick %d was rewritten to %q, want the binding untouched", idx, tick[0])
		}
	}
}

// TestRun_AcceleratedBindingRepeatsFaster pins that accel_targets reaches the
// repeat loop: the same hold repeats a targeted binding several times as often
// as one the list leaves out. At 20 ms, a 300 ms hold is about 15 repeats
// unaccelerated; 8x over 50 ms brings the gap down to 2.5 ms almost at once.
func TestRun_AcceleratedBindingRepeatsFaster(t *testing.T) {
	cfg := config.HeldRepeatConfig{
		Enabled:            true,
		InitialDelay:       1,
		Interval:           20,
		AccelEnabled:       true,
		AccelRampMs:        50,
		AccelMaxMultiplier: 8,
		AccelTargets:       []string{"scroll_down"},
	}

	const hold = 300 * time.Millisecond

	plainTicks, _ := holdBinding(t, cfg, "action scroll_up", hold)
	acceleratedTicks, _ := holdBinding(t, cfg, testScrollBinding, hold)
	plain, accelerated := len(plainTicks), len(acceleratedTicks)

	if plain == 0 || accelerated < 3*plain {
		t.Errorf(
			"accelerated repeats = %d, plain = %d; want the targeted binding at least 3x as often",
			accelerated,
			plain,
		)
	}
}

// TestStream_ScrolledOnlyForARepeatedAcceleratedScroll pins when a release has
// smooth scroll to drop: only for a scroll the accel options sped up, and only
// once it has actually repeated — so a tap keeps its whole step, an
// unaccelerated hold keeps upstream behavior, and an accelerated non-scroll
// binding never touches scrolling.
func TestStream_ScrolledOnlyForARepeatedAcceleratedScroll(t *testing.T) {
	accel := config.HeldRepeatConfig{
		Enabled:            true,
		InitialDelay:       1,
		Interval:           10,
		AccelEnabled:       true,
		AccelRampMs:        50,
		AccelMaxMultiplier: 4,
		AccelTargets:       []string{"scroll_down", "move_cell"},
	}

	tap := accel
	tap.InitialDelay = 500

	tests := []struct {
		name         string
		cfg          config.HeldRepeatConfig
		binding      string
		wantScrolled bool
	}{
		{"held accelerated scroll", accel, testScrollBinding, true},
		{"tap released before the first repeat keeps its step", tap, testScrollBinding, false},
		{
			"scroll left out of accel_targets keeps upstream behavior",
			accel,
			"action scroll_up",
			false,
		},
		{"accelerated non-scroll binding leaves scrolling alone", accel, testCellBinding, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, scrolled := holdBinding(t, tt.cfg, tt.binding, 80*time.Millisecond)
			if scrolled != tt.wantScrolled {
				t.Errorf("Stream.Scrolled() = %v, want %v", scrolled, tt.wantScrolled)
			}
		})
	}
}
