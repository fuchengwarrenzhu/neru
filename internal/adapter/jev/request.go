package jev

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/y3owk1n/neru/internal/domain/semantic"
)

// request is one System One call: one choice question over one leaf.
type request struct {
	Model     string              `json:"model"`
	State     state               `json:"state"`
	Questions map[string]question `json:"questions"`
}

type state struct {
	UserQuery  string      `json:"user_query"` //nolint:tagliatelle // Instructions name user_query.
	Candidates []candidate `json:"candidates"`
}

// candidate is everything sent about one control. Adding a field here sends
// it off the machine; see the package comment for what must never be sent.
type candidate struct {
	ID          string  `json:"id"`
	Role        string  `json:"role,omitempty"`
	Subrole     string  `json:"subrole,omitempty"`
	Title       string  `json:"title,omitempty"`
	Description string  `json:"description,omitempty"`
	Parent      *parent `json:"parent,omitempty"`
	HasPopup    bool    `json:"has_popup,omitempty"` //nolint:tagliatelle // snake_case like user_query.
	App         string  `json:"app,omitempty"`
}

type parent struct {
	Role  string `json:"role,omitempty"`
	Title string `json:"title,omitempty"`
}

type question struct {
	Type         string   `json:"type"`
	Instructions string   `json:"instructions"`
	Criteria     criteria `json:"criteria"`
}

// criteria is the option IDs of one question, encoded as a JSON object in
// candidate order with NONE last.
type criteria []string

func newRequest(query string, options []semantic.Option) request {
	candidates := make([]candidate, len(options))
	ids := make(criteria, 0, len(options)+1)

	for index, option := range options {
		candidates[index] = newCandidate(option)
		ids = append(ids, option.ID)
	}

	return request{
		Model: Model,
		State: state{UserQuery: clip(query, maxFieldRunes), Candidates: candidates},
		Questions: map[string]question{
			questionID: {
				Type:         "choice",
				Instructions: Instructions,
				Criteria:     append(ids, semantic.NoneID),
			},
		},
	}
}

func newCandidate(option semantic.Option) candidate {
	sent := candidate{
		ID:          option.ID,
		Role:        clip(option.Role, maxFieldRunes),
		Subrole:     clip(option.Subrole, maxFieldRunes),
		Title:       clip(option.Title, maxFieldRunes),
		Description: clip(option.Description, maxFieldRunes),
		HasPopup:    option.HasPopup,
		App:         clip(option.AppName, maxFieldRunes),
	}

	if option.ParentRole != "" || option.ParentTitle != "" {
		sent.Parent = &parent{
			Role:  clip(option.ParentRole, maxFieldRunes),
			Title: clip(option.ParentTitle, maxFieldRunes),
		}
	}

	return sent
}

// MarshalJSON writes the criteria as an object whose keys keep their order.
func (ids criteria) MarshalJSON() ([]byte, error) {
	var buffer bytes.Buffer

	buffer.WriteByte('{')

	for index, optionID := range ids {
		if index > 0 {
			buffer.WriteByte(',')
		}

		description := "The control with id " + optionID + " in candidates"
		if optionID == semantic.NoneID {
			description = noneCriterion
		}

		key, err := json.Marshal(optionID)
		if err != nil {
			return nil, err
		}

		value, err := json.Marshal(description)
		if err != nil {
			return nil, err
		}

		buffer.Write(key)
		buffer.WriteByte(':')
		buffer.Write(value)
	}

	buffer.WriteByte('}')

	return buffer.Bytes(), nil
}

// clip trims text and cuts it to at most limit characters.
func clip(text string, limit int) string {
	text = strings.TrimSpace(text)

	count := 0
	for index := range text {
		if count == limit {
			return text[:index]
		}

		count++
	}

	return text
}
