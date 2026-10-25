package config

import (
	"slices"
	"time"

	"github.com/y3owk1n/neru/internal/domain/action"
	"github.com/y3owk1n/neru/internal/domain/motion"
)

// HeldRepeatActionName returns the name of a lone "action <name> …" binding, or "".
func HeldRepeatActionName(actions []string) string {
	if len(actions) != 1 {
		return ""
	}

	parts := SplitStepArgs(actions[0])
	if len(parts) < 2 || parts[0] != action.PrefixAction {
		return ""
	}

	return parts[1]
}

// AccelAppliesTo reports whether the named action is in the acceleration allowlist.
func (c HeldRepeatConfig) AccelAppliesTo(name string) bool {
	return c.AccelEnabled && slices.Contains(c.AccelTargets, name)
}

// HeldMotion reports whether a held binding glides, with the direction it
// points in and its step size. Only a lone move_mouse_relative with parsable,
// non-zero --dx/--dy qualifies, and only while held_repeat.enabled is set.
// The flags are read with the executor's quote-aware grammar, so a binding
// that runs also glides.
func (c HeldRepeatConfig) HeldMotion(actions []string) (motion.Direction, int, bool) {
	if !c.Enabled || action.Name(HeldRepeatActionName(actions)) != action.NameMoveMouseRelative {
		return motion.Direction{}, 0, false
	}

	deltaX, deltaY, ok := action.ParseDeltaFlags(SplitStepArgs(actions[0]))
	if !ok {
		return motion.Direction{}, 0, false
	}

	dir := motion.FromDelta(deltaX, deltaY)
	if dir.IsZero() {
		return motion.Direction{}, 0, false
	}

	return dir, max(absInt(deltaX), absInt(deltaY)), true
}

// RampFor is how fast a held binding running the named action goes, in the
// units the motion package takes: one step is worth one interval, and the
// accel options say whether and how far that climbs. The glide reads it for
// move_mouse_relative and the repeat loop for everything else, so the two
// accelerate on one ramp. An action accel_targets does not name gets a
// multiplier of 1: a constant-speed glide, or the plain interval_ms repeat.
func (c HeldRepeatConfig) RampFor(name action.Name) motion.Ramp {
	ramp := motion.Ramp{
		Interval:   time.Duration(c.Interval) * time.Millisecond,
		Multiplier: 1,
	}

	if c.AccelAppliesTo(string(name)) {
		ramp.Multiplier = c.AccelMaxMultiplier
		ramp.Duration = time.Duration(c.AccelRampMs) * time.Millisecond
	}

	return ramp
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}

	return v
}
