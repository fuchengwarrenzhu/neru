package modes

import (
	"context"
	"slices"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/y3owk1n/neru/internal/derrors"
	"github.com/y3owk1n/neru/internal/domain"
	domainHint "github.com/y3owk1n/neru/internal/domain/hint"
	"github.com/y3owk1n/neru/internal/domain/semantic"
)

// A --semantic search picks its target by what the query describes. Typing
// only edits the query; Return asks the target selector once, with the query
// and what each hint's element says about itself; a single match is selected
// through the same confirm path a typed label takes, and anything else leaves
// the search open with a short status.
//
// The selector is remote, so it is never asked under h.mu. The submit takes a
// snapshot under the lock and starts a goroutine; the answer comes back
// through the outer lock and is applied only if nothing it was asked about has
// changed in between — the requestScreenCapturePermissionAndResume pattern
// (AGENTS.md in this directory).

// What the search input says about a selection in place of its result count.
const (
	semanticStatusPending     = "…"
	semanticStatusNoMatch     = "no single match"
	semanticStatusNoKey       = "no API key"
	semanticStatusKeyExposed  = "API key readable by others"
	semanticStatusKeyRejected = "API key rejected"
	semanticStatusRateLimited = "rate limited"
	semanticStatusTimedOut    = "timed out"
	semanticStatusFailed      = "selection failed"
	semanticStatusUnavailable = "no target selector"
)

// Why an answer was dropped, for the debug log.
const (
	staleShuttingDown = "shutting_down"
	staleModeChanged  = "mode_changed"
	staleSearchClosed = "search_closed"
	staleRescanned    = "hints_rescanned"
	staleSuperseded   = "superseded"
)

// The outcome a selection logs.
const (
	semanticOutcomeMatch = "match"
	semanticOutcomeNone  = "none"
	semanticOutcomeError = "error"
	semanticOutcomeStale = "stale"
)

// semanticAnswer is what a submit snapshots under h.mu, the selector's answer,
// and what that answer is checked against when it comes back.
type semanticAnswer struct {
	// token and session are h.semanticRequest and h.hintSearchSession at the
	// submit.
	token   uint64
	session uint64
	// source is the hint collection the candidates were built from. Its
	// identity is what says the hints were not rescanned in between.
	source     *domainHint.Collection
	candidates int

	selection semantic.Selection
	err       error
	latency   time.Duration
}

// submitSemanticSearch asks the target selector which hint the query
// describes. It runs under h.mu, so it builds the candidates from the elements
// already in memory and hands the asking to a goroutine.
func (h *handlerState) submitSemanticSearch() {
	ctx := h.hints.Context

	query := strings.TrimSpace(ctx.SearchQuery())
	source := ctx.SourceHints()

	if query == "" || source == nil || source.Count() == 0 {
		return
	}

	// A new submit replaces the one in flight, whose answer would describe a
	// query the user has moved on from.
	h.cancelSemanticRequest()

	h.semanticQuery = ctx.SearchQuery()

	if h.targetSelector == nil {
		h.logger.Debug("Semantic selection skipped: no target selector")
		h.showSemanticStatus(semanticStatusUnavailable)

		return
	}

	candidates := semanticCandidates(source.All(), ctx.ScannedApp())

	requestCtx, cancel := h.semanticRequestContext()
	h.semanticCancel = cancel

	answer := semanticAnswer{
		token:      h.semanticRequest,
		session:    h.hintSearchSession,
		source:     source,
		candidates: len(candidates),
	}

	h.showSemanticStatus(semanticStatusPending)

	selector := h.targetSelector
	outer := h.outer

	// Nothing waits on this goroutine. It asks with no lock held and comes
	// back through the outer lock, where the snapshot decides whether the
	// answer still applies.
	go func(answer semanticAnswer) {
		defer cancel()

		started := time.Now()
		answer.selection, answer.err = selector.Select(requestCtx, query, candidates)
		answer.latency = time.Since(started)

		outer.settleSemanticSelection(answer)
	}(answer)
}

// semanticRequestContext bounds one selection by the configured budget, under
// the handler's context so a shutdown stops it too.
func (h *handlerState) semanticRequestContext() (context.Context, context.CancelFunc) {
	if h.targetSelectionBudget <= 0 {
		return context.WithCancel(h.ctx)
	}

	return context.WithTimeout(h.ctx, h.targetSelectionBudget)
}

// cancelSemanticRequest drops the selection in flight, if any: its answer
// comes back with a token that no longer matches, and its request stops.
func (h *handlerState) cancelSemanticRequest() {
	h.semanticRequest++

	if h.semanticCancel != nil {
		h.semanticCancel()
		h.semanticCancel = nil
	}
}

// settleSemanticSelection applies a selection's answer. It runs on the
// goroutine that asked, with nothing held, and takes the lock itself.
func (h *Handler) settleSemanticSelection(answer semanticAnswer) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.applySemanticSelection(answer)
}

