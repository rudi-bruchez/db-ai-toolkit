package tsql

import "strings"

// Marker is in the text of every query goDTA sends, as a comment right after
// its first keyword: SELECT /* godta:internal */ ... The Query Store and the
// plan cache capture goDTA's own catalog reads like any other query (in
// capture mode ALL, a single dry-run is enough), and collect leaves out every
// text that carries it. A query that filters on it carries it in a string
// literal, which matches, so it leaves itself out too.
const Marker = "godta:internal"

// LegacyMarker is the comment collect's queries carried before Marker: the
// Query Store of a database collected by an earlier build still holds them.
const LegacyMarker = "godta:collect"

// IsInternal reports whether text, as the Query Store or the plan cache holds
// it, is a query goDTA sent.
func IsInternal(text string) bool {
	return strings.Contains(text, Marker) || strings.Contains(text, LegacyMarker)
}
