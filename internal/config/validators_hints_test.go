package config_test

import (
	"strings"
	"testing"

	"github.com/y3owk1n/neru/internal/config"
)

const (
	dirNormal     = "normal"
	dirReverse    = "reverse"
	bundleExample = "com.example"
	bundleOther   = "com.example.other"
)

func TestValidateHints_UIPlacement(t *testing.T) {
	validPlacements := []string{
		"top",
		"center",
		"bottom",
	}

	for _, placement := range validPlacements {
		cfg := config.DefaultConfig()
		cfg.Hints.UI.Placement = placement

		err := cfg.ValidateHints(nil)
		if err != nil {
			t.Fatalf("ValidateHints() expected placement %q to be valid, got %v", placement, err)
		}
	}

	cfg := config.DefaultConfig()

	cfg.Hints.UI.Placement = "floating"

	err := cfg.ValidateHints(nil)
	if err == nil {
		t.Fatal("ValidateHints() expected error for invalid hints.ui.placement")
	}
}

func TestValidateHints_PositiveUnitFloat(t *testing.T) {
	// merge_iou_threshold cannot be 0
	cfg := config.DefaultConfig()
	cfg.Hints.Vision.MergeIOUThreshold = 0.0

	err := cfg.ValidateHints(nil)
	if err == nil {
		t.Fatal("ValidateHints() expected error for 0.0 merge_iou_threshold")
	}

	// button_min_confidence cannot be 0
	cfg = config.DefaultConfig()
	cfg.Hints.Vision.ButtonMinConfidence = 0.0

	err = cfg.ValidateHints(nil)
	if err == nil {
		t.Fatal("ValidateHints() expected error for 0.0 button_min_confidence")
	}

	// generic_clickable_min_confidence cannot be 0
	cfg = config.DefaultConfig()
	cfg.Hints.Vision.GenericClickableMinConfidence = 0.0

	err = cfg.ValidateHints(nil)
	if err == nil {
		t.Fatal("ValidateHints() expected error for 0.0 generic_clickable_min_confidence")
	}

	// but minimum_confidence CAN be 0
	cfg = config.DefaultConfig()
	cfg.Hints.Vision.MinimumConfidence = 0.0

	err = cfg.ValidateHints(nil)
	if err != nil {
		t.Fatalf("ValidateHints() expected no error for 0.0 minimum_confidence, got %v", err)
	}
}

func TestValidateHints_LabelDirection(t *testing.T) {
	tests := []struct {
		name      string
		direction string
		wantErr   bool
	}{
		{name: "reverse is valid", direction: dirReverse, wantErr: false},
		{name: "normal is valid", direction: dirNormal, wantErr: false},
		{name: "empty defaults to normal (no error)", direction: "", wantErr: false},
		{name: "unknown value is rejected", direction: "sideways", wantErr: true},
		{name: "uppercase is rejected (case sensitive)", direction: "NORMAL", wantErr: true},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Hints.LabelDirection = testCase.direction

			err := cfg.ValidateHints(nil)
			if testCase.wantErr && err == nil {
				t.Fatalf(
					"ValidateHints() expected error for label_direction=%q, got nil",
					testCase.direction,
				)
			}

			if !testCase.wantErr && err != nil {
				t.Fatalf(
					"ValidateHints() unexpected error for label_direction=%q: %v",
					testCase.direction,
					err,
				)
			}
		})
	}
}

func TestValidateAppConfigs_LabelDirection(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Hints.AppConfigs = []config.AppConfig{
		{
			BundleID:       bundleExample,
			LabelDirection: dirNormal,
		},
	}

	err := cfg.ValidateAppConfigs()
	if err != nil {
		t.Fatalf("ValidateAppConfigs() unexpected error for valid app config: %v", err)
	}

	cfg = config.DefaultConfig()
	cfg.Hints.AppConfigs = []config.AppConfig{
		{
			BundleID:       bundleExample,
			LabelDirection: "diagonal",
		},
	}

	err = cfg.ValidateAppConfigs()
	if err == nil {
		t.Fatal("ValidateAppConfigs() expected error for invalid app label_direction")
	}
}

func TestLabelDirectionForApp(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Hints.LabelDirection = dirReverse
	cfg.Hints.AppConfigs = []config.AppConfig{
		{
			BundleID:       bundleExample,
			LabelDirection: dirNormal,
		},
	}

	// App override takes precedence.
	if got := cfg.Hints.LabelDirectionForApp(bundleExample); got != dirNormal {
		t.Errorf("LabelDirectionForApp(app with override) = %q, want %q", got, dirNormal)
	}

	// Fallback to global config.
	if got := cfg.Hints.LabelDirectionForApp("com.other"); got != dirReverse {
		t.Errorf("LabelDirectionForApp(app without override) = %q, want %q", got, dirReverse)
	}

	// Empty global value normalizes to the default (normal).
	cfg = config.DefaultConfig()
	cfg.Hints.LabelDirection = ""

	if got := cfg.Hints.LabelDirectionForApp(bundleExample); got != dirNormal {
		t.Errorf("LabelDirectionForApp(empty global) = %q, want %q", got, dirNormal)
	}
}

// TestValidateHints_SemanticEndpoint pins what a --semantic search may send its
// question to: an https URL that carries no credential, and an absolute key
// path. The refusals never quote the value, since a refused URL may hold a key.
func TestValidateHints_SemanticEndpoint(t *testing.T) {
	const secret = "s3cret-token"

	tests := []struct {
		name     string
		endpoint string
		keyFile  string
		wantErr  bool
	}{
		{name: "typesafe by default", endpoint: config.DefaultSemanticEndpoint},
		{
			name:     "gateway route and key",
			endpoint: "https://gateway.example.com/typesafe/v1/systemone",
			keyFile:  "/Users/someone/.config/credentials/gateway/neru.apikey",
		},
		{name: "http", endpoint: "http://api.typesafe.ai/v1/systemone", wantErr: true},
		{name: "no endpoint", endpoint: "", wantErr: true},
		{name: "no host", endpoint: "https:///v1/systemone", wantErr: true},
		{name: "user", endpoint: "https://" + secret + "@gateway.example.com/v1", wantErr: true},
		{name: "query", endpoint: "https://gateway.example.com/v1?key=" + secret, wantErr: true},
		{name: "fragment", endpoint: "https://gateway.example.com/v1#" + secret, wantErr: true},
		{
			name:     "relative key file",
			endpoint: config.DefaultSemanticEndpoint,
			keyFile:  "~/.config/credentials/gateway/neru.apikey",
			wantErr:  true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Hints.Semantic.Endpoint = testCase.endpoint
			cfg.Hints.Semantic.APIKeyFile = testCase.keyFile

			err := cfg.ValidateHints(nil)
			if (err != nil) != testCase.wantErr {
				t.Fatalf("ValidateHints() error = %v, wantErr %v", err, testCase.wantErr)
			}

			if err != nil && strings.Contains(err.Error(), secret) {
				t.Fatalf("ValidateHints() error %q quotes the endpoint", err)
			}
		})
	}
}
