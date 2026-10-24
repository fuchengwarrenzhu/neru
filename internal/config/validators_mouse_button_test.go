package config_test

import (
	"strings"
	"testing"

	"github.com/y3owk1n/neru/internal/config"
)

// A mode's table may bind a mouse button; the macOS mode tap reports the click
// while the mode is up.
func TestConfig_Validate_AcceptsAMouseButtonInAModesTable(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Scroll.Hotkeys = map[string]config.StringOrStringArray{"MouseLeft": {"idle"}}
	cfg.Modes = map[string]config.CustomModeConfig{
		"cursor": {Hotkeys: map[string]config.StringOrStringArray{
			"MouseRight":  {"idle"},
			"MouseMiddle": {"idle"},
		}},
	}

	err := cfg.Validate()
	if err != nil {
		t.Fatalf("Validate() = %v, want a mode table to accept mouse buttons", err)
	}
}

// Nothing reports a click while no mode is active, so a global binding for a
// button could never fire, and a button takes no modifiers anywhere.
func TestConfig_Validate_RefusesAMouseButtonOutsideAModesTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(cfg *config.Config)
		want   string
	}{
		{
			name: "global hotkeys",
			mutate: func(cfg *config.Config) {
				cfg.Hotkeys.Bindings = map[string][]string{"MouseLeft": {"idle"}}
			},
			want: "MouseLeft",
		},
		{
			name: "global app_configs hotkeys",
			mutate: func(cfg *config.Config) {
				cfg.AppConfigs = []config.AppConfig{{
					BundleID: "com.example.app",
					Hotkeys:  map[string]config.StringOrStringArray{"MouseLeft": {"idle"}},
				}}
			},
			want: "a mouse button binds only in a mode's hotkeys",
		},
		{
			name: "a button with a modifier",
			mutate: func(cfg *config.Config) {
				cfg.Scroll.Hotkeys = map[string]config.StringOrStringArray{
					"Shift+MouseLeft": {"idle"},
				}
			},
			want: "Shift+MouseLeft",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.DefaultConfig()
			testCase.mutate(cfg)

			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("Validate() = %v, want a refusal naming %q", err, testCase.want)
			}
		})
	}
}
