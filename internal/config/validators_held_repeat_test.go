package config_test

import (
	"math"
	"slices"
	"testing"

	"github.com/y3owk1n/neru/internal/config"
	"github.com/y3owk1n/neru/internal/domain/action"
)

func TestConfigValidateHeldRepeat_AccelDefaults(t *testing.T) {
	cfg := config.DefaultConfig()

	err := cfg.ValidateHeldRepeat(nil)
	if err != nil {
		t.Fatalf("ValidateHeldRepeat() unexpected error: %v", err)
	}
}

// TestConfigValidateHeldRepeat_AccelAcceptsEveryHeldRepeatAction pins the
// rule accel_targets is checked against: acceleration scales a held repeat, so
// any action that repeats while held is a target, scrolling included.
func TestConfigValidateHeldRepeat_AccelAcceptsEveryHeldRepeatAction(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.HeldRepeat.AccelEnabled = true
	cfg.HeldRepeat.AccelTargets = nil

	for _, name := range []action.Name{
		action.NameMoveMouseRelative, action.NameScrollUp, action.NameScrollDown,
		action.NameScrollLeft, action.NameScrollRight, action.NamePageUp,
		action.NamePageDown, action.NameMoveCell,
	} {
		cfg.HeldRepeat.AccelTargets = append(cfg.HeldRepeat.AccelTargets, string(name))
	}

	err := cfg.ValidateHeldRepeat(nil)
	if err != nil {
		t.Fatalf("ValidateHeldRepeat() refused held-repeat targets: %v", err)
	}
}

func TestConfigValidateHeldRepeat_AccelInvalid(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.Config)
	}{
		{
			name: "negative ramp",
			mutate: func(c *config.Config) {
				c.HeldRepeat.AccelRampMs = -1
			},
		},
		{
			name: "multiplier below one",
			mutate: func(c *config.Config) {
				c.HeldRepeat.AccelMaxMultiplier = 0.5
			},
		},
		{
			name: "multiplier above the upper bound",
			mutate: func(c *config.Config) {
				c.HeldRepeat.AccelMaxMultiplier = config.MaxHeldRepeatAccelMultiplier + 1
			},
		},
		{
			name: "multiplier is NaN",
			mutate: func(c *config.Config) {
				c.HeldRepeat.AccelMaxMultiplier = math.NaN()
			},
		},
		{
			name: "multiplier is Inf",
			mutate: func(c *config.Config) {
				c.HeldRepeat.AccelMaxMultiplier = math.Inf(1)
			},
		},
		{
			name: "no targets while acceleration is enabled",
			mutate: func(c *config.Config) {
				c.HeldRepeat.AccelEnabled = true
				c.HeldRepeat.AccelTargets = nil
			},
		},
		{
			name: "target action does not repeat while held",
			mutate: func(c *config.Config) {
				c.HeldRepeat.AccelTargets = []string{"left_click"}
			},
		},
		{
			name: "target action is unknown",
			mutate: func(c *config.Config) {
				c.HeldRepeat.AccelTargets = []string{"move_mouse_relatve"}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			tt.mutate(cfg)

			err := cfg.ValidateHeldRepeat(nil)
			if err == nil {
				t.Error("ValidateHeldRepeat() expected error, got nil")
			}
		})
	}
}

// TestConfigValidateHeldRepeat_AccelWithoutRepeatWarns pins which tier the
// cross-field rule sits in. Acceleration only ever scales a repeat, so turning
// it on without repeat itself does nothing — but refusing the file over it
// would replace every other setting in it with the defaults (ADR 0002), and
// reporting it to the log alone would hide it from `neru config validate`,
// which is the command someone runs to find exactly this (ADR 0006).
func TestConfigValidateHeldRepeat_AccelWithoutRepeatWarns(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.HeldRepeat.Enabled = false
	cfg.HeldRepeat.AccelEnabled = true

	warnings := &config.Warnings{}

	err := cfg.ValidateWithWarnings(warnings, config.WrittenConfig{})
	if err != nil {
		t.Fatalf("ValidateWithWarnings() refused a configuration that loads: %v", err)
	}

	want := []string{
		"held_repeat.accel_enabled has no effect while held_repeat.enabled is false",
	}

	if got := warnings.Messages(); !slices.Equal(got, want) {
		t.Errorf("warnings = %q, want %q", got, want)
	}
}

// TestConfigValidateHeldRepeat_AccelWithRepeatIsSilent is the other half: the
// warning names a combination, so it must not fire on the one that works.
func TestConfigValidateHeldRepeat_AccelWithRepeatIsSilent(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.HeldRepeat.Enabled = true
	cfg.HeldRepeat.AccelEnabled = true

	warnings := &config.Warnings{}

	err := cfg.ValidateWithWarnings(warnings, config.WrittenConfig{})
	if err != nil {
		t.Fatalf("ValidateWithWarnings() refused the working combination: %v", err)
	}

	if got := warnings.Messages(); len(got) > 0 {
		t.Errorf("warnings = %q, want none", got)
	}
}

func TestHeldRepeatAccelAppliesTo(t *testing.T) {
	enabled := config.HeldRepeatConfig{
		AccelEnabled: true,
		AccelTargets: []string{testMoveMouseRelative},
	}

	if !enabled.AccelAppliesTo(testMoveMouseRelative) {
		t.Error("expected move_mouse_relative to be accelerated")
	}

	if enabled.AccelAppliesTo("scroll_up") {
		t.Error("expected action outside the allowlist to be left alone")
	}

	if enabled.AccelAppliesTo("") {
		t.Error("expected empty action name to be left alone")
	}

	disabled := enabled
	disabled.AccelEnabled = false

	if disabled.AccelAppliesTo(testMoveMouseRelative) {
		t.Error("expected no acceleration while accel_enabled is false")
	}
}
