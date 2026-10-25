package semantic_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/y3owk1n/neru/internal/derrors"
	"github.com/y3owk1n/neru/internal/domain/semantic"
)

const testQuery = "open the settings"

// fakeChooser records every question it is asked. Questions before
// leafCalls are leaves and go to leaf; the rest are finals and go to final.
// Every leaf is answered before a final starts, so the split is exact.
type fakeChooser struct {
	leafCalls int
	leaf      func(options []semantic.Option) (string, error)
	final     func(options []semantic.Option) (string, error)
	delay     time.Duration

	mu       sync.Mutex
	calls    [][]semantic.Option
	inFlight int
	peak     int
}

func (f *fakeChooser) Choose(
	_ context.Context,
	_ string,
	options []semantic.Option,
) (string, error) {
	f.mu.Lock()
	call := len(f.calls)
	f.calls = append(f.calls, slices.Clone(options))
	f.inFlight++
	f.peak = max(f.peak, f.inFlight)
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()

	time.Sleep(f.delay)

	if call >= f.leafCalls && f.final != nil {
		return f.final(options)
	}

	if f.leaf != nil {
		return f.leaf(options)
	}

	return semantic.NoneID, nil
}

func (f *fakeChooser) recorded() [][]semantic.Option {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.calls)
}

// pickTitle answers the option titled title, or NONE when the leaf has none.
func pickTitle(title string) func([]semantic.Option) (string, error) {
	return func(options []semantic.Option) (string, error) {
		for _, option := range options {
			if option.Title == title {
				return option.ID, nil
			}
		}

		return semantic.NoneID, nil
	}
}

func candidates(count int) []semantic.Candidate {
	result := make([]semantic.Candidate, count)
	for index := range result {
		result[index] = semantic.Candidate{
			Role:  "AXButton",
			Title: fmt.Sprintf("control %d", index),
		}
	}

	return result
}

func TestSelect_SplitsCandidatesIntoBalancedLeaves(t *testing.T) {
	tests := []struct {
		count int
		want  []int
	}{
		{count: 254, want: []int{254}},
		{count: 255, want: []int{128, 127}},
		{count: 256, want: []int{128, 128}},
		{count: 510, want: []int{128, 128, 127, 127}},
		{count: 1024, want: []int{128, 128, 128, 128, 128, 128, 128, 128}},
	}

	for _, testCase := range tests {
		t.Run(strconv.Itoa(testCase.count), func(t *testing.T) {
			chooser := &fakeChooser{leafCalls: semantic.LeafCount(testCase.count)}

			_, err := semantic.Select(
				context.Background(), chooser, testQuery, candidates(testCase.count), 4,
			)
			if err != nil {
				t.Fatalf("Select() error = %v", err)
			}

			calls := chooser.recorded()

			sizes := make([]int, 0, len(calls))
			for _, call := range calls {
				sizes = append(sizes, len(call))
			}

			slices.Sort(sizes)
			slices.Reverse(sizes)

			if !slices.Equal(sizes, testCase.want) {
				t.Fatalf("leaf sizes = %v, want %v", sizes, testCase.want)
			}

			if got := semantic.LeafCount(testCase.count); got != len(testCase.want) {
				t.Fatalf("LeafCount(%d) = %d, want %d", testCase.count, got, len(testCase.want))
			}

			assertEachCandidateOnce(t, calls, testCase.count)
		})
	}
}

func TestSelect_NoneEverywhereMeansNone(t *testing.T) {
	for _, count := range []int{1, 254, 1024} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			chooser := &fakeChooser{leafCalls: semantic.LeafCount(count)}

			selection, err := semantic.Select(
				context.Background(), chooser, testQuery, candidates(count), 4,
			)
			if err != nil {
				t.Fatalf("Select() error = %v", err)
			}

			if selection.Matched {
				t.Fatalf("Select() matched %d, want no match", selection.Index)
			}

			if got := len(chooser.recorded()); got != semantic.LeafCount(count) {
				t.Fatalf(
					"questions asked = %d, want only the %d leaves",
					got,
					semantic.LeafCount(count),
				)
			}
		})
	}
}

