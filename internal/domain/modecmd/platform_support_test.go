package modecmd_test

import (
	"slices"
	"testing"

	"github.com/y3owk1n/neru/internal/domain/modecmd"
	"github.com/y3owk1n/neru/internal/domain/parity"
)

// TestPlatformSupport_SemanticIsMacOSOnly pins --semantic's column: the view
// it opens hides labels and hands Tab back to them, which only the macOS
// overlay and search field do, so writing it anywhere else warns at load.
func TestPlatformSupport_SemanticIsMacOSOnly(t *testing.T) {
	t.Parallel()

	declaration := modecmd.PlatformSupport()

	tests := []struct {
		flag modecmd.Flag
		want parity.Platforms
	}{
		{flag: modecmd.FlagSemantic, want: parity.Platforms{parity.Darwin}},
		{flag: modecmd.FlagSearch, want: parity.AllPlatforms},
	}

	for _, testCase := range tests {
		t.Run(testCase.flag.String(), func(t *testing.T) {
			t.Parallel()

			word, declared := declaration.Lookup(parity.KindModeFlag, testCase.flag.String(), "")
			if !declared {
				t.Fatalf("%s has no platform column", testCase.flag.Long())
			}

			if !slices.Equal(word.Platforms, testCase.want) {
				t.Errorf(
					"%s platforms = %v, want %v",
					testCase.flag.Long(),
					word.Platforms,
					testCase.want,
				)
			}

			if !word.Platforms.Everywhere() && word.Note == "" {
				t.Errorf("%s is narrower than every platform and says nothing about why",
					testCase.flag.Long())
			}
		})
	}
}
