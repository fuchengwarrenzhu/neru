package mocks

import (
	"context"
	"slices"
	"sync"

	"github.com/y3owk1n/neru/internal/domain/semantic"
	"github.com/y3owk1n/neru/internal/ports"
)

// MockTargetSelector is a mock implementation of ports.TargetSelector. By
// default it matches nothing.
type MockTargetSelector struct {
	SelectFunc func(context.Context, string, []semantic.Candidate) (semantic.Selection, error)

	mu         sync.Mutex
	queries    []string
	candidates [][]semantic.Candidate
}

// Select implements ports.TargetSelector.
func (m *MockTargetSelector) Select(
	ctx context.Context,
	query string,
	candidates []semantic.Candidate,
) (semantic.Selection, error) {
	m.mu.Lock()
	m.queries = append(m.queries, query)
	m.candidates = append(m.candidates, slices.Clone(candidates))
	m.mu.Unlock()

	if m.SelectFunc != nil {
		return m.SelectFunc(ctx, query, candidates)
	}

	return semantic.Selection{}, nil
}

// Queries returns the queries passed to Select, in order.
func (m *MockTargetSelector) Queries() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return slices.Clone(m.queries)
}

// Candidates returns the candidates passed to each Select call, in order.
func (m *MockTargetSelector) Candidates() [][]semantic.Candidate {
	m.mu.Lock()
	defer m.mu.Unlock()

	return slices.Clone(m.candidates)
}

// Ensure MockTargetSelector implements ports.TargetSelector.
var _ ports.TargetSelector = (*MockTargetSelector)(nil)
