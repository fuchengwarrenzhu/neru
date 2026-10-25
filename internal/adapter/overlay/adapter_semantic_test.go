package overlay_test

import (
	"context"
	"image"
	"testing"

	"go.uber.org/zap"

	"github.com/y3owk1n/neru/internal/adapter/overlay"
	renderhints "github.com/y3owk1n/neru/internal/adapter/overlay/render/hints"
	"github.com/y3owk1n/neru/internal/config"
	"github.com/y3owk1n/neru/internal/ports"
)

// styleManager is a screen that also keeps the hint style it was last drawn
// with, which is how a frame's LabelsHidden reaches a renderer.
type styleManager struct {
	*screenManager

	style renderhints.StyleMode
}

func (m *styleManager) DrawHintsWithStyle(
	drawn []*renderhints.Hint,
	style renderhints.StyleMode,
) error {
	m.style = style

	return m.screenManager.DrawHintsWithStyle(drawn, style)
}

// statusManager is a screen whose search badge can say a status, the way the
// macOS one can.
type statusManager struct {
	*screenManager

	status string
}

func (m *statusManager) DrawHintSearchInputStatus(
	query string,
	status string,
	resultCount int,
	frame renderhints.SearchInputFrame,
	style renderhints.SearchInputStyle,
) error {
	m.status = status

	return m.DrawHintSearchInput(query, resultCount, frame, style)
}

var _ overlay.HintSearchStatusDrawer = (*statusManager)(nil)

func TestAdapterShowFrame_LabelsHiddenReachesTheHintStyle(t *testing.T) {
	t.Parallel()

	for _, hidden := range []bool{true, false} {
		manager := &styleManager{screenManager: newScreenManager()}
		adapter := overlay.NewAdapter(manager, testStyles{}, zap.NewNop())

		frame := hintsFrame(t, image.Rect(0, 0, 1920, 1080), "ab", image.Pt(100, 200))
		frame.LabelsHidden = hidden

		err := adapter.ShowFrame(context.Background(), frame)
		if err != nil {
			t.Fatalf("ShowFrame() error = %v", err)
		}

		if got := manager.style.LabelsHidden(); got != hidden {
			t.Errorf("frame LabelsHidden = %v drew a style with LabelsHidden %v", hidden, got)
		}

		// The hints themselves still go to the backend: a hidden label keeps
		// its boundary, which is drawn from the same hint.
		if labels := manager.drawnLabels(); len(labels) != 1 {
			t.Errorf("hints drawn = %v, want the frame's one hint", labels)
		}
	}
}

func TestAdapterDrawHintSearch_DrawsTheStatusWhereTheBackendCanSayOne(t *testing.T) {
	t.Parallel()

	styles := searchStyles{layout: overlay.SearchInputLayout{
		Position: config.SearchInputPositionTopLeft,
		Width:    200,
		Height:   40,
	}}
	search := ports.HintSearch{
		Screen:      image.Rect(0, 0, 1000, 800),
		Query:       "open the code menu",
		ResultCount: 3,
		Status:      "no single match",
	}

	capable := &statusManager{screenManager: newScreenManager()}

	err := overlay.NewAdapter(capable, styles, zap.NewNop()).DrawHintSearch(search)
	if err != nil {
		t.Fatalf("DrawHintSearch() error = %v", err)
	}

	if capable.status != search.Status {
		t.Errorf("status drawn = %q, want %q", capable.status, search.Status)
	}

	// A backend that cannot say a status still draws the box, with the count.
	plain := newScreenManager()

	err = overlay.NewAdapter(plain, styles, zap.NewNop()).DrawHintSearch(search)
	if err != nil {
		t.Fatalf("DrawHintSearch() error = %v", err)
	}

	if plain.searchDraws != 1 || plain.searchResults != search.ResultCount {
		t.Errorf("plain backend drew %d boxes with count %d, want one with %d",
			plain.searchDraws, plain.searchResults, search.ResultCount)
	}
}
