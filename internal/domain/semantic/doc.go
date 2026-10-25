// Package semantic picks the one on-screen control a natural-language query
// names, out of more candidates than a single question can carry.
//
// A Chooser answers one closed question: which of these options fulfills the
// query, or NONE. Select runs the tournament around it. The candidates are
// split in halves until every leaf fits one question, every leaf is asked
// once, and the leaves' winners, with their original metadata and nothing
// else, meet in a final that is the same tournament run again. A chooser only
// ever returns an option ID, so nothing a leaf said about its own certainty
// can reach a comparison with another leaf.
package semantic
