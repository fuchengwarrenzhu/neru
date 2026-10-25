package modes

import (
	"context"
	"image"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/y3owk1n/neru/internal/app/components"
	hintscomponent "github.com/y3owk1n/neru/internal/app/components/hints"
	"github.com/y3owk1n/neru/internal/app/components/scroll"
	"github.com/y3owk1n/neru/internal/app/services"
	"github.com/y3owk1n/neru/internal/config"
	"github.com/y3owk1n/neru/internal/derrors"
	"github.com/y3owk1n/neru/internal/domain"
	"github.com/y3owk1n/neru/internal/domain/element"
	domainhint "github.com/y3owk1n/neru/internal/domain/hint"
	"github.com/y3owk1n/neru/internal/domain/semantic"
	"github.com/y3owk1n/neru/internal/domain/state"
	"github.com/y3owk1n/neru/internal/ports"
	portmocks "github.com/y3owk1n/neru/internal/ports/mocks"
)

// semanticQuery is what the user types; semanticWaitBudget bounds every wait
// for the answer goroutine to come back through the lock.
const (
	semanticQuery      = "open the code menu"
	semanticWaitBudget = 5 * time.Second
	semanticAppID      = "com.example.Editor"
)

// The two input paths a search can take, and the log line a failed selection
// writes.
const (
	pathSearchField = "search field"
	pathKeyStream   = "key stream"
	logFailed       = "Semantic selection failed"
)

// selectorAnswer is what the gated selector returns for one question.
type selectorAnswer struct {
	selection semantic.Selection
	err       error
}

// semanticFixture is a handler sitting in hints mode with three hints and a
// --semantic search open, over a selector that answers only when the test
// says so.
type semanticFixture struct {
	handler  *Handler
	overlay  *portmocks.MockOverlayPort
	field    *portmocks.MockTextInputPort
	selector *portmocks.MockTargetSelector
	logs     *observer.ObservedLogs
	hints    []*domainhint.Interface

	asked   chan struct{}
	answers chan selectorAnswer

	movesMu sync.Mutex
	moves   []image.Point
}

func semanticElement(t *testing.T, id, title string, bounds image.Rectangle) *element.Element {
	t.Helper()

	elem, err := element.NewElement(
		element.ID(id),
		bounds,
		element.RoleButton,
		element.WithTitle(title),
		element.WithDescription(title+" description"),
		element.WithValue("draft the user typed"),
		element.WithSubrole("AXToolbarButton"),
		element.WithContext("AXToolbar", "Repository"),
		element.WithHasPopup(true),
	)
	if err != nil {
		t.Fatalf("NewElement() error = %v", err)
	}

	return elem
}

