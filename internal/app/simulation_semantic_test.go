package app_test

import (
	"context"
	"testing"

	"github.com/y3owk1n/neru/internal/domain"
	"github.com/y3owk1n/neru/internal/domain/semantic"
)

// semanticHotkey opens the search that picks a hint by describing it, with
// the binding a user writes for it: a click, toggled by the same chord.
const (
	semanticHotkey  = "Primary+Shift+Z"
	semanticBinding = "hints --action left_click --toggle --search --semantic"
)

// TestSimulation_SemanticHintSearchClicksTheDescribedControl is the journey:
// the chord shows every target's boundary with no labels and opens the field,
// typing sends nothing, Return asks the selector once, and its pick is clicked
// through the confirm path a typed label takes. The chord pressed while the
// search is up toggles hints off.
func TestSimulation_SemanticHintSearchClicksTheDescribedControl(t *testing.T) {
	cfg := simConfig()
	cfg.Hints.BoundaryHighlight.Enabled = true
	cfg.Hotkeys.Bindings[semanticHotkey] = []string{semanticBinding}

	elements := threeButtons(t)
	sim := newSimHarness(t, cfg, elements)
	sim.textInput.Started = true

	// The selector picks the candidate titled "Search"; the fake stands in for
	// the remote model and never reaches the network.
	sim.selector.SelectFunc = func(
		_ context.Context,
		_ string,
		candidates []semantic.Candidate,
	) (semantic.Selection, error) {
		for index, candidate := range candidates {
			if candidate.Title == "Search" {
				return semantic.Selection{Index: index, Matched: true}, nil
			}
		}

		return semantic.Selection{}, nil
	}

	sim.pressHotkey(semanticHotkey)
	sim.waitMode(domain.ModeHints)
	sim.waitFor("search field opened", func() bool { return sim.textInput.StartCount() == 1 })

	if !sim.overlay.lastHintLabelsHidden() {
		t.Fatal("the semantic search drew hint labels; want only the target boundaries")
	}

	if labels := sim.overlay.lastHintLabels(); len(labels) != len(elements) {
		t.Fatalf("boundaries drawn for %d targets, want all %d", len(labels), len(elements))
	}

	callbacks := sim.textInput.Callbacks()
	callbacks.OnQueryChanged("the search box")

	if asked := len(sim.selector.Queries()); asked != 0 {
		t.Fatalf("typing asked the selector %d times, want 0", asked)
	}

	callbacks.OnConfirm()

	sim.waitFor("the described control clicked", func() bool {
		return len(sim.ax.recordedClicks()) == 1
	})

	target := elements[2].Center()
	if clicks := sim.ax.recordedClicks(); clicks[0].point != target {
		t.Fatalf("click landed at %v, want the chosen hint's center %v", clicks[0].point, target)
	}

	if queries := sim.selector.Queries(); len(queries) != 1 || queries[0] != "the search box" {
		t.Errorf("selector asked %q, want the submitted query once", queries)
	}

	// A one-shot click returns to idle, as it does for a typed label.
	sim.waitMode(domain.ModeIdle)

	sim.pressHotkey(semanticHotkey)
	sim.waitMode(domain.ModeHints)
	sim.waitFor("search field opened again", func() bool { return sim.textInput.StartCount() == 2 })

	sim.pressHotkey(semanticHotkey)
	sim.waitMode(domain.ModeIdle)

	if clicks := sim.ax.recordedClicks(); len(clicks) != 1 {
		t.Errorf("clicks = %d after toggling off, want still 1", len(clicks))
	}
}

// TestSimulation_SemanticHintSearchKeepsTheSearchOnNoMatch covers the other
// answer: nothing is clicked, the field and the boundaries stay, the status
// says why, and Tab hands over to the labels.
func TestSimulation_SemanticHintSearchKeepsTheSearchOnNoMatch(t *testing.T) {
	cfg := simConfig()
	cfg.Hints.BoundaryHighlight.Enabled = true
	cfg.Hotkeys.Bindings[semanticHotkey] = []string{semanticBinding}

	elements := threeButtons(t)
	sim := newSimHarness(t, cfg, elements)
	sim.textInput.Started = true

	sim.pressHotkey(semanticHotkey)
	sim.waitMode(domain.ModeHints)
	sim.waitFor("search field opened", func() bool { return sim.textInput.StartCount() == 1 })

	callbacks := sim.textInput.Callbacks()
	callbacks.OnQueryChanged("the print button")
	callbacks.OnConfirm()

	sim.waitFor("the no-match status", func() bool {
		return sim.overlay.lastSearchStatus() == "no single match"
	})

	if clicks := sim.ax.recordedClicks(); len(clicks) != 0 {
		t.Errorf("a no-match clicked %v", clicks)
	}

	if stops := sim.textInput.StopCount(); stops != 0 {
		t.Errorf("search field stopped %d times on no match, want it kept open", stops)
	}

	if !sim.overlay.lastHintLabelsHidden() {
		t.Error("the labels came back on no match; want the boundaries kept")
	}

	if !sim.textInput.EmitTab() {
		t.Fatal("the semantic search registered no Tab handler")
	}

	sim.waitFor("labels back after Tab", func() bool {
		return !sim.overlay.lastHintLabelsHidden() &&
			len(sim.overlay.lastHintLabels()) == len(elements)
	})

	if mode := sim.app.CurrentMode(); mode != domain.ModeHints {
		t.Errorf("mode after Tab = %s, want hints", domain.ModeString(mode))
	}
}

// TestSimulation_PlainSearchOverASemanticSessionIsPlain covers a plain
// `hints --search` binding pressed while a semantic search is open. That is a
// refresh, and --semantic is never inherited across one: the labels come
// back, typing filters them locally, and Return clicks the match without
// asking the selector.
func TestSimulation_PlainSearchOverASemanticSessionIsPlain(t *testing.T) {
	const plainSearchHotkey = "Primary+Shift+F"

	cfg := simConfig()
	cfg.Hints.BoundaryHighlight.Enabled = true
	cfg.Hotkeys.Bindings[semanticHotkey] = []string{semanticBinding}
	cfg.Hotkeys.Bindings[plainSearchHotkey] = []string{"hints --action left_click --search"}

	elements := threeButtons(t)
	sim := newSimHarness(t, cfg, elements)
	sim.textInput.Started = true

	sim.pressHotkey(semanticHotkey)
	sim.waitMode(domain.ModeHints)
	sim.waitFor("semantic field opened", func() bool { return sim.textInput.StartCount() == 1 })

	sim.pressHotkey(plainSearchHotkey)
	sim.waitFor("plain field opened", func() bool { return sim.textInput.StartCount() == 2 })

	if sim.overlay.lastHintLabelsHidden() {
		t.Fatal("labels still hidden after a plain search refreshed the session")
	}

	callbacks := sim.textInput.Callbacks()
	if callbacks.OnTab != nil {
		t.Error("the plain search took Tab from the field")
	}

	callbacks.OnQueryChanged("cancel")
	sim.waitFor("hints narrowed locally", func() bool {
		return len(sim.overlay.lastHintLabels()) == 1
	})

	callbacks.OnConfirm()
	sim.waitFor("the match clicked", func() bool { return len(sim.ax.recordedClicks()) == 1 })

	if clicks := sim.ax.recordedClicks(); clicks[0].point != elements[1].Center() {
		t.Errorf("click at %v, want the text match at %v", clicks[0].point, elements[1].Center())
	}

	if asked := len(sim.selector.Queries()); asked != 0 {
		t.Errorf("a plain search asked the selector %d times, want 0", asked)
	}
}
