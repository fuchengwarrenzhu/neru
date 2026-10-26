package modes

import (
	"context"
	"errors"
	"image"
	"slices"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/y3owk1n/neru/internal/app/services"
	"github.com/y3owk1n/neru/internal/derrors"
	"github.com/y3owk1n/neru/internal/domain"
	"github.com/y3owk1n/neru/internal/domain/element"
	domainHint "github.com/y3owk1n/neru/internal/domain/hint"
	"github.com/y3owk1n/neru/internal/domain/semantic"
	"github.com/y3owk1n/neru/internal/ports"
)

// A --semantic search picks its target by what the query describes. Typing
// only edits the query. Return scans the session's application again, asks
// the target selector once, with the query and what each freshly scanned
// element says about itself, and then reads the chosen element again where it
// is now. A single match that is still there is selected through the same
// confirm path a typed label takes, at the place it was read; anything else
// leaves the search open with a short status.
//
// The scan is fresh because the hints on screen can describe a page that has
// since changed — a dialog that opened after they were read. It is a fresher
// read, not a promise: the page can still change after it. The elements it
// leases are the only ones offered, because only a leased element can be read
// again before the click (ports.ElementLease).
//
// None of the three — the scan, the selector, the second read — runs under
// h.mu. The submit takes a snapshot under the lock and starts one goroutine,
// which comes back through the outer lock after the scan and again after the
// answer, and each return applies only if nothing it depends on has changed in
// between — the requestScreenCapturePermissionAndResume pattern (AGENTS.md in
// this directory). The goroutine owns the scan's lease alone, and releases it
// last.

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
	semanticStatusTargetsOnly = "not available for supplied targets"
	semanticStatusScanFailed  = "scan failed"
	semanticStatusNothing     = "nothing to select"
	semanticStatusMoved       = "target changed"
	semanticStatusFocusMoved  = "focus moved"
)

// One Return is one request, bounded as a whole by the target selection
// budget, and each step gets a deadline within what is left of it: the scan at
// most semanticScanShare of the budget, so a slow scan leaves the selector
// time to answer, and the second read at most semanticResolveTimeout. With no
// budget the scan keeps the HintTimeout every hint walk gets.
const (
	semanticScanShare      = 2
	semanticResolveTimeout = time.Second
)

// Why an answer was dropped, for the debug log.
const (
	staleShuttingDown = "shutting_down"
	staleModeChanged  = "mode_changed"
	staleSearchClosed = "search_closed"
	staleFocusMoved   = "focus_moved"
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
	// focus is the focused-app publication when the hints were read
	// (h.hintsFocus). Any later one means another process took the focus, so
	// the answer no longer applies. The search field's own activation never
	// publishes: the watcher skips Neru and a return to the process it last
	// announced (appwatcher_darwin.m).
	focus *string
	// source is the hint collection the candidates were built from: the one
	// on screen at the submit, and the scan's once it is adopted. Its identity
	// is what says the hints were not rescanned in between.
	source *domainHint.Collection
	// asked are the hints offered as candidates, in candidate order: the
	// selection's index points here, not into source, which also holds the
	// hints that were never offered.
	asked []*domainHint.Interface

	selection semantic.Selection
	err       error
	latency   time.Duration

	// live is where the chosen element was read again after the answer, and
	// liveErr why it could not be.
	live    image.Rectangle
	liveErr error
}

// semanticScan is the scan a submit asks for, snapshotted under h.mu, and what
// it found.
type semanticScan struct {
	// app is the application the session was activated over. The scan reads
	// its windows by name, since while the search field is open the focused
	// application is Neru.
	app                string
	filterRoles        []string
	filterTextContains []string
	strategy           string
	labelDirection     string
	screen             image.Rectangle

	hints []*domainHint.Interface
	// offered are the hints whose element the lease holds, in scan order.
	// Only those are asked about: an element that cannot be read again before
	// the click cannot be confirmed, so it is never a choice.
	offered  []*domainHint.Interface
	lease    ports.ElementLease
	err      error
	duration time.Duration
}

