// Package jev implements ports.TargetSelector with TypeSafe's Jev model: one
// System One choice question per leaf of the semantic tournament, over stdlib
// net/http.
//
// This is the only package in Neru that reaches the network. It sends the
// user's query and each candidate's role, subrole, title, description, parent
// role and title, popup flag and app name, every string cut to 200 characters,
// and nothing else: no element value, keystroke, screenshot, window title or
// bounds. The API key is read from its file on every selection, refused when
// other users can reach it, and never logged or put in an error.
package jev