// newSemanticFixture opens a --semantic search. field says whether the
// platform's search field takes the keys; without one they arrive through the
// key stream.
func newSemanticFixture(t *testing.T, field bool) *semanticFixture {
	t.Helper()

	core, logs := observer.New(zapcore.DebugLevel)

	fixture := &semanticFixture{
		overlay: &portmocks.MockOverlayPort{
			HintSearchBoundsFunc: func(image.Rectangle) image.Rectangle {
				return image.Rect(400, 740, 600, 780)
			},
		},
		field:   &portmocks.MockTextInputPort{Started: field},
		logs:    logs,
		asked:   make(chan struct{}, 8),
		answers: make(chan selectorAnswer),
	}

	fixture.selector = &portmocks.MockTargetSelector{
		SelectFunc: func(
			_ context.Context,
			_ string,
			_ []semantic.Candidate,
		) (semantic.Selection, error) {
			fixture.asked <- struct{}{}

			answer, open := <-fixture.answers
			if !open {
				return semantic.Selection{}, derrors.New(derrors.CodeContextCanceled, "test over")
			}

			return answer.selection, answer.err
		},
	}

	t.Cleanup(func() { close(fixture.answers) })

	appState := state.NewAppState()
	appState.SetMode(domain.ModeHints)

	fixture.handler = newHandlerWithState(handlerState{
		ctx:         context.Background(),
		config:      config.DefaultConfig(),
		logger:      zap.New(core),
		appState:    appState,
		cursorState: state.NewCursorState(),
		scroll:      &components.ScrollComponent{Context: &scroll.Context{}},
		hints: &components.HintsComponent{
			Context: &hintscomponent.Context{},
		},
		overlayPort:           fixture.overlay,
		textInput:             fixture.field,
		targetSelector:        fixture.selector,
		targetSelectionBudget: semanticWaitBudget,
		actionService: services.NewActionService(
			&portmocks.MockAccessibilityPort{},
			fixture.overlay,
			&portmocks.MockSystemPort{
				MoveCursorToPointFunc: func(_ context.Context, point image.Point, _ bool) error {
					fixture.movesMu.Lock()
					fixture.moves = append(fixture.moves, point)
					fixture.movesMu.Unlock()

					return nil
				},
			},
			zap.NewNop(),
		),
		screenBounds: image.Rect(0, 0, 1000, 800),
	})

	fixture.hints = []*domainhint.Interface{
		mustNewModeHint("A", semanticElement(t, "save", "Save", image.Rect(10, 10, 90, 40))),
		mustNewModeHint("S", semanticElement(t, "code", "Code", image.Rect(110, 10, 190, 40))),
		mustNewModeHint("D", semanticElement(t, "help", "Help", image.Rect(210, 10, 290, 40))),
	}

	fixture.locked(func(locked *handlerState) {
		manager := domainhint.NewManager(locked.logger, &fixture.handler.mu)
		manager.SetUpdateCallback(locked.drawHints)
		locked.hints.Context.SetManager(manager)

		setErr := locked.hints.Context.SetHints(domainhint.NewCollection(fixture.hints))
		if setErr != nil {
			t.Fatalf("SetHints() error = %v", setErr)
		}

		locked.hints.Context.SetSemantic(true)
		locked.hints.Context.SetScannedApp(semanticAppID)

		startErr := locked.startHintSearch(false)
		if startErr != nil {
			t.Fatalf("startHintSearch() error = %v", startErr)
		}
	})

	return fixture
}

// locked runs fn holding the handler's lock, the way every entry point does.
func (f *semanticFixture) locked(fn func(h *handlerState)) {
	f.handler.mu.Lock()
	defer f.handler.mu.Unlock()

	fn(&f.handler.handlerState)
}

// typeQuery puts text in the search the way the active input path does.
func (f *semanticFixture) typeQuery(text string) {
	if f.field.Started {
		f.field.EmitQueryChanged(text)

		return
	}

	for key := range strings.SplitSeq(text, "") {
		if key == " " {
			key = config.KeyNameSpace
		}

		f.press(key)
	}
}

// press hands one key to the key-stream search path.
func (f *semanticFixture) press(key string) {
	f.locked(func(h *handlerState) { h.handleSearchInputKey(key) })
}

// submit presses Return on the active input path and waits until the
// selector has been asked.
func (f *semanticFixture) submit(t *testing.T) {
	t.Helper()

	if f.field.Started {
		f.field.EmitConfirm()
	} else {
		f.press(config.KeyNameReturn)
	}

	select {
	case <-f.asked:
	case <-time.After(semanticWaitBudget):
		t.Fatal("Return did not ask the target selector")
	}
}

// answer hands the selector's answer to the question in flight.
func (f *semanticFixture) answer(t *testing.T, answer selectorAnswer) {
	t.Helper()

	select {
	case f.answers <- answer:
	case <-time.After(semanticWaitBudget):
		t.Fatal("no question was waiting for an answer")
	}
}

// waitForLog waits for a debug line with the given message, and returns it.
func (f *semanticFixture) waitForLog(t *testing.T, message string) observer.LoggedEntry {
	t.Helper()

	deadline := time.Now().Add(semanticWaitBudget)

	for time.Now().Before(deadline) {
		entries := f.logs.FilterMessage(message).All()
		if len(entries) > 0 {
			return entries[len(entries)-1]
		}

		time.Sleep(time.Millisecond)
	}

	t.Fatalf("timed out waiting for the log line %q", message)

	return observer.LoggedEntry{}
}

func (f *semanticFixture) recordedMoves() []image.Point {
	f.movesMu.Lock()
	defer f.movesMu.Unlock()

	return append([]image.Point(nil), f.moves...)
}

func (f *semanticFixture) status() string {
	search, _ := f.overlay.LastHintSearch()

	return search.Status
}

// lastHintsFrame is the most recent hints frame handed to the overlay.
func (f *semanticFixture) lastHintsFrame(t *testing.T) ports.HintsFrame {
	t.Helper()

	frames := f.overlay.Frames()
	for _, frame := range slices.Backward(frames) {
		if frame, ok := frame.(ports.HintsFrame); ok {
			return frame
		}
	}

	t.Fatal("no hints frame was drawn")

	return ports.HintsFrame{}
}

