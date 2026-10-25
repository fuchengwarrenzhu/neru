package ports

import (
	"context"

	"github.com/y3owk1n/neru/internal/domain/semantic"
)

// TargetSelector picks the one candidate control a natural-language query
// names. Its implementation sends the query and the candidates' metadata to a
// remote model, so a caller asks only when the user submits a query, never per
// keystroke.
type TargetSelector interface {
	// Select returns the candidate query names, or a Selection with Matched
	// false when no single candidate fulfills it. Index refers to candidates.
	//
	// ctx bounds the whole selection; the caller sets the budget.
	// Implementations return CodeCredentialMissing or CodeCredentialInsecure
	// when the credential cannot be used, CodeUnauthorized when the service
	// rejects it, CodeRateLimited, CodeTimeout, CodeContextCanceled, and
	// CodeRemoteFailed for any other failure or an answer outside the
	// service's contract.
	Select(
		ctx context.Context,
		query string,
		candidates []semantic.Candidate,
	) (semantic.Selection, error)
}