// run scans with nothing held, within ctx and bound, and keeps the hints whose
// center is on the session's screen.
func (scan *semanticScan) run(
	ctx context.Context,
	bound time.Duration,
	service *services.HintService,
) {
	scanCtx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()

	started := time.Now()
	hints, lease, err := service.LeaseHints(scanCtx, scan.app, scan.filterRoles,
		scan.filterTextContains, scan.strategy, scan.labelDirection)
	scan.duration = time.Since(started)

	if err != nil {
		scan.err = err

		// The service reports a walk the deadline cut short as an
		// accessibility failure; the deadline is what the user should hear.
		if errors.Is(scanCtx.Err(), context.DeadlineExceeded) {
			scan.err = derrors.Wrap(err, derrors.CodeTimeout, "semantic scan timed out")
		}

		return
	}

	scan.hints = filterHintsForScreen(hints, scan.screen)
	scan.lease = lease

	if lease == nil {
		return
	}

	scan.offered = make([]*domainHint.Interface, 0, len(scan.hints))

	for _, hint := range scan.hints {
		if lease.Holds(hint.Element()) {
			scan.offered = append(scan.offered, hint)
		}
	}
}

// submitSemanticSearch asks the target selector which hint the query
// describes. It runs under h.mu, so it snapshots what the scan needs and hands
// the scanning and the asking to a goroutine.
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

	if status := h.semanticScanRefusal(); status != "" {
		h.logger.Debug("Semantic selection skipped: the session cannot be scanned for one",
			zap.Int("hints", source.Count()),
		)
		h.showSemanticStatus(status)

		return
	}

	requestCtx, cancel := h.semanticRequestContext()
	h.semanticCancel = cancel

	answer := semanticAnswer{
		token:   h.semanticRequest,
		session: h.hintSearchSession,
		focus:   h.hintsFocus,
		source:  source,
	}

	scan := h.semanticScanRequest()

	h.showSemanticStatus(semanticStatusPending)

	service := h.hintService
	selector := h.targetSelector
	scanBound := h.semanticScanBound()
	outer := h.outer

	// Nothing waits on this goroutine. It scans and asks with no lock held,
	// and comes back through the outer lock after each, where the snapshot
	// decides whether what it has still applies.
	go func() {
		defer cancel()

		scan.run(requestCtx, scanBound, service)

		if scan.lease != nil {
			// The lease is this goroutine's alone, so nothing can release a
			// reference while the chosen element is read again below.
			defer scan.lease.Release()
		}

		candidates, ask := outer.settleSemanticScan(&answer, &scan)
		if !ask {
			return
		}

		started := time.Now()
		answer.selection, answer.err = selector.Select(requestCtx, query, candidates)
		answer.latency = time.Since(started)

		if chosen, named := answer.named(); named && answer.err == nil {
			answer.live, answer.liveErr = resolveWithin(requestCtx, scan.lease, chosen)
		}

		outer.settleSemanticSelection(answer)
	}()
}

// resolveWithin reads the chosen element again within semanticResolveTimeout
// and what is left of the request. A read a deadline cut short says so, so it
// is reported as slow rather than as a target that changed.
func resolveWithin(
	ctx context.Context,
	lease ports.ElementLease,
	chosen *domainHint.Interface,
) (image.Rectangle, error) {
	resolveCtx, cancel := context.WithTimeout(ctx, semanticResolveTimeout)
	defer cancel()

	live, err := lease.Resolve(resolveCtx, chosen.Element())
	if err != nil && errors.Is(resolveCtx.Err(), context.DeadlineExceeded) {
		err = derrors.Wrap(err, derrors.CodeTimeout, "reading the chosen element again timed out")
	}

	return live, err
}