func TestHandler_SubmitSemanticSearch_AsksOnceOnReturnAndShowsPending(t *testing.T) {
	for _, path := range []struct {
		name  string
		field bool
	}{
		{name: pathSearchField, field: true},
		{name: pathKeyStream, field: false},
	} {
		t.Run(path.name, func(t *testing.T) {
			fixture := newSemanticFixture(t, path.field)

			fixture.typeQuery(semanticQuery)

			if asked := len(fixture.selector.Queries()); asked != 0 {
				t.Fatalf("typing asked the selector %d times, want 0", asked)
			}

			fixture.locked(func(h *handlerState) {
				if got := h.hints.Context.Hints().Count(); got != len(fixture.hints) {
					t.Errorf("typing narrowed the hints to %d, want all %d in view",
						got, len(fixture.hints))
				}
			})

			fixture.submit(t)

			queries := fixture.selector.Queries()
			if len(queries) != 1 || queries[0] != semanticQuery {
				t.Fatalf("selector asked %q, want exactly %q once", queries, semanticQuery)
			}

			candidates := fixture.selector.Candidates()[0]
			if len(candidates) != len(fixture.hints) {
				t.Fatalf("candidates = %d, want one per hint (%d)",
					len(candidates), len(fixture.hints))
			}

			want := semantic.Candidate{
				Role:        string(element.RoleButton),
				Subrole:     "AXToolbarButton",
				Title:       "Code",
				Description: "Code description",
				ParentRole:  "AXToolbar",
				ParentTitle: "Repository",
				AppName:     semanticAppID,
				HasPopup:    true,
			}
			if candidates[1] != want {
				t.Errorf("candidate = %+v, want %+v", candidates[1], want)
			}

			if got := fixture.status(); got != semanticStatusPending {
				t.Errorf("status while asking = %q, want %q", got, semanticStatusPending)
			}

			fixture.locked(func(h *handlerState) {
				if !h.hints.Context.SearchActive() {
					t.Error("the search closed while the selector was asked")
				}
			})

			fixture.answer(t, selectorAnswer{})
			fixture.waitForLog(t, "Semantic selection found no single match")
		})
	}
}

func TestHandler_ApplySemanticSelection_NoMatchKeepsTheSearchOpen(t *testing.T) {
	tests := []struct {
		name   string
		answer selectorAnswer
		log    string
		status string
	}{
		{
			name:   "none",
			answer: selectorAnswer{},
			log:    "Semantic selection found no single match",
			status: semanticStatusNoMatch,
		},
		{
			name: "no API key",
			answer: selectorAnswer{
				err: derrors.New(derrors.CodeCredentialMissing, "no key"),
			},
			log:    logFailed,
			status: semanticStatusNoKey,
		},
		{
			name:   "timeout",
			answer: selectorAnswer{err: derrors.New(derrors.CodeTimeout, "slow")},
			log:    logFailed,
			status: semanticStatusTimedOut,
		},
		{
			name:   "any other failure",
			answer: selectorAnswer{err: derrors.New(derrors.CodeRemoteFailed, "broken")},
			log:    logFailed,
			status: semanticStatusFailed,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newSemanticFixture(t, true)

			fixture.typeQuery(semanticQuery)
			fixture.submit(t)
			fixture.answer(t, testCase.answer)
			fixture.waitForLog(t, testCase.log)

			if got := fixture.status(); got != testCase.status {
				t.Errorf("status = %q, want %q", got, testCase.status)
			}

			if moves := fixture.recordedMoves(); len(moves) != 0 {
				t.Errorf("cursor moved to %v, want nothing selected", moves)
			}

			if stops := fixture.field.StopCount(); stops != 0 {
				t.Errorf("search field stopped %d times, want it left open", stops)
			}

			fixture.locked(func(h *handlerState) {
				ctx := h.hints.Context
				if !ctx.SearchActive() || ctx.SearchQuery() != semanticQuery {
					t.Errorf("search active = %v with query %q, want it open with %q",
						ctx.SearchActive(), ctx.SearchQuery(), semanticQuery)
				}

				if got := ctx.Hints().Count(); got != len(fixture.hints) {
					t.Errorf(
						"visible hints = %d, want every boundary (%d)",
						got,
						len(fixture.hints),
					)
				}
			})

			if frame := fixture.lastHintsFrame(t); !frame.LabelsHidden {
				t.Error("the labels came back; the search should still show only boundaries")
			}

			// Editing the query leaves the status that described the old one.
			fixture.typeQuery(semanticQuery + " please")

			if got := fixture.status(); got != "" {
				t.Errorf("status after an edit = %q, want it cleared", got)
			}
		})
	}
}