func TestSelect_SingleWinnerNeedsNoFinal(t *testing.T) {
	tests := []struct {
		name   string
		count  int
		target int
	}{
		{name: "one leaf", count: 100, target: 42},
		{name: "one winning leaf of eight", count: 1024, target: 700},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			title := fmt.Sprintf("control %d", testCase.target)
			chooser := &fakeChooser{
				leafCalls: semantic.LeafCount(testCase.count),
				leaf:      pickTitle(title),
			}

			selection, err := semantic.Select(
				context.Background(), chooser, testQuery, candidates(testCase.count), 4,
			)
			if err != nil {
				t.Fatalf("Select() error = %v", err)
			}

			if !selection.Matched || selection.Index != testCase.target {
				t.Fatalf("Select() = %+v, want index %d", selection, testCase.target)
			}

			if got := len(chooser.recorded()); got != semantic.LeafCount(testCase.count) {
				t.Fatalf(
					"questions asked = %d, want %d with no final",
					got,
					semantic.LeafCount(testCase.count),
				)
			}
		})
	}
}

func TestSelect_FinalsReceiveTheWinnersMetadataAndNothingElse(t *testing.T) {
	const count = 1024

	winners := []int{5, 300, 900}

	pool := candidates(count)
	for _, index := range winners {
		pool[index] = semantic.Candidate{
			Role:        "AXButton",
			Subrole:     "AXToolbarButton",
			Title:       "Save",
			Description: fmt.Sprintf("save variant %d", index),
			ParentRole:  "AXToolbar",
			ParentTitle: fmt.Sprintf("toolbar %d", index),
			AppName:     "Editor",
			HasPopup:    index == 300,
		}
	}

	// The final must see each leaf winner exactly as Select first offered it:
	// the same ID and the same metadata, in leaf order. Option has no field
	// for anything a leaf answered, so equality here also means no confidence
	// or probability from one leaf travels into the final.
	wantFinal := make([]semantic.Option, 0, len(winners))
	for _, index := range winners {
		wantFinal = append(wantFinal, semantic.Option{
			ID:        fmt.Sprintf("c%04d", index),
			Candidate: pool[index],
		})
	}

	tests := []struct {
		name      string
		final     func([]semantic.Option) (string, error)
		wantMatch bool
		wantIndex int
	}{
		{
			name: "final picks one winner",
			final: func(options []semantic.Option) (string, error) {
				return options[1].ID, nil
			},
			wantMatch: true,
			wantIndex: 300,
		},
		{
			name: "final finds the winners equally plausible",
			final: func([]semantic.Option) (string, error) {
				return semantic.NoneID, nil
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			leaves := semantic.LeafCount(count)
			chooser := &fakeChooser{
				leafCalls: leaves,
				leaf:      pickTitle("Save"),
				final:     testCase.final,
			}

			selection, err := semantic.Select(context.Background(), chooser, testQuery, pool, 4)
			if err != nil {
				t.Fatalf("Select() error = %v", err)
			}

			if selection.Matched != testCase.wantMatch ||
				(testCase.wantMatch && selection.Index != testCase.wantIndex) {
				t.Fatalf("Select() = %+v, want matched %v index %d",
					selection, testCase.wantMatch, testCase.wantIndex)
			}

			calls := chooser.recorded()
			if len(calls) != leaves+1 {
				t.Fatalf("questions asked = %d, want %d leaves and one final", len(calls), leaves)
			}

			assertEachCandidateOnce(t, calls[:leaves], count)

			if got := calls[leaves]; !reflect.DeepEqual(got, wantFinal) {
				t.Fatalf("final options = %+v, want %+v", got, wantFinal)
			}
		})
	}
}