// semanticScanRefusal says why Return cannot scan the session for a
// selection, as the status to show, or "" when it can.
func (h *handlerState) semanticScanRefusal() string {
	ctx := h.hints.Context

	switch {
	case ctx.TargetsSupplied():
		// Supplied targets were never read from an application, so nothing
		// can confirm one is still there.
		return semanticStatusTargetsOnly
	case readsScreen(ctx.ActiveStrategy()):
		// Whatever the session read off the screen has no element to read
		// again.
		return semanticStatusVisionOnly
	case h.hintService == nil || ctx.ScannedApp() == "":
		// The application the session was activated over is the one to scan;
		// with none recorded, any other would be a guess.
		return semanticStatusScanFailed
	case h.focusedApp.publication() != h.hintsFocus:
		// Another application took the focus after the hints were read, so the
		// one they were read over may now be covered.
		return semanticStatusFocusMoved
	}

	asked, _ := semanticCandidates(ctx.SourceHints().All(), ctx.ScannedApp())
	if len(asked) == 0 {
		return semanticStatusVisionOnly
	}

	return ""
}

// semanticScanRequest is what the scan needs from the session, copied so the
// goroutine reads nothing the handler owns.
func (h *handlerState) semanticScanRequest() semanticScan {
	ctx := h.hints.Context

	return semanticScan{
		app:                ctx.ScannedApp(),
		filterRoles:        slices.Clone(ctx.FilterRoles()),
		filterTextContains: slices.Clone(ctx.FilterTextContains()),
		strategy:           ctx.StrategyOverride(),
		labelDirection:     ctx.LabelDirectionOverride(),
		screen:             h.screenBounds,
	}
}

// named is the hint the selector chose, when it chose one of those asked.
func (answer *semanticAnswer) named() (*domainHint.Interface, bool) {
	index := answer.selection.Index
	if !answer.selection.Matched || index < 0 || index >= len(answer.asked) {
		return nil, false
	}

	return answer.asked[index], true
}

// settleSemanticScan puts a submit's scan on screen and returns the candidates
// to ask about. It runs on the goroutine that scanned, with nothing held, and
// takes the lock itself. It returns false when there is nothing to ask.
func (h *Handler) settleSemanticScan(
	answer *semanticAnswer,
	scan *semanticScan,
) ([]semantic.Candidate, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.applySemanticScan(answer, scan)
}

// applySemanticScan replaces the hints the submit was made over with the scan,
// and records in answer what is about to be asked. A request that went stale
// while it scanned is dropped, and a scan that failed or found nothing that
// can be confirmed leaves the hints and the search as they were, with a
// status: nothing is asked on the strength of the old hints.
//
// Caller must hold h.mu.
func (h *handlerState) applySemanticScan(
	answer *semanticAnswer,
	scan *semanticScan,
) ([]semantic.Candidate, bool) {
	fields := []zap.Field{
		zap.Int("hints", len(scan.hints)),
		zap.Int64("scan_ms", scan.duration.Milliseconds()),
	}

	reason := h.staleSemanticReason(*answer)
	if reason != "" {
		h.logger.Debug("Semantic scan dropped", append(fields,
			zap.String("outcome", semanticOutcomeStale),
			zap.String("reason", reason),
		)...)

		h.releaseDroppedSemanticRequest(*answer, reason)

		return nil, false
	}

	if scan.err != nil {
		h.semanticCancel = nil

		h.logger.Debug("Semantic scan failed", append(fields,
			zap.String("outcome", semanticOutcomeError),
			zap.String("error_code", string(derrors.GetCode(scan.err))),
		)...)
		h.showSemanticStatus(semanticScanErrorStatus(scan.err))

		return nil, false
	}

	asked, candidates := semanticCandidates(scan.offered, scan.app)
	if len(candidates) == 0 {
		h.semanticCancel = nil

		h.logger.Debug("Semantic scan found nothing to select", append(fields,
			zap.String("outcome", semanticOutcomeNone),
		)...)
		h.showSemanticStatus(semanticStatusNothing)

		return nil, false
	}

	ctx := h.hints.Context

	setErr := ctx.SetHints(domainHint.NewCollection(scan.hints))
	if setErr != nil {
		h.semanticCancel = nil

		h.logger.Warn("Failed to put the semantic scan on screen", zap.Error(setErr))
		h.showSemanticStatus(semanticStatusFailed)

		return nil, false
	}

	h.cycleHintIndex = -1

	answer.source = ctx.SourceHints()
	answer.asked = asked

	// The hints were drawn again, and on a backend that paints the search
	// input with them the input went with them.
	h.drawHintSearchInput()

	h.logger.Debug("Semantic scan adopted", append(fields,
		zap.Int("candidates", len(candidates)),
	)...)

	return candidates, true
}

