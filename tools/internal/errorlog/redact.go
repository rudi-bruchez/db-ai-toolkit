package errorlog

import (
	"regexp"
	"sort"
	"strconv"
)

var (
	// reDBQuoted matches an explicitly quoted database name, e.g. database
	// 'ApiCatalog'. Quoting is required so ordinary prose following the word
	// "database" (has, is, was, ...) is never mistaken for a name.
	reDBQuoted = regexp.MustCompile(`(?i)(?:Database|base de données)[\s\x{00a0}]+'([^']+)'`)
	// reDBColon matches the unquoted "Database: name" form used in backup
	// summary lines (e.g. "Database: sales,"). The colon disambiguates it
	// from plain sentences like "The database has already joined...".
	reDBColon = regexp.MustCompile(`(?i)(?:Database|base de données)[\s\x{00a0}]*:[\s\x{00a0}]*([A-Za-z0-9_]+)`)
	reLogin   = regexp.MustCompile(`(?i)(?:user|utilisateur|login)[\s\x{00a0}]+'([^']+)'`)
	reIP2     = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
)

// Redactor maps sensitive entities to stable tokens (DB_1, LOGIN_1, IP_1) so a
// log can be shared for diagnosis without exposing real names.
type Redactor struct {
	tokens map[string]string // value -> token
	order  []string          // values in insertion order (for legend)
	nDB    int
	nLogin int
	nIP    int
}

func NewRedactor() *Redactor {
	return &Redactor{tokens: map[string]string{}}
}

func (r *Redactor) register(value, prefix string, counter *int) {
	if value == "" {
		return
	}
	if _, ok := r.tokens[value]; ok {
		return
	}
	*counter++
	r.tokens[value] = prefix + "_" + strconv.Itoa(*counter)
	r.order = append(r.order, value)
}

// AddLogin registers value directly as a login entity, bypassing Scan's
// pattern matching. Use it for values that are already known to be sensitive
// (e.g. a service account name pulled from a structured summary field) but
// that would not otherwise match reLogin's quoted "user '...'" form.
func (r *Redactor) AddLogin(value string) {
	r.register(value, "LOGIN", &r.nLogin)
}

// Scan finds entities in s and assigns them stable tokens.
func (r *Redactor) Scan(s string) {
	for _, m := range reDBQuoted.FindAllStringSubmatch(s, -1) {
		r.register(m[1], "DB", &r.nDB)
	}
	for _, m := range reDBColon.FindAllStringSubmatch(s, -1) {
		r.register(m[1], "DB", &r.nDB)
	}
	for _, m := range reLogin.FindAllStringSubmatch(s, -1) {
		r.register(m[1], "LOGIN", &r.nLogin)
	}
	for _, m := range reIP2.FindAllString(s, -1) {
		r.register(m, "IP", &r.nIP)
	}
}

// Apply replaces every registered value with its token. Longer values are
// replaced first so a value that is a substring of another is not corrupted.
// Replacement is boundary-aware: a value only matches when it is not flanked
// by identifier characters (letters, digits, underscore), so a short value
// like "sa" redacts the quoted login 'sa' but leaves it untouched inside an
// ordinary word like "message".
func (r *Redactor) Apply(s string) string {
	vals := make([]string, len(r.order))
	copy(vals, r.order)
	sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	for _, v := range vals {
		re := regexp.MustCompile(`(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(v) + `([^A-Za-z0-9_]|$)`)
		s = re.ReplaceAllString(s, `${1}`+r.tokens[v]+`${2}`)
	}
	return s
}

// Legend returns "TOKEN = value" lines sorted by token.
func (r *Redactor) Legend() []string {
	lines := make([]string, 0, len(r.order))
	for _, v := range r.order {
		lines = append(lines, r.tokens[v]+" = "+v)
	}
	sort.Strings(lines)
	return lines
}
