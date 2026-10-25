package accessibility

import (
	"image"
	"testing"

	"go.uber.org/zap"

	"github.com/y3owk1n/neru/internal/adapter/accessibility/ax"
)

// contextNode is a node that also answers the context a platform tree walk
// knows about it: its subrole, its parent and whether it opens a popup.
type contextNode struct {
	MockNode

	subrole     string
	parentRole  string
	parentTitle string
	hasPopup    bool
}

func (n *contextNode) Subrole() string     { return n.subrole }
func (n *contextNode) ParentRole() string  { return n.parentRole }
func (n *contextNode) ParentTitle() string { return n.parentTitle }
func (n *contextNode) HasPopup() bool      { return n.hasPopup }

func newContextNode() *contextNode {
	return &contextNode{
		MockNode: MockNode{
			MockID:          "elem_1",
			MockBounds:      image.Rect(10, 20, 110, 44),
			MockRole:        "AXButton",
			MockTitle:       "Code",
			MockDescription: "Open the code menu",
			MockValue:       "typed by the user",
			MockClickable:   true,
		},
		subrole:     "AXToolbarButton",
		parentRole:  "AXToolbar",
		parentTitle: "Repository",
		hasPopup:    true,
	}
}

// BenchmarkAdapter_ConvertToDomainElement measures the per-element cost of
// turning a walked node into a domain element, which every hints activation
// pays once per clickable element.
func BenchmarkAdapter_ConvertToDomainElement(b *testing.B) {
	adapter := NewAdapter(zap.NewNop(), &MockAXClient{}, false)
	node := newContextNode()

	b.ReportAllocs()

	for b.Loop() {
		_, err := adapter.convertToDomainElement(node)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func TestAdapter_ConvertToDomainElement_CarriesWhereTheNodeSits(t *testing.T) {
	t.Parallel()

	adapter := NewAdapter(zap.NewNop(), &MockAXClient{}, false)

	tests := []struct {
		name            string
		node            ax.Node
		wantSubrole     string
		wantParentRole  string
		wantParentTitle string
		wantPopup       bool
	}{
		{
			name:            "a node the macOS walk described",
			node:            newContextNode(),
			wantSubrole:     "AXToolbarButton",
			wantParentRole:  "AXToolbar",
			wantParentTitle: "Repository",
			wantPopup:       true,
		},
		{
			// Nodes from a walk that reads no context convert as before, with
			// none.
			name: "a node with no context",
			node: &newContextNode().MockNode,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			converted, err := adapter.convertToDomainElement(testCase.node)
			if err != nil {
				t.Fatalf("convertToDomainElement() error = %v", err)
			}

			if got := converted.Subrole(); got != testCase.wantSubrole {
				t.Errorf("Subrole() = %q, want %q", got, testCase.wantSubrole)
			}

			if got := converted.ParentRole(); got != testCase.wantParentRole {
				t.Errorf("ParentRole() = %q, want %q", got, testCase.wantParentRole)
			}

			if got := converted.ParentTitle(); got != testCase.wantParentTitle {
				t.Errorf("ParentTitle() = %q, want %q", got, testCase.wantParentTitle)
			}

			if got := converted.HasPopup(); got != testCase.wantPopup {
				t.Errorf("HasPopup() = %v, want %v", got, testCase.wantPopup)
			}

			// The value is user content and travels on the element only for
			// local search; the context sits beside it rather than replacing it.
			if got := converted.Value(); got != "typed by the user" {
				t.Errorf("Value() = %q, want the node's value kept", got)
			}
		})
	}
}