func TestHandler_ApplySemanticSelection_MatchSelectsThroughTheConfirmPath(t *testing.T) {
	fixture := newSemanticFixture(t, true)

	fixture.typeQuery(semanticQuery)
	fixture.submit(t)
	fixture.answer(t, selectorAnswer{selection: semantic.Selection{Index: 1, Matched: true}})
	fixture.waitForLog(t, "Semantic selection matched")

	chosen := fixture.hints[1].Element().Center()

	moves := fixture.recordedMoves()
	if len(moves) != 1 || moves[0] != chosen {
		t.Fatalf("cursor moves = %v, want one to the chosen hint's center %v", moves, chosen)
	}

	fixture.locked(func(h *handlerState) {
		ctx := h.hints.Context
		if ctx.SearchActive() {
			t.Error("the search is still open after a match")
		}

		visible := ctx.Hints().All()
		if len(visible) != 1 || visible[0] != fixture.hints[1] {
			t.Errorf("visible hints = %d, want only the chosen one", len(visible))
		}
	})

	if stops := fixture.field.StopCount(); stops != 1 {
		t.Errorf("search field stopped %d times, want once by the confirm", stops)
	}
}

func TestHandler_ApplySemanticSelection_DropsAStaleAnswer(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		// change is what happens while the selector is still thinking.
		change func(t *testing.T, fixture *semanticFixture)
	}{
		{
			name:   "a newer submit",
			reason: staleSuperseded,
			change: func(t *testing.T, fixture *semanticFixture) {
				t.Helper()

				fixture.submit(t)
			},
		},
		{
			name:   "Tab to the labels",
			reason: staleSearchClosed,
			change: func(t *testing.T, fixture *semanticFixture) {
				t.Helper()

				if !fixture.field.EmitTab() {
					t.Fatal("the semantic search registered no Tab handler")
				}
			},
		},
		{
			name:   "hints scanned again",
			reason: staleRescanned,
			change: func(t *testing.T, fixture *semanticFixture) {
				t.Helper()

				fixture.locked(func(h *handlerState) {
					rescanned := domainhint.NewCollection(fixture.hints)

					setErr := h.hints.Context.SetHints(rescanned)
					if setErr != nil {
						t.Fatalf("SetHints() error = %v", setErr)
					}
				})
			},
		},
		{
			name:   "Escape out of hints",
			reason: staleModeChanged,
			change: func(t *testing.T, fixture *semanticFixture) {
				t.Helper()

				fixture.field.EmitCancel()
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newSemanticFixture(t, true)

			fixture.typeQuery(semanticQuery)
			fixture.submit(t)
			testCase.change(t, fixture)

			// The first question's answer names a hint; it must not be acted on.
			fixture.answer(
				t,
				selectorAnswer{selection: semantic.Selection{Index: 0, Matched: true}},
			)

			entry := fixture.waitForLog(t, "Semantic selection dropped")
			if got := entry.ContextMap()["reason"]; got != testCase.reason {
				t.Errorf("drop reason = %v, want %q", got, testCase.reason)
			}

			if moves := fixture.recordedMoves(); len(moves) != 0 {
				t.Errorf("a stale answer moved the cursor to %v", moves)
			}
		})
	}
}

