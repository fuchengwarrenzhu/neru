package semantic

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/y3owk1n/neru/internal/derrors"
)

// MaxLeafOptions is the most candidates one question carries. A choice
// question takes at most 255 options, and NONE is always one of them.
const MaxLeafOptions = 254

// NoneID is the option a Chooser answers when no single candidate fulfills
// the query: the target is absent, or several are equally plausible.
const NoneID = "NONE"

// Candidate is what a selector may know about one control. It deliberately
// has no field for the element's value, its bounds, or the window title: those
// are user content or layout, and never leave the machine.
type Candidate struct {
	Role        string
	Subrole     string
	Title       string
	Description string
	ParentRole  string
	ParentTitle string
	AppName     string
	HasPopup    bool
}

// Option is a candidate as a question presents it: an ID that is unique
// within one Select, and the candidate's metadata.
type Option struct {
	Candidate

	ID string
}

// Chooser asks one question about at most MaxLeafOptions options.
type Chooser interface {
	// Choose returns the ID of the one option that fulfills query, or NoneID.
	// Any other answer fails the selection.
	Choose(ctx context.Context, query string, options []Option) (string, error)
}

// Selection is the outcome of Select.
type Selection struct {
	// Index is the chosen candidate's position in the slice given to Select.
	// It means nothing unless Matched is true.
	Index int
	// Matched is false when no single candidate fulfills the query.
	Matched bool
}

// Select asks chooser about every candidate and returns the one it picks.
//
// No more than maxInFlight questions are asked at once (at least one). One
// leaf's error ends the whole selection, since the target may be a candidate
// that leaf held; the remaining questions are canceled through ctx.
func Select(
	ctx context.Context,
	chooser Chooser,
	query string,
	candidates []Candidate,
	maxInFlight int,
) (Selection, error) {
	if len(candidates) == 0 {
		return Selection{}, nil
	}

	options := make([]Option, len(candidates))
	positions := make(map[string]int, len(candidates))

	for index, candidate := range candidates {
		id := fmt.Sprintf("c%04d", index)
		options[index] = Option{ID: id, Candidate: candidate}
		positions[id] = index
	}

	winner, found, err := runRound(ctx, chooser, query, options, max(maxInFlight, 1))
	if err != nil || !found {
		return Selection{}, err
	}

	return Selection{Index: positions[winner.ID], Matched: true}, nil
}

// LeafCount reports how many leaf questions Select asks about count candidates
// before any final.
func LeafCount(count int) int {
	if count <= 0 {
		return 0
	}

	if count <= MaxLeafOptions {
		return 1
	}

	half := upperHalf(count)

	return LeafCount(half) + LeafCount(count-half)
}

// runRound asks every leaf of options once and returns the one winner, running
// a final over the leaf winners when there is more than one. The final is the
// same tournament over fewer options, so it terminates: several leaves each
// hold at least two options and return at most one.
func runRound(
	ctx context.Context,
	chooser Chooser,
	query string,
	options []Option,
	maxInFlight int,
) (Option, bool, error) {
	leaves := splitLeaves(options)

	answers, err := askLeaves(ctx, chooser, query, leaves, maxInFlight)
	if err != nil {
		return Option{}, false, err
	}

	var winners []Option

	for index, leaf := range leaves {
		if answers[index] == NoneID {
			continue
		}

		winner, ok := optionByID(leaf, answers[index])
		if !ok {
			return Option{}, false, derrors.New(
				derrors.CodeInternal,
				"target chooser answered an option it was not offered",
			)
		}

		winners = append(winners, winner)
	}

	switch len(winners) {
	case 0:
		return Option{}, false, nil
	case 1:
		return winners[0], true, nil
	default:
		return runRound(ctx, chooser, query, winners, maxInFlight)
	}
}

// splitLeaves halves options until every part fits one question, so leaf
// sizes never differ by more than one.
func splitLeaves(options []Option) [][]Option {
	if len(options) <= MaxLeafOptions {
		return [][]Option{options}
	}

	half := upperHalf(len(options))

	return append(splitLeaves(options[:half]), splitLeaves(options[half:])...)
}

// upperHalf is the size of the first part when count is split in two.
func upperHalf(count int) int {
	return (count + 1) / 2 //nolint:mnd // a binary split
}

// askLeaves asks every leaf its question, at most maxInFlight at a time, and
// returns the answers in leaf order. The first failure cancels the rest and is
// the error returned.
func askLeaves(
	ctx context.Context,
	chooser Chooser,
	query string,
	leaves [][]Option,
	maxInFlight int,
) ([]string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	answers := make([]string, len(leaves))
	slots := make(chan struct{}, maxInFlight)

	var (
		group    sync.WaitGroup
		failMu   sync.Mutex
		firstErr error
	)

	fail := func(err error) {
		failMu.Lock()
		defer failMu.Unlock()

		if firstErr == nil {
			firstErr = err

			cancel()
		}
	}

	for index, leaf := range leaves {
		group.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()

			ctxErr := ctx.Err()
			if ctxErr != nil {
				fail(contextError(ctxErr))

				return
			}

			answer, err := chooser.Choose(ctx, query, leaf)
			if err != nil {
				fail(err)

				return
			}

			answers[index] = answer
		})
	}

	group.Wait()

	if firstErr != nil {
		return nil, firstErr
	}

	return answers, nil
}

func optionByID(options []Option, id string) (Option, bool) {
	for _, option := range options {
		if option.ID == id {
			return option, true
		}
	}

	return Option{}, false
}

func contextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return derrors.Wrap(err, derrors.CodeTimeout, "target selection timed out")
	}

	return derrors.Wrap(err, derrors.CodeContextCanceled, "target selection canceled")
}