func TestSelect_RejectsAnAnswerOutsideTheLeaf(t *testing.T) {
	tests := []struct {
		name   string
		answer string
	}{
		{name: "unknown id", answer: "c9999"},
		{name: "id from another leaf", answer: "c0200"},
		{name: "empty answer", answer: ""},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			chooser := &fakeChooser{
				leafCalls: 2,
				leaf: func(options []semantic.Option) (string, error) {
					if options[0].ID == "c0000" {
						return testCase.answer, nil
					}

					return semantic.NoneID, nil
				},
			}

			selection, err := semantic.Select(
				context.Background(), chooser, testQuery, candidates(300), 4,
			)
			if !derrors.IsCode(err, derrors.CodeInternal) {
				t.Fatalf("Select() error = %v, want code %s", err, derrors.CodeInternal)
			}

			if selection.Matched {
				t.Fatalf("Select() matched %d despite the error", selection.Index)
			}
		})
	}
}

func TestSelect_FailsWhenAnyLeafFails(t *testing.T) {
	leafErr := derrors.New(derrors.CodeRateLimited, "rate limited")

	chooser := &fakeChooser{
		leafCalls: semantic.LeafCount(1024),
		leaf: func(options []semantic.Option) (string, error) {
			for _, option := range options {
				if option.ID == "c0700" {
					return "", leafErr
				}
			}

			return options[0].ID, nil
		},
	}

	selection, err := semantic.Select(context.Background(), chooser, testQuery, candidates(1024), 4)
	if !errors.Is(err, leafErr) {
		t.Fatalf("Select() error = %v, want the failing leaf's error", err)
	}

	if selection.Matched {
		t.Fatalf("Select() matched %d despite a failed leaf", selection.Index)
	}
}

func TestSelect_BoundsQuestionsInFlight(t *testing.T) {
	tests := []struct {
		name        string
		maxInFlight int
		wantPeak    int
	}{
		{name: "four", maxInFlight: 4, wantPeak: 4},
		{name: "zero means one", maxInFlight: 0, wantPeak: 1},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			chooser := &fakeChooser{
				leafCalls: semantic.LeafCount(2048),
				delay:     2 * time.Millisecond,
			}

			_, err := semantic.Select(
				context.Background(), chooser, testQuery, candidates(2048), testCase.maxInFlight,
			)
			if err != nil {
				t.Fatalf("Select() error = %v", err)
			}

			if chooser.peak < 1 || chooser.peak > testCase.wantPeak {
				t.Fatalf(
					"peak questions in flight = %d, want 1..%d",
					chooser.peak,
					testCase.wantPeak,
				)
			}
		})
	}
}

func TestSelect_StopsWhenTheContextEnds(t *testing.T) {
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancelExpired()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name string
		ctx  context.Context //nolint:containedctx // each case runs under its own context
		want derrors.Code
	}{
		{name: "deadline passed", ctx: expired, want: derrors.CodeTimeout},
		{name: "canceled", ctx: canceled, want: derrors.CodeContextCanceled},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			chooser := &fakeChooser{leafCalls: 1}

			_, err := semantic.Select(testCase.ctx, chooser, testQuery, candidates(10), 4)
			if !derrors.IsCode(err, testCase.want) {
				t.Fatalf("Select() error = %v, want code %s", err, testCase.want)
			}

			if got := len(chooser.recorded()); got != 0 {
				t.Fatalf("questions asked = %d after the context ended, want 0", got)
			}
		})
	}
}

func TestSelect_AsksNothingWithoutCandidates(t *testing.T) {
	chooser := &fakeChooser{}

	selection, err := semantic.Select(context.Background(), chooser, testQuery, nil, 4)
	if err != nil || selection.Matched {
		t.Fatalf("Select() = %+v, %v; want no match and no error", selection, err)
	}

	if got := len(chooser.recorded()); got != 0 {
		t.Fatalf("questions asked = %d, want 0", got)
	}
}

// assertEachCandidateOnce checks that leaves offered every candidate ID of a
// count-candidate Select exactly once between them.
func assertEachCandidateOnce(t *testing.T, leaves [][]semantic.Option, count int) {
	t.Helper()

	seen := make(map[string]int, count)

	for _, leaf := range leaves {
		for _, option := range leaf {
			seen[option.ID]++
		}
	}

	if len(seen) != count {
		t.Fatalf("leaves offered %d distinct candidates, want %d", len(seen), count)
	}

	for id, times := range seen {
		if times != 1 {
			t.Fatalf("candidate %s offered %d times at the leaves, want once", id, times)
		}
	}
}
