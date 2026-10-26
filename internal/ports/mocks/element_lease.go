package mocks

import (
	"context"
	"image"
	"sync"

	"github.com/y3owk1n/neru/internal/derrors"
	"github.com/y3owk1n/neru/internal/domain/element"
	"github.com/y3owk1n/neru/internal/ports"
)

// MockElementLease is a mock implementation of ports.ElementLease. By default
// it holds every element and resolves each to the bounds it was scanned with.
type MockElementLease struct {
	HoldsFunc   func(*element.Element) bool
	ResolveFunc func(context.Context, *element.Element) (image.Rectangle, error)

	mu       sync.Mutex
	released int
	resolved []*element.Element
}

// Holds implements ports.ElementLease.
func (m *MockElementLease) Holds(elem *element.Element) bool {
	if m.HoldsFunc != nil {
		return m.HoldsFunc(elem)
	}

	return true
}

// Resolve implements ports.ElementLease. A released lease resolves nothing.
func (m *MockElementLease) Resolve(
	ctx context.Context,
	elem *element.Element,
) (image.Rectangle, error) {
	m.mu.Lock()
	m.resolved = append(m.resolved, elem)
	released := m.released > 0
	m.mu.Unlock()

	if released {
		return image.Rectangle{}, derrors.New(derrors.CodeElementNotFound, "lease released")
	}

	if m.ResolveFunc != nil {
		return m.ResolveFunc(ctx, elem)
	}

	return elem.Bounds(), nil
}

// Release implements ports.ElementLease.
func (m *MockElementLease) Release() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.released++
}

// Releases reports how many times Release was called.
func (m *MockElementLease) Releases() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.released
}

// Resolved returns the elements Resolve was asked about, in order.
func (m *MockElementLease) Resolved() []*element.Element {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]*element.Element(nil), m.resolved...)
}

var _ ports.ElementLease = (*MockElementLease)(nil)
