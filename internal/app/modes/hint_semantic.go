package modes

import (
	"context"
	"slices"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/y3owk1n/neru/internal/derrors"
	"github.com/y3owk1n/neru/internal/domain"
	"github.com/y3owk1n/neru/internal/domain/element"
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
	semanticStatusVisionOnly  = "not available for vision hints"
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
	source *domainHint.Collection
	// asked are the hints offered as candidates, in candidate order: the
	// selection's index points here, not into source, which also holds the
	// hints that were never offered.
	asked []*domainHint.Interface

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

	if query == "" {
		// Return on an empty field withdraws what was asked before, so an
		// answer to text that is gone cannot act.
		if h.withdrawSemanticRequest() {
			h.drawHintSearchInput()
		}

		return
	}

	if source == nil || source.Count() == 0 {
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

	asked, candidates := semanticCandidates(source.All(), ctx.ScannedApp())
	if len(candidates) == 0 {
		h.logger.Debug("Semantic selection skipped: every hint was read off the screen",
			zap.Int("hints", source.Count()),
		)
		h.showSemanticStatus(semanticStatusVisionOnly)

		return
	}

	requestCtx, cancel := h.semanticRequestContext()
	h.semanticCancel = cancel

	answer := semanticAnswer{
		token:   h.semanticRequest,
		session: h.hintSearchSession,
		source:  source,
		asked:   asked,
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

// withdrawSemanticRequest cancels the request in flight, if there is one, and
// takes its "…" off the search input. It reports whether there was one; the
// caller draws the input.
func (h *handlerState) withdrawSemanticRequest() bool {
	if h.semanticCancel == nil {
		return false
	}

	h.cancelSemanticRequest()

	if h.hints.Context.SearchStatus() == semanticStatusPending {
		h.hints.Context.SetSearchStatus("")
	}

	return true
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
		zap.Int("candidates", len(answer.asked)),
		zap.Int64("latency_ms", answer.latency.Milliseconds()),
	}

	reason := h.staleSemanticReason(answer)
	if reason != "" {
		h.logger.Debug("Semantic selection dropped", append(fields,
			zap.String("outcome", semanticOutcomeStale),
			zap.String("reason", reason),
		)...)

		h.releaseDroppedSemanticRequest(answer, reason)

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

// releaseDroppedSemanticRequest settles a dropped answer that was still the
// request in flight. A superseded answer has a newer request behind it, and a
// closed search took its status with it; any other drop — the hints rescanned
// under it, say — would otherwise leave "…" on screen with nothing coming.
func (h *handlerState) releaseDroppedSemanticRequest(answer semanticAnswer, reason string) {
	if reason == staleSuperseded || reason == staleSearchClosed ||
		answer.token != h.semanticRequest {
		return
	}

	h.semanticCancel = nil

	if h.hints == nil || h.hints.Context == nil || !h.hints.Context.SearchActive() {
		return
	}

	if h.hints.Context.SearchStatus() == semanticStatusPending {
		h.showSemanticStatus("")
	}
}

// semanticChoice maps the selection back to the hint it was built from, and
// checks that hint is still one of the hints on screen.
func (h *handlerState) semanticChoice(answer semanticAnswer) (*domainHint.Interface, bool) {
	index := answer.selection.Index
	if index < 0 || index >= len(answer.asked) {
		return nil, false
	}

	chosen := answer.asked[index]

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
// the query and filters nothing. Once the text differs from what was
// submitted, a request still in flight is withdrawn — its answer would act on
// text the field no longer holds — and a status that described it goes.
func (h *handlerState) editSemanticQuery() {
	ctx := h.hints.Context

	if ctx.SearchQuery() != h.semanticQuery {
		h.withdrawSemanticRequest()
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

// documentParentRoles are the parents whose title names the window or the
// page rather than a control: a window's title is often a document name or an
// email subject, and a web area's is the page's title. A candidate keeps such
// a parent's role and never its title. ParentRole holds the raw AX role.
var documentParentRoles = map[string]bool{
	string(element.RoleWindow):      true,
	string(element.RoleSheet):       true,
	"AXDrawer":                      true,
	string(element.RoleApplication): true,
	"AXWebArea":                     true,
}

// semanticCandidates describes the hints the target selector may be told
// about, from what the scan already read, and returns them with the hints
// they describe, in the same order. It asks the platform nothing, because it
// runs under h.mu.
//
// It is the one place that decides what leaves the machine, and what it
// leaves out is the point. semantic.Candidate has no field for an element's
// value, which is user content, or its bounds. A hint read off the screen by
// OCR is left out whole, since its title is whatever text was on screen. A
// parent that is a window or a page keeps its role and loses its title,
// since that title is the window's or the document's.
func semanticCandidates(
	hints []*domainHint.Interface,
	app string,
) ([]*domainHint.Interface, []semantic.Candidate) {
	asked := make([]*domainHint.Interface, 0, len(hints))
	candidates := make([]semantic.Candidate, 0, len(hints))

	for _, hint := range hints {
		described := hint.Element()
		if described.Source() == element.SourceVision {
			continue
		}

		parentTitle := described.ParentTitle()
		if documentParentRoles[described.ParentRole()] {
			parentTitle = ""
		}

		asked = append(asked, hint)
		candidates = append(candidates, semantic.Candidate{
			Role:        string(described.Role()),
			Subrole:     described.Subrole(),
			Title:       described.Title(),
			Description: described.Description(),
			ParentRole:  described.ParentRole(),
			ParentTitle: parentTitle,
			AppName:     app,
			HasPopup:    described.HasPopup(),
		})
	}

	return asked, candidates
}
