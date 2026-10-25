package accessibility

import (
	"github.com/y3owk1n/neru/internal/adapter/accessibility/ax"
	"github.com/y3owk1n/neru/internal/derrors"
	"github.com/y3owk1n/neru/internal/domain/element"
)

// convertToDomainElement converts an ax.Node to a domain Element.
func (a *Adapter) convertToDomainElement(node ax.Node) (*element.Element, error) {
	if node == nil {
		return nil, derrors.New(derrors.CodeInvalidInput, "node is nil")
	}

	elementID := element.ID(node.ID())
	bounds := node.Bounds()
	role := element.Role(node.Role())
	isClickable := node.IsClickable()

	searchText := ""
	if provider, ok := node.(interface{ SearchText() string }); ok {
		searchText = provider.SearchText()
	}

	// The context was read during the walk, so this asks the platform nothing.
	var (
		subrole, parentRole, parentTitle string
		hasPopup                         bool
	)

	if described, ok := node.(describedNode); ok {
		subrole = described.Subrole()
		parentRole = described.ParentRole()
		parentTitle = described.ParentTitle()
		hasPopup = described.HasPopup()
	}

	// Create element with options
	domElement, elementErr := element.NewElement(
		elementID,
		bounds,
		role,
		element.WithClickable(isClickable),
		element.WithTitle(node.Title()),
		element.WithDescription(node.Description()),
		element.WithValue(node.Value()),
		element.WithSearchText(searchText),
		element.WithFocused(node.IsFocused()),
		element.WithIdentifier(node.Identifier()),
		element.WithSubrole(subrole),
		element.WithContext(parentRole, parentTitle),
		element.WithHasPopup(hasPopup),
	)
	if elementErr != nil {
		return nil, derrors.Wrap(
			elementErr,
			derrors.CodeAccessibilityFailed,
			"failed to create element",
		)
	}

	return domElement, nil
}

// describedNode is the part of a node that says where it sits and what it
// opens, which the macOS walk answers and the other platforms' nodes do not.
// It is what lets a target selector tell a toolbar's "Code" menu button from a
// heading with the same title.
type describedNode interface {
	Subrole() string
	ParentRole() string
	ParentTitle() string
	HasPopup() bool
}