// semanticScanErrorStatus is the status a failed scan shows.
func semanticScanErrorStatus(err error) string {
	if derrors.GetCode(err) == derrors.CodeTimeout {
		return semanticStatusTimedOut
	}

	return semanticStatusScanFailed
}

// semanticRequestContext bounds one request by the configured budget, under
// the handler's context so a shutdown stops it too.
func (h *handlerState) semanticRequestContext() (context.Context, context.CancelFunc) {
	if h.targetSelectionBudget <= 0 {
		return context.WithCancel(h.ctx)
	}

	return context.WithTimeout(h.ctx, h.targetSelectionBudget)
}

// semanticScanBound is the most of the request the scan may take.
func (h *handlerState) semanticScanBound() time.Duration {
	if h.targetSelectionBudget <= 0 {
		return HintTimeout
	}

	return min(HintTimeout, h.targetSelectionBudget/semanticScanShare)
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

	// The element was read again after the answer. One that is gone, hidden,
	// covered within its own application or off the session's screen is not
	// clicked where it used to be.
	live, liveErr := chosen, answer.liveErr
	if liveErr == nil {
		live, liveErr = chosen.WithBounds(answer.live)
	}

	if liveErr != nil || !live.Element().Center().In(h.screenBounds) {
		code, status := derrors.CodeElementNotFound, semanticStatusMoved
		if liveErr != nil {
			code = derrors.GetCode(liveErr)
		}

		if code == derrors.CodeTimeout {
			status = semanticStatusTimedOut
		}

		h.logger.Debug("Semantic selection named a target that changed", append(fields,
			zap.String("outcome", semanticOutcomeError),
			zap.String("error_code", string(code)),
		)...)
		h.showSemanticStatus(status)

		return
	}

	h.logger.Debug("Semantic selection matched", append(fields,
		zap.String("outcome", semanticOutcomeMatch),
	)...)

	h.selectSemanticChoice(live)
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

	if h.focusedApp.publication() != answer.focus {
		return staleFocusMoved
	}

	// A newer submit is named before the rescan it made, since it is why
	// the hints changed.
	if answer.token != h.semanticRequest {
		return staleSuperseded
	}

	if h.hints.Context.SourceHints() != answer.source {
		return staleRescanned
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

	if reason == staleFocusMoved {
		h.showSemanticStatus(semanticStatusFocusMoved)

		return
	}

	if h.hints.Context.SearchStatus() == semanticStatusPending {
		h.showSemanticStatus("")
	}
}

// semanticChoice maps the selection back to the hint it was built from, and
// checks that hint is still one of the hints on screen.
func (h *handlerState) semanticChoice(answer semanticAnswer) (*domainHint.Interface, bool) {
	chosen, named := answer.named()
	if !named {
		return nil, false
	}

	current := h.hints.Context.SourceHints()
	if current == nil || !slices.Contains(current.All(), chosen) {
		return nil, false
	}

	return chosen, true
}

// selectSemanticChoice narrows the hints to the chosen one, placed where it
// was read again, and confirms the search, which acts on it exactly as Return
// does on a single text match: the pending action, --repeat and --on-exit all
// apply.
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