// applySemanticSelection selects the matched hint, or shows why there is none.
//
// Caller must hold h.mu.
func (h *handlerState) applySemanticSelection(answer semanticAnswer) {
	fields := []zap.Field{
		zap.Int("candidates", answer.candidates),
		zap.Int64("latency_ms", answer.latency.Milliseconds()),
	}

	reason := h.staleSemanticReason(answer)
	if reason != "" {
		h.logger.Debug("Semantic selection dropped", append(fields,
			zap.String("outcome", semanticOutcomeStale),
			zap.String("reason", reason),
		)...)

		return
	}

	// The request finished, so there is nothing left to cancel.
	h.semanticCancel = nil

	if answer.err != nil {
		h.logger.Debug("Semantic selection failed", append(fields,
			zap.String("outcome", semanticOutcomeError),
			zap.String("error_code", string(derrors.GetCode(answer.err))),
		)...)
		h.showSemanticStatus(semanticErrorStatus(answer.err))

		return
	}

	if !answer.selection.Matched {
		h.logger.Debug("Semantic selection found no single match", append(fields,
			zap.String("outcome", semanticOutcomeNone),
		)...)
		h.showSemanticStatus(semanticStatusNoMatch)

		return
	}

	chosen, found := h.semanticChoice(answer)
	if !found {
		h.logger.Debug("Semantic selection named a hint no longer on screen", append(fields,
			zap.String("outcome", semanticOutcomeError),
			zap.String("error_code", string(derrors.CodeInternal)),
		)...)
		h.showSemanticStatus(semanticStatusFailed)

		return
	}

	h.logger.Debug("Semantic selection matched", append(fields,
		zap.String("outcome", semanticOutcomeMatch),
	)...)

	h.selectSemanticChoice(chosen)
}

// staleSemanticReason says why an answer no longer applies, or "" when it
// does: the mode, the search and the hints it was asked about are the ones on
// screen, and no later submit replaced it.
func (h *handlerState) staleSemanticReason(answer semanticAnswer) string {
	if h.ctx.Err() != nil {
		return staleShuttingDown
	}

	if h.appState.CurrentMode() != domain.ModeHints {
		return staleModeChanged
	}

	if h.hints == nil || h.hints.Context == nil || !h.hints.Context.SearchActive() ||
		answer.session != h.hintSearchSession {
		return staleSearchClosed
	}

	if h.hints.Context.SourceHints() != answer.source {
		return staleRescanned
	}

	if answer.token != h.semanticRequest {
		return staleSuperseded
	}

	return ""
}

// semanticChoice maps the selection back to the hint it was built from, and
// checks that hint is still one of the hints on screen.
func (h *handlerState) semanticChoice(answer semanticAnswer) (*domainHint.Interface, bool) {
	asked := answer.source.All()

	index := answer.selection.Index
	if index < 0 || index >= len(asked) {
		return nil, false
	}

	chosen := asked[index]

	current := h.hints.Context.SourceHints()
	if current == nil || !slices.Contains(current.All(), chosen) {
		return nil, false
	}

	return chosen, true
}

// selectSemanticChoice narrows the hints to the chosen one and confirms the
// search, which acts on it exactly as Return does on a single text match: the
// pending action, --repeat and --on-exit all apply.
func (h *handlerState) selectSemanticChoice(chosen *domainHint.Interface) {
	ctx := h.hints.Context

	ctx.SetSearchStatus("")
	// The chosen hint is drawn with its label, as a typed label leaves it.
	ctx.SetLabelsHidden(false)

	setErr := ctx.SetVisibleHints(domainHint.NewCollection([]*domainHint.Interface{chosen}))
	if setErr != nil {
		h.logger.Warn("Failed to narrow hints to the semantic match", zap.Error(setErr))
		ctx.SetLabelsHidden(true)
		h.showSemanticStatus(semanticStatusFailed)

		return
	}

	h.confirmHintSearch()
}

// showSemanticStatus puts a status in the search input in place of its count.
func (h *handlerState) showSemanticStatus(status string) {
	h.hints.Context.SetSearchStatus(status)
	h.drawHintSearchInput()
}

// editSemanticQuery is what typing does in a --semantic search: it redraws
// the query and filters nothing. A status that described the submitted query
// goes once the text differs from it; one still waiting stays.
func (h *handlerState) editSemanticQuery() {
	ctx := h.hints.Context

	status := ctx.SearchStatus()
	if status != "" && status != semanticStatusPending && ctx.SearchQuery() != h.semanticQuery {
		ctx.SetSearchStatus("")
	}

	h.drawHintSearchInput()
	h.cycleHintIndex = -1
}

// semanticErrorStatuses says what a failed selection shows, by the code the
// selector failed with; any code not here shows semanticStatusFailed.
var semanticErrorStatuses = map[derrors.Code]string{
	derrors.CodeCredentialMissing:  semanticStatusNoKey,
	derrors.CodeCredentialInsecure: semanticStatusKeyExposed,
	derrors.CodeUnauthorized:       semanticStatusKeyRejected,
	derrors.CodeRateLimited:        semanticStatusRateLimited,
	derrors.CodeTimeout:            semanticStatusTimedOut,
}

// semanticErrorStatus is the status a failed selection shows.
func semanticErrorStatus(err error) string {
	status, named := semanticErrorStatuses[derrors.GetCode(err)]
	if !named {
		return semanticStatusFailed
	}

	return status
}

// semanticCandidates describes each hint's element to the target selector,
// from what the scan already read. It asks the platform nothing, because it
// runs under h.mu.
//
// What it leaves out is the point: the element's value is user content, and
// the window title and the bounds say more about the user than about the
// control, so semantic.Candidate has no field for any of them.
func semanticCandidates(hints []*domainHint.Interface, app string) []semantic.Candidate {
	candidates := make([]semantic.Candidate, len(hints))

	for index, hint := range hints {
		element := hint.Element()
		candidates[index] = semantic.Candidate{
			Role:        string(element.Role()),
			Subrole:     element.Subrole(),
			Title:       element.Title(),
			Description: element.Description(),
			ParentRole:  element.ParentRole(),
			ParentTitle: element.ParentTitle(),
			AppName:     app,
			HasPopup:    element.HasPopup(),
		}
	}

	return candidates
}