func TestHandler_HintSearchTab_BringsTheLabelsBackAndClosesTheField(t *testing.T) {
	for _, path := range []struct {
		name  string
		field bool
		tab   func(t *testing.T, fixture *semanticFixture)
	}{
		{
			name:  pathSearchField,
			field: true,
			tab: func(t *testing.T, fixture *semanticFixture) {
				t.Helper()

				if !fixture.field.EmitTab() {
					t.Fatal("the semantic search registered no Tab handler")
				}
			},
		},
		{
			name:  pathKeyStream,
			field: false,
			tab: func(_ *testing.T, fixture *semanticFixture) {
				fixture.press(config.KeyNameTab)
			},
		},
	} {
		t.Run(path.name, func(t *testing.T) {
			fixture := newSemanticFixture(t, path.field)

			if frame := fixture.lastHintsFrame(t); !frame.LabelsHidden {
				t.Fatal("the semantic search drew the labels; want boundaries only")
			}

			fixture.typeQuery("code")
			path.tab(t, fixture)

			frame := fixture.lastHintsFrame(t)
			if frame.LabelsHidden {
				t.Error("labels still hidden after Tab")
			}

			if len(frame.Hints) != len(fixture.hints) {
				t.Errorf("Tab drew %d hints, want every labeled hint (%d)",
					len(frame.Hints), len(fixture.hints))
			}

			fixture.locked(func(h *handlerState) {
				if h.hints.Context.SearchActive() {
					t.Error("the search is still open after Tab")
				}

				if mode := h.appState.CurrentMode(); mode != domain.ModeHints {
					t.Errorf("mode after Tab = %s, want hints", domain.ModeString(mode))
				}
			})

			if path.field && fixture.field.StopCount() != 1 {
				t.Errorf("search field stopped %d times, want once", fixture.field.StopCount())
			}
		})
	}
}

func TestHandler_HintSearchEscape_LeavesHintsInASemanticSearch(t *testing.T) {
	for _, path := range []struct {
		name  string
		field bool
	}{
		{name: pathSearchField, field: true},
		{name: pathKeyStream, field: false},
	} {
		t.Run(path.name, func(t *testing.T) {
			fixture := newSemanticFixture(t, path.field)

			if path.field {
				fixture.field.EmitCancel()
			} else {
				fixture.press(config.KeyNameEscape)
			}

			fixture.locked(func(h *handlerState) {
				if mode := h.appState.CurrentMode(); mode != domain.ModeIdle {
					t.Errorf("mode after Escape = %s, want idle", domain.ModeString(mode))
				}
			})
		})
	}
}

// TestHandler_StartHintSearch_PlainSearchKeepsUpstreamKeys pins that a search
// without --semantic is untouched: the field keeps Tab, and Escape steps back
// to the labels rather than leaving hints.
func TestHandler_StartHintSearch_PlainSearchKeepsUpstreamKeys(t *testing.T) {
	overlay := &portmocks.MockOverlayPort{
		HintSearchBoundsFunc: func(image.Rectangle) image.Rectangle {
			return image.Rect(400, 740, 600, 780)
		},
	}
	field := &portmocks.MockTextInputPort{Started: true}

	handler := newHintSearchTestHandler(t, overlay, field)

	handler.mu.Lock()
	startErr := handler.startHintSearch(false)
	handler.mu.Unlock()

	if startErr != nil {
		t.Fatalf("startHintSearch() error = %v", startErr)
	}

	if field.EmitTab() {
		t.Error("a plain search took Tab from the field")
	}

	field.EmitCancel()

	handler.mu.Lock()
	defer handler.mu.Unlock()

	if handler.hints.Context.SearchActive() {
		t.Error("Escape left the plain search open")
	}

	if mode := handler.appState.CurrentMode(); mode != domain.ModeHints {
		t.Errorf("mode after Escape = %s, want hints with the labels back", domain.ModeString(mode))
	}
}

// TestHandler_SubmitSemanticSearch_LogsNoQueryOrUIText pins the privacy rule
// for the one feature that sends UI text off the machine: the log says how
// many candidates, how long and what came of it, never what was asked or what
// the controls are called.
func TestHandler_SubmitSemanticSearch_LogsNoQueryOrUIText(t *testing.T) {
	fixture := newSemanticFixture(t, true)

	fixture.typeQuery(semanticQuery)
	fixture.submit(t)
	fixture.answer(t, selectorAnswer{})
	fixture.waitForLog(t, "Semantic selection found no single match")

	forbidden := []string{
		semanticQuery,
		"Save",
		"Code",
		"Help",
		"Repository",
		"draft the user typed",
	}

	for _, entry := range fixture.logs.All() {
		texts := []string{entry.Message}

		for _, value := range entry.ContextMap() {
			if text, ok := value.(string); ok {
				texts = append(texts, text)
			}
		}

		for _, text := range texts {
			for _, secret := range forbidden {
				if strings.Contains(text, secret) {
					t.Errorf("log line %q carries %q", entry.Message, secret)
				}
			}
		}
	}
}
