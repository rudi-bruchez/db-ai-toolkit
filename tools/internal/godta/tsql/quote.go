// Package tsql holds every write statement goDTA sends, and the quoting that
// is the only way a name or a string enters one (spec section 7.2).
//
// Each template returns one statement, without GO and without a terminating
// semicolon: the caller sends it as its own batch, or writes it to a script
// with its own separator.
package tsql

import (
	"fmt"
	"strings"
	"unicode/utf16"
)

// maxNameLen is QUOTENAME's limit: beyond 128 characters it returns NULL.
const maxNameLen = 128

// QuoteName brackets an identifier and doubles its closing brackets, like
// QUOTENAME. It panics on a name longer than 128 characters (counted as
// nvarchar counts them, in UTF-16 code units), where QUOTENAME returns NULL:
// such a name cannot exist in the catalog, so it is a programming error.
func QuoteName(s string) string {
	if n := len(utf16.Encode([]rune(s))); n > maxNameLen {
		panic(fmt.Sprintf("tsql.QuoteName: identifier of %d characters, limit %d", n, maxNameLen))
	}
	return "[" + strings.ReplaceAll(s, "]", "]]") + "]"
}

// QuoteString returns s as a Unicode string literal, N'...', with its
// apostrophes doubled.
func QuoteString(s string) string {
	return "N'" + strings.ReplaceAll(s, "'", "''") + "'"
}

var likeEscaper = strings.NewReplacer("[", "[[]", "_", "[_]", "%", "[%]")

// LikePrefix returns a LIKE pattern matching every string that starts with
// prefix literally: [, _ and % are escaped by brackets, then % is appended.
func LikePrefix(prefix string) string {
	return likeEscaper.Replace(prefix) + "%"
}

// RunPattern is the LIKE pattern of an object name created by goDTA: godta_,
// an eight-character run ID of [a-z0-9], then _. godta[_]% alone would also
// match the test data set's objects (godta_t, godta_filt) and a customer's own.
func RunPattern() string {
	return "godta[_]" + strings.Repeat("[a-z0-9]", runIDLen) + "[_]%"
}

const runIDLen = 8

// IsRunName reports whether s has the form RunPattern describes. It is
// case-sensitive, where LIKE follows the collation: a run ID is always lower
// case, so a name LIKE finds under a case-insensitive collation but this
// rejects is not goDTA's.
func IsRunName(s string) bool {
	rest, ok := strings.CutPrefix(s, "godta_")
	if !ok || len(rest) < runIDLen+1 {
		return false
	}
	for i := range runIDLen {
		c := rest[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return rest[runIDLen] == '_'
}
