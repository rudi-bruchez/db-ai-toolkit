# errorlog-parse Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rewrite the `errorlog-parse` CLI into a token-saving SQL Server ERRORLOG preprocessor that decodes real (UTF-16) logs, filters noise via extensible multilingual rule packs, summarizes the instance, emits actionable advisories, aggregates repeats, optionally redacts, and renders a compact text or Markdown digest.

**Architecture:** One Go module (`tools/`), one internal package `errorlog` split into focused single-responsibility files, driven by a thin `cmd/errorlog-parse/main.go`. Pipeline: `acquire → decode → parse → (summary ∥ classify+timerange → aggregate → redact) → advice → render`. Rules and advisories are data files (`.rules`) embedded via `go:embed` and overridable at runtime.

**Tech Stack:** Go 1.22, **standard library only** (no external modules). `archive/zip`, `compress/gzip`, `unicode/utf16`, `regexp`, `embed`, `flag`, `testing`.

## Global Constraints

- **Go 1.22**, module `github.com/rudi-bruchez/db-ai-toolkit/tools` (already exists in `tools/go.mod`).
- **Zero external dependencies.** Standard library only. Rule packs are plain-text `.rules` files, never YAML.
- **KISS / no unnecessary architectural complexity.** It is a small, compact executable.
- **Multilingual by data**: matching relies on error/severity **numbers** (language-independent) plus keyword rules in per-language packs. All packs apply simultaneously (logs can be mixed FR+EN).
- **NBSP-aware regexes**: French SQL Server puts U+00A0 before colons — patterns matching a colon use `[\s\x{00A0}]*:`.
- **Encoding**: input may be UTF-16LE/BE with BOM or UTF-8; line endings may be CRLF.
- **Filtering is fail-safe**: unknown entries are kept.
- **Quality gate**: `go vet ./...` and `go test ./...` green; `golangci-lint` green; apply relevant golang-skills; a final `/code-review` at effort **xhigh** before merge (see Task 12).
- **Test data privacy**: committed fixtures use **synthetic** data only — never the real customer names from the 83 MB sample.

Reference spec: [docs/superpowers/specs/2026-07-15-errorlog-parse-design.md](../specs/2026-07-15-errorlog-parse-design.md).

## File Structure

All paths under `tools/`:

| File | Responsibility |
|------|----------------|
| `internal/errorlog/decode.go` | Bytes → UTF-8 text (BOM detection, CRLF→LF) |
| `internal/errorlog/parse.go` | UTF-8 text → `[]Entry` (timestamp + continuation grouping). **Replaces v1** |
| `internal/errorlog/rules.go` | Load `.rules` packs, `RuleSet.Classify` |
| `internal/errorlog/rules/common.rules`, `en.rules`, `fr.rules` | Signal/noise/severity rules (data) |
| `internal/errorlog/summary.go` | Boot sequence → `InstanceSummary` |
| `internal/errorlog/advice.go` | `advice.rules` (count) + `checks.rules` (boot presence) → `[]Advisory` |
| `internal/errorlog/rules/advice.rules`, `checks.rules` | Advisory data |
| `internal/errorlog/aggregate.go` | `[]Entry` (kept) → `[]Event` (collapse repeats) |
| `internal/errorlog/redact.go` | Consistent pseudonymization + legend |
| `internal/errorlog/render.go` | `Report` → text or Markdown |
| `internal/errorlog/input.go` | Path → bytes (zip/gz/plain, file/dir/stdin) |
| `cmd/errorlog-parse/main.go` | Flags + pipeline wiring. **Replaces v1** |
| `internal/errorlog/testdata/` | Synthetic golden fixture + expected output |

**v1 removal**: Task 2 replaces `internal/errorlog/parse.go` and deletes `internal/errorlog/parse_test.go`; Task 11 replaces `cmd/errorlog-parse/main.go`.

Shared types (defined in the task that owns them, listed here for reference):

```go
// parse.go
type Entry struct {
	Time    time.Time // parsed from first line; zero value if unparseable
	RawTime string    // timestamp token as written
	Source  string    // "Server" | "Logon" | "Backup" | "spid51" ...
	Lines   []string  // first line + continuation lines, raw
}

// aggregate.go
type Event struct {
	First, Last time.Time
	Count       int    // 1 = single entry, >1 = aggregated
	Text        string // representative text (multi-line for singletons)
}

// advice.go
type Advisory struct {
	ID      string
	Message string
	URL     string
}

// summary.go
type InstanceSummary struct {
	Product, Edition, OS, CPU, Collation, AuthMode, ServiceAcct, UTCAdjust, LogPath string
	RAMMB                            int
	FirstTime, LastTime, StartTime   time.Time
	TCPPorts, AGListeners            []string
	SPN, IFI                         string // detailed (Markdown) fields
}

// render.go
type Report struct {
	Summary              InstanceSummary
	Advisories           []Advisory
	Total, Kept, Dropped int
	DroppedByCat         map[string]int
	Events               []Event
}
```

---

### Task 1: Decoder (`decode.go`)

**Files:**
- Create: `tools/internal/errorlog/decode.go`
- Test: `tools/internal/errorlog/decode_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func Decode(raw []byte) string` — UTF-8 text with LF endings.
- Produces test helper (in `decode_test.go`, exported via a small helper used by later tests): `func encodeUTF16LE(s string) []byte`.

- [ ] **Step 1: Write the failing test**

```go
package errorlog

import (
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

// encodeUTF16LE builds a UTF-16LE byte slice with BOM and CRLF endings,
// mimicking a real SQL Server ERRORLOG on Windows.
func encodeUTF16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := []byte{0xFF, 0xFE}
	for _, v := range u {
		b = binary.LittleEndian.AppendUint16(b, v)
	}
	return b
}

func TestDecodeUTF16LEWithBOMAndCRLF(t *testing.T) {
	raw := encodeUTF16LE("Erreur : 976\r\nGravité : 14\r\n")
	got := Decode(raw)
	want := "Erreur : 976\nGravité : 14\n"
	if got != want {
		t.Fatalf("Decode = %q, want %q", got, want)
	}
}

func TestDecodeUTF8Passthrough(t *testing.T) {
	if got := Decode([]byte("plain\r\ntext")); got != "plain\ntext" {
		t.Fatalf("Decode = %q", got)
	}
	if got := Decode([]byte{0xEF, 0xBB, 0xBF, 'x'}); got != "x" {
		t.Fatalf("BOM strip failed: %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tools && go test ./internal/errorlog -run TestDecode -v`
Expected: FAIL — `undefined: Decode`.

- [ ] **Step 3: Write minimal implementation**

```go
package errorlog

import (
	"encoding/binary"
	"strings"
	"unicode/utf16"
)

// Decode converts raw ERRORLOG bytes to UTF-8 text with LF line endings,
// detecting the encoding from a leading BOM. SQL Server writes ERRORLOG in
// UTF-16LE with a BOM on Windows; UTF-8 (with or without BOM) is also handled.
func Decode(raw []byte) string {
	switch {
	case len(raw) >= 2 && raw[0] == 0xFF && raw[1] == 0xFE:
		return normalizeNewlines(decodeUTF16(raw[2:], binary.LittleEndian))
	case len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF:
		return normalizeNewlines(decodeUTF16(raw[2:], binary.BigEndian))
	case len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF:
		return normalizeNewlines(string(raw[3:]))
	default:
		return normalizeNewlines(string(raw))
	}
}

func decodeUTF16(b []byte, order binary.ByteOrder) string {
	if len(b)%2 != 0 {
		b = b[:len(b)-1] // drop a dangling odd byte defensively
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = order.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u))
}

func normalizeNewlines(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tools && go test ./internal/errorlog -run TestDecode -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/internal/errorlog/decode.go tools/internal/errorlog/decode_test.go
git commit -m "feat(errorlog): UTF-16/BOM/CRLF decoder"
```

---

### Task 2: Parser (`parse.go`, replaces v1)

**Files:**
- Modify (full rewrite): `tools/internal/errorlog/parse.go`
- Delete: `tools/internal/errorlog/parse_test.go` (v1 tests for the old API)
- Test: `tools/internal/errorlog/parse_test.go` (new content)

**Interfaces:**
- Consumes: `Decode` output (UTF-8/LF text).
- Produces:
  - `type Entry struct { Time time.Time; RawTime, Source string; Lines []string }`
  - `func (e Entry) Text() string` — all lines joined by `\n`.
  - `func (e Entry) Message() string` — text minus the `timestamp + source` prefix of the first line.
  - `func Parse(text string) []Entry` — groups continuation lines (lines not starting with a timestamp) into the preceding entry.

- [ ] **Step 1: Delete the v1 test file**

Run: `git rm tools/internal/errorlog/parse_test.go`

- [ ] **Step 2: Write the failing test**

```go
package errorlog

import "testing"

const twoEntries = "2026-06-12 15:28:18.32 spid61     Erreur : 824, Gravité : 24, État : 2.\n" +
	"2026-06-12 15:28:18.32 spid61     Detail line about page (1:2571).\n" +
	"2026-06-12 15:28:19.00 Server     Next entry.\n"

func TestParseGroupsContinuations(t *testing.T) {
	// The "Detail line" starts with a timestamp too, so it is its own entry.
	// Build an explicit continuation case with a TAB-prefixed line instead.
	text := "2026-06-12 15:28:18.32 Server     Registry startup parameters:\n" +
		"\t -d F:\\master.mdf\n" +
		"\t -e F:\\ERRORLOG\n" +
		"2026-06-12 15:28:19.00 Server     Next.\n"
	entries := Parse(text)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if len(entries[0].Lines) != 3 {
		t.Fatalf("first entry lines = %d, want 3 (1 + 2 continuations)", len(entries[0].Lines))
	}
	if entries[0].Source != "Server" {
		t.Fatalf("source = %q, want Server", entries[0].Source)
	}
	if entries[0].Time.IsZero() {
		t.Fatal("timestamp not parsed")
	}
}

func TestParseMessageStripsPrefix(t *testing.T) {
	e := Parse("2026-06-12 15:28:18.32 Logon      Login failed for user 'x'.\n")[0]
	if got := e.Message(); got != "Login failed for user 'x'." {
		t.Fatalf("Message = %q", got)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd tools && go test ./internal/errorlog -run TestParse -v`
Expected: FAIL (compile error or wrong counts) after the rewrite in Step 4 is not yet in place. If the old `Parse` still compiles, expect assertion failures.

- [ ] **Step 4: Write the implementation**

```go
package errorlog

import (
	"regexp"
	"strings"
	"time"
)

const tsLayout = "2006-01-02 15:04:05.00"

// lineRe matches a top-level entry: timestamp, source column, message.
var lineRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{2})\s+(\S+)\s+(.*)$`)

// Entry is one logical ERRORLOG record: a timestamped first line plus any
// continuation lines that followed it (stack dumps, startup parameters, ...).
type Entry struct {
	Time    time.Time
	RawTime string
	Source  string
	Lines   []string
}

// Text returns the entry's raw lines joined with newlines.
func (e Entry) Text() string { return strings.Join(e.Lines, "\n") }

// Message returns the entry text without the "timestamp + source" prefix on
// the first line; continuation lines are appended unchanged.
func (e Entry) Message() string {
	if len(e.Lines) == 0 {
		return ""
	}
	m := lineRe.FindStringSubmatch(e.Lines[0])
	if m == nil {
		return e.Text()
	}
	parts := append([]string{m[3]}, e.Lines[1:]...)
	return strings.Join(parts, "\n")
}

// Parse splits UTF-8/LF text into entries, attaching non-timestamped lines to
// the preceding entry.
func Parse(text string) []Entry {
	var entries []Entry
	var cur *Entry
	flush := func() {
		if cur == nil {
			return
		}
		for len(cur.Lines) > 0 && strings.TrimSpace(cur.Lines[len(cur.Lines)-1]) == "" {
			cur.Lines = cur.Lines[:len(cur.Lines)-1]
		}
		if len(cur.Lines) > 0 {
			entries = append(entries, *cur)
		}
		cur = nil
	}
	for _, line := range strings.Split(text, "\n") {
		if m := lineRe.FindStringSubmatch(line); m != nil {
			flush()
			t, _ := time.Parse(tsLayout, m[1])
			cur = &Entry{Time: t, RawTime: m[1], Source: m[2], Lines: []string{line}}
			continue
		}
		if cur == nil {
			cur = &Entry{Lines: []string{line}}
			continue
		}
		cur.Lines = append(cur.Lines, line)
	}
	flush()
	return entries
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd tools && go test ./internal/errorlog -run TestParse -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add tools/internal/errorlog/parse.go tools/internal/errorlog/parse_test.go
git commit -m "feat(errorlog): entry parser with continuation grouping (replaces v1)"
```

---

### Task 3: Rule engine + packs (`rules.go`, `rules/*.rules`)

**Files:**
- Create: `tools/internal/errorlog/rules.go`
- Create: `tools/internal/errorlog/rules/common.rules`
- Create: `tools/internal/errorlog/rules/en.rules`
- Create: `tools/internal/errorlog/rules/fr.rules`
- Test: `tools/internal/errorlog/rules_test.go`

**Interfaces:**
- Consumes: `Entry` (Task 2).
- Produces:
  - `type RuleSet struct { ... }`
  - `func LoadEmbeddedRules() (*RuleSet, error)` — loads `rules/*.rules` except `advice.rules`/`checks.rules`.
  - `func (rs *RuleSet) LoadDir(dir string) error` — add/override packs from a directory.
  - `func (rs *RuleSet) Classify(e Entry, minSeverity int) (keep bool, category string)`.
- Rule line format (all `.rules` files): `category<TAB>kind<TAB>regexp`, one TAB between columns, `#` comments, blank lines ignored. `kind` ∈ `signal|noise|severity`. A `severity` rule's regex must have one capture group around the number.

- [ ] **Step 1: Create the rule pack data files**

`tools/internal/errorlog/rules/common.rules` (columns separated by a single TAB):

```text
# category	kind	regexp   (language-independent number rules)
error-num	signal	(?i)(Error|Erreur)[\s\x{00A0}]*:[\s\x{00A0}]*\d+
severity-en	severity	(?i)Severity[\s\x{00A0}]*:[\s\x{00A0}]*(\d{1,2})
severity-fr	severity	(?i)Gravité[\s\x{00A0}]*:[\s\x{00A0}]*(\d{1,2})
dump	signal	(?i)Stack (Dump|Signature)|SqlDumpExceptionHandler|BugCheck
assertion	signal	(?i)SQL Server Assertion
io-stall	signal	(?i)I/O requests taking longer than
paged-out	signal	(?i)has been paged out|significant part of sql server process memory
corruption	signal	(?i)consistency-based I/O error|torn page|incorrect checksum|\bcorrupt|\bsuspect\b
```

`tools/internal/errorlog/rules/en.rules`:

```text
# category	kind	regexp
login-fail	signal	(?i)Login failed
deadlock	signal	(?i)deadlock
ag-error	signal	(?i)availability (group|replica).*(error|fail|offline)
backup	noise	(?i)Log was backed up|Database backed up|BACKUP (DATABASE|LOG).*successfully
login-ok	noise	(?i)Login succeeded for user
checkdb-ok	noise	(?i)CHECKDB.*found 0 .*error
db-option	noise	(?i)Setting database option .* to
startup	noise	(?i)Starting up database|is starting up|Server is listening|Recovery is complete
```

`tools/internal/errorlog/rules/fr.rules`:

```text
# category	kind	regexp
login-fail	signal	(?i)Échec de la connexion
backup	noise	(?i)journal.*sauvegard|Log was backed up
checkdb-ok	noise	(?i)CHECKDB.*0 erreur.*0 (erreur|cohérence)
db-option	noise	(?i)Définition de l'option de base de données
startup	noise	(?i)Démarrage de la base de données|La récupération est terminée
```

> Note: `login-fail` in `en.rules` matches the English "Login failed" that this
> French server still emits; `fr.rules` adds the fully localized variant.

- [ ] **Step 2: Write the failing test**

```go
package errorlog

import "testing"

func mustRules(t *testing.T) *RuleSet {
	t.Helper()
	rs, err := LoadEmbeddedRules()
	if err != nil {
		t.Fatalf("LoadEmbeddedRules: %v", err)
	}
	return rs
}

func classifyLine(t *testing.T, rs *RuleSet, line string, minSev int) (bool, string) {
	t.Helper()
	e := Parse(line + "\n")[0]
	return rs.Classify(e, minSev)
}

func TestClassifySignalNoiseSeverity(t *testing.T) {
	rs := mustRules(t)
	cases := []struct {
		line     string
		wantKeep bool
	}{
		{"2026-06-16 00:00:01.22 Logon      Login failed for user 'x'.", true},
		{"2026-06-13 00:00:00.50 spid61     Erreur : 824, Gravité : 24, État : 2.", true},
		{"2026-06-12 15:28:18.32 Backup     Log was backed up. Database: sales.", false},
		{"2026-06-12 15:28:18.32 Logon      Login succeeded for user 'svc'.", false},
		{"2026-06-12 15:28:18.32 spid20     Some entry we have no rule for.", true}, // unknown -> keep
	}
	for _, c := range cases {
		keep, _ := classifyLine(t, rs, c.line, 16)
		if keep != c.wantKeep {
			t.Errorf("Classify(%q) keep=%v, want %v", c.line, keep, c.wantKeep)
		}
	}
}

func TestSeverityThresholdBeatsNoise(t *testing.T) {
	rs := mustRules(t)
	// Looks like backup noise but carries Gravité 21 -> keep.
	line := "2026-06-12 15:28:18.32 spid20     Log was backed up but Gravité : 21 raised."
	if keep, _ := classifyLine(t, rs, line, 16); !keep {
		t.Fatal("high severity must override noise")
	}
	// Below threshold, matches only noise -> drop.
	line14 := "2026-06-12 15:28:18.32 Logon      Erreur : 18456, Gravité : 14 : Login succeeded for user 'x'."
	if keep, _ := classifyLine(t, rs, line14, 16); !keep {
		t.Skip("kept via error-num signal, acceptable")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd tools && go test ./internal/errorlog -run TestClassify -v`
Expected: FAIL — `undefined: LoadEmbeddedRules`.

- [ ] **Step 4: Write the implementation**

```go
package errorlog

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

//go:embed rules/*.rules
var rulesFS embed.FS

type ruleKind int

const (
	kindSignal ruleKind = iota
	kindNoise
	kindSeverity
)

type rule struct {
	category string
	kind     ruleKind
	re       *regexp.Regexp
}

// RuleSet is the merged set of classification rules from one or more packs.
type RuleSet struct {
	rules []rule
}

var tabRe = regexp.MustCompile(`\t+`)

func parseKind(s string) (ruleKind, error) {
	switch s {
	case "signal":
		return kindSignal, nil
	case "noise":
		return kindNoise, nil
	case "severity":
		return kindSeverity, nil
	default:
		return 0, fmt.Errorf("unknown kind %q", s)
	}
}

func parseRuleLines(name, content string) ([]rule, error) {
	var out []rule
	for i, raw := range strings.Split(content, "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		cols := tabRe.Split(strings.TrimSpace(line), 3)
		if len(cols) != 3 {
			return nil, fmt.Errorf("%s:%d: expected 3 TAB-separated columns, got %d", name, i+1, len(cols))
		}
		kind, err := parseKind(cols[1])
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", name, i+1, err)
		}
		re, err := regexp.Compile(cols[2])
		if err != nil {
			return nil, fmt.Errorf("%s:%d: bad regexp: %w", name, i+1, err)
		}
		out = append(out, rule{category: cols[0], kind: kind, re: re})
	}
	return out, nil
}

// LoadEmbeddedRules loads every embedded rules/*.rules pack except the advisory
// data files (advice.rules, checks.rules), which have their own loaders.
func LoadEmbeddedRules() (*RuleSet, error) {
	rs := &RuleSet{}
	entries, err := rulesFS.ReadDir("rules")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".rules") || name == "advice.rules" || name == "checks.rules" {
			continue
		}
		content, err := fs.ReadFile(rulesFS, "rules/"+name)
		if err != nil {
			return nil, err
		}
		rules, err := parseRuleLines(name, string(content))
		if err != nil {
			return nil, err
		}
		rs.rules = append(rs.rules, rules...)
	}
	return rs, nil
}

// LoadDir adds packs from a user-supplied directory (rules/*.rules), letting
// contributors extend or override the embedded ones without recompiling.
func (rs *RuleSet) LoadDir(dir string) error {
	matches, err := filepath.Glob(filepath.Join(dir, "*.rules"))
	if err != nil {
		return err
	}
	for _, path := range matches {
		base := filepath.Base(path)
		if base == "advice.rules" || base == "checks.rules" {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rules, err := parseRuleLines(base, string(content))
		if err != nil {
			return err
		}
		rs.rules = append(rs.rules, rules...)
	}
	return nil
}

// Classify decides whether to keep an entry and, if dropped, its noise
// category. Precedence: severity >= threshold, then any signal, then noise,
// else keep (fail-safe for unknown entries).
func (rs *RuleSet) Classify(e Entry, minSeverity int) (keep bool, category string) {
	text := e.Text()
	for _, r := range rs.rules {
		if r.kind == kindSeverity {
			if m := r.re.FindStringSubmatch(text); len(m) > 1 {
				if n, err := strconv.Atoi(m[1]); err == nil && n >= minSeverity {
					return true, ""
				}
			}
		}
	}
	for _, r := range rs.rules {
		if r.kind == kindSignal && r.re.MatchString(text) {
			return true, ""
		}
	}
	for _, r := range rs.rules {
		if r.kind == kindNoise && r.re.MatchString(text) {
			return false, r.category
		}
	}
	return true, ""
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd tools && go test ./internal/errorlog -run 'TestClassify|TestSeverity' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add tools/internal/errorlog/rules.go tools/internal/errorlog/rules/ tools/internal/errorlog/rules_test.go
git commit -m "feat(errorlog): data-driven multilingual rule engine + packs"
```

---

### Task 4: Instance summary (`summary.go`)

**Files:**
- Create: `tools/internal/errorlog/summary.go`
- Test: `tools/internal/errorlog/summary_test.go`

**Interfaces:**
- Consumes: `[]Entry` (Task 2).
- Produces:
  - `type InstanceSummary struct { ... }` (fields listed in File Structure).
  - `func Summarize(entries []Entry) InstanceSummary` — extracts boot facts and the first/last timestamps.
  - `func BootText(entries []Entry, maxLines int) string` — concatenated raw lines of the boot region (used by Task 5).

- [ ] **Step 1: Write the failing test**

```go
package errorlog

import "testing"

const bootSample = "2026-06-12 15:28:18.32 Server     Microsoft SQL Server 2022 (RTM-CU25) (KB5081477) - 16.0.4255.1 (X64)\n" +
	"2026-06-12 15:28:18.32 Server     Standard Edition (64-bit) on Windows Server 2025 Datacenter\n" +
	"2026-06-12 15:28:18.33 Server     UTC adjustment: 2:00\n" +
	"2026-06-12 15:28:18.34 Server     Authentication mode is MIXED.\n" +
	"2026-06-12 15:28:18.38 Server     Detected 65535 MB of RAM, 60393 MB of available memory.\n" +
	"2026-06-12 15:28:18.66 Server     Default collation: French_CI_AS (Français 1036)\n" +
	"2026-06-12 15:28:20.53 spid35s    Server is listening on [ 'any' <ipv4> 1433] accept sockets 1.\n" +
	"2026-06-12 15:28:19.86 Server     Database Instant File Initialization: activé.\n" +
	"2026-06-16 09:12:00.00 Backup     Log was backed up. Database: sales.\n"

func TestSummarizeExtractsCoreFacts(t *testing.T) {
	s := Summarize(Parse(bootSample))
	if s.RAMMB != 65535 {
		t.Errorf("RAMMB = %d, want 65535", s.RAMMB)
	}
	if s.AuthMode != "MIXED" {
		t.Errorf("AuthMode = %q, want MIXED", s.AuthMode)
	}
	if s.Collation != "French_CI_AS" {
		t.Errorf("Collation = %q", s.Collation)
	}
	if s.StartTime.IsZero() || s.FirstTime.IsZero() || s.LastTime.IsZero() {
		t.Error("timestamps not set")
	}
	if s.LastTime.Before(s.FirstTime) {
		t.Error("LastTime before FirstTime")
	}
	if len(s.TCPPorts) == 0 || s.TCPPorts[0] != "1433" {
		t.Errorf("TCPPorts = %v, want [1433 ...]", s.TCPPorts)
	}
	if s.IFI == "" {
		t.Error("IFI not detected")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tools && go test ./internal/errorlog -run TestSummarize -v`
Expected: FAIL — `undefined: Summarize`.

- [ ] **Step 3: Write the implementation**

```go
package errorlog

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	reProduct   = regexp.MustCompile(`(Microsoft SQL Server .*?\d+\.\d+\.\d+\.\d+)`)
	reEdition   = regexp.MustCompile(`(?i)((?:Standard|Enterprise|Developer|Express|Web)\b.*Edition.*)`)
	reRAM       = regexp.MustCompile(`Detected (\d+) MB of RAM`)
	reAuth      = regexp.MustCompile(`(?i)Authentication mode is (\w+)`)
	reCollation = regexp.MustCompile(`(?i)Default collation:\s*(\S+)`)
	reUTC       = regexp.MustCompile(`(?i)UTC adjustment:\s*([\d:]+)`)
	rePort      = regexp.MustCompile(`(?i)listening on \[ .* (\d{2,5})\] accept sockets`)
	reIFI       = regexp.MustCompile(`(?i)Instant File Initialization[\s\x{00a0}]*:[\s\x{00a0}]*(\S+?)[.\s]`)
	reListener  = regexp.MustCompile(`(?i)listening on virtual network name '([^']+)'`)
	reSvc       = regexp.MustCompile(`(?i)service account is '([^']+)'`)
	reLogPath   = regexp.MustCompile(`(?i)Logging SQL Server messages in file '([^']+)'`)
	reSPN       = regexp.MustCompile(`(?i)(Service Principal Name.*)$`)
)

// Summarize extracts instance facts from the boot sequence and the log's time
// window. Fields not present in the boot region are left as zero values.
func Summarize(entries []Entry) InstanceSummary {
	var s InstanceSummary
	boot := BootText(entries, 400)
	first := firstMatch(reProduct, boot)
	s.Product = first
	s.Edition = firstMatch(reEdition, boot)
	if m := reRAM.FindStringSubmatch(boot); m != nil {
		s.RAMMB, _ = strconv.Atoi(m[1])
	}
	s.AuthMode = firstMatch(reAuth, boot)
	s.Collation = firstMatch(reCollation, boot)
	s.UTCAdjust = firstMatch(reUTC, boot)
	s.IFI = firstMatch(reIFI, boot)
	s.ServiceAcct = firstMatch(reSvc, boot)
	s.LogPath = firstMatch(reLogPath, boot)
	s.SPN = firstMatch(reSPN, boot)
	s.TCPPorts = uniqueMatches(rePort, boot)
	s.AGListeners = uniqueMatches(reListener, boot)

	for _, e := range entries {
		if e.Time.IsZero() {
			continue
		}
		if s.FirstTime.IsZero() {
			s.FirstTime = e.Time
			s.StartTime = e.Time
		}
		s.LastTime = e.Time
	}
	return s
}

// BootText joins the raw lines of the first entries (the boot region) so that
// facts and boot checks can be matched against a single string.
func BootText(entries []Entry, maxLines int) string {
	var b strings.Builder
	n := 0
	for _, e := range entries {
		for _, l := range e.Lines {
			b.WriteString(l)
			b.WriteByte('\n')
			n++
			if n >= maxLines {
				return b.String()
			}
		}
	}
	return b.String()
}

func firstMatch(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func uniqueMatches(re *regexp.Regexp, s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		v := strings.TrimSpace(m[1])
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// StartTimeFmt is a helper for renderers.
func (s InstanceSummary) window() string {
	if s.FirstTime.IsZero() {
		return ""
	}
	const f = "2006-01-02 15:04"
	return s.FirstTime.Format(f) + " → " + s.LastTime.Format(f)
}

var _ = time.Time{} // time imported for InstanceSummary fields
```

> Note: `InstanceSummary` is declared here (owning file). Remove the
> `var _ = time.Time{}` line if `time` is already referenced; it only guards
> against an unused import during incremental editing.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tools && go test ./internal/errorlog -run TestSummarize -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/internal/errorlog/summary.go tools/internal/errorlog/summary_test.go
git commit -m "feat(errorlog): instance summary from boot sequence"
```

---

### Task 5: Advisories (`advice.go`, `advice.rules`, `checks.rules`)

**Files:**
- Create: `tools/internal/errorlog/advice.go`
- Create: `tools/internal/errorlog/rules/advice.rules`
- Create: `tools/internal/errorlog/rules/checks.rules`
- Test: `tools/internal/errorlog/advice_test.go`

**Interfaces:**
- Consumes: `map[string]int` (dropped-by-category counts, from the classify pass), boot text (`BootText`, Task 4).
- Produces:
  - `type Advisory struct { ID, Message, URL string }`
  - `func LoadEmbeddedAdvisories() (*Advisor, error)`
  - `func (a *Advisor) Evaluate(droppedByCat map[string]int, bootText string) []Advisory`

- [ ] **Step 1: Create the advisory data files**

`tools/internal/errorlog/rules/advice.rules` (columns TAB-separated: `category<TAB>min_count<TAB>message<TAB>url`; `{count}` is interpolated):

```text
# category	min_count	message	url
backup	500	{count} successful-backup messages dominate this log. Enable trace flag 3226 to stop logging successful backups.	https://www.mssqltips.com/sqlservertip/1457/stop-logging-all-successful-backups-in-your-sql-server-error-logs/
```

`tools/internal/errorlog/rules/checks.rules` (columns TAB-separated: `check_id<TAB>detect_regexp<TAB>message<TAB>url`):

```text
# check_id	detect_regexp	message	url
ifi-off	(?i)Instant File Initialization[\s\x{00a0}]*:[\s\x{00a0}]*(disabled|désactivé)	Instant File Initialization is off. Grant "Perform Volume Maintenance Tasks" to the service account for instant file initialization.	https://learn.microsoft.com/sql/relational-databases/databases/database-instant-file-initialization
spn-fail	(?i)failed to register.*Service Principal Name|échec.*SPN	SPN registration failed: Kerberos authentication will fail. Register the SPN (setspn) or check the service account rights.	https://learn.microsoft.com/sql/database-engine/configure-windows/register-a-service-principal-name-for-kerberos-connections
```

- [ ] **Step 2: Write the failing test**

```go
package errorlog

import (
	"strings"
	"testing"
)

func TestAdviceCountAndBootCheck(t *testing.T) {
	a, err := LoadEmbeddedAdvisories()
	if err != nil {
		t.Fatalf("LoadEmbeddedAdvisories: %v", err)
	}
	boot := "Database Instant File Initialization: désactivé. blah"
	adv := a.Evaluate(map[string]int{"backup": 105575}, boot)

	var haveBackup, haveIFI bool
	for _, x := range adv {
		if x.ID == "backup" {
			haveBackup = true
			if !strings.Contains(x.Message, "105575") {
				t.Errorf("count not interpolated: %q", x.Message)
			}
			if !strings.Contains(x.Message, "3226") {
				t.Errorf("missing TF 3226: %q", x.Message)
			}
		}
		if x.ID == "ifi-off" {
			haveIFI = true
		}
	}
	if !haveBackup || !haveIFI {
		t.Fatalf("advisories missing: backup=%v ifi=%v", haveBackup, haveIFI)
	}
}

func TestAdviceBelowThresholdSilent(t *testing.T) {
	a, _ := LoadEmbeddedAdvisories()
	if adv := a.Evaluate(map[string]int{"backup": 10}, ""); len(adv) != 0 {
		t.Fatalf("expected no advisories, got %v", adv)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd tools && go test ./internal/errorlog -run TestAdvice -v`
Expected: FAIL — `undefined: LoadEmbeddedAdvisories`.

- [ ] **Step 4: Write the implementation**

```go
package errorlog

import (
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
)

type countAdvice struct {
	category string
	minCount int
	template string
	url      string
}

type bootCheck struct {
	id      string
	re      *regexp.Regexp
	message string
	url     string
}

// Advisor holds the two advisory sources: category-volume and boot-state.
type Advisor struct {
	counts []countAdvice
	checks []bootCheck
}

// LoadEmbeddedAdvisories reads advice.rules and checks.rules from the embedded
// rules directory.
func LoadEmbeddedAdvisories() (*Advisor, error) {
	a := &Advisor{}
	adviceRaw, err := fs.ReadFile(rulesFS, "rules/advice.rules")
	if err != nil {
		return nil, err
	}
	if err := a.parseAdvice(string(adviceRaw)); err != nil {
		return nil, err
	}
	checksRaw, err := fs.ReadFile(rulesFS, "rules/checks.rules")
	if err != nil {
		return nil, err
	}
	if err := a.parseChecks(string(checksRaw)); err != nil {
		return nil, err
	}
	return a, nil
}

func dataLines(content string) []string {
	var out []string
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimRight(raw, "\r")
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		out = append(out, t)
	}
	return out
}

func (a *Advisor) parseAdvice(content string) error {
	for i, line := range dataLines(content) {
		cols := tabRe.Split(line, 4)
		if len(cols) != 4 {
			return fmt.Errorf("advice.rules:%d: expected 4 columns, got %d", i+1, len(cols))
		}
		n, err := strconv.Atoi(cols[1])
		if err != nil {
			return fmt.Errorf("advice.rules:%d: bad min_count: %w", i+1, err)
		}
		a.counts = append(a.counts, countAdvice{cols[0], n, cols[2], cols[3]})
	}
	return nil
}

func (a *Advisor) parseChecks(content string) error {
	for i, line := range dataLines(content) {
		cols := tabRe.Split(line, 4)
		if len(cols) != 4 {
			return fmt.Errorf("checks.rules:%d: expected 4 columns, got %d", i+1, len(cols))
		}
		re, err := regexp.Compile(cols[1])
		if err != nil {
			return fmt.Errorf("checks.rules:%d: bad regexp: %w", i+1, err)
		}
		a.checks = append(a.checks, bootCheck{cols[0], re, cols[2], cols[3]})
	}
	return nil
}

// Evaluate returns advisories: category-volume ones whose dropped count reaches
// the threshold, plus boot checks whose pattern is present in bootText.
func (a *Advisor) Evaluate(droppedByCat map[string]int, bootText string) []Advisory {
	var out []Advisory
	for _, c := range a.counts {
		if n := droppedByCat[c.category]; n >= c.minCount {
			msg := strings.ReplaceAll(c.template, "{count}", strconv.Itoa(n))
			out = append(out, Advisory{ID: c.category, Message: msg, URL: c.url})
		}
	}
	for _, ch := range a.checks {
		if ch.re.MatchString(bootText) {
			out = append(out, Advisory{ID: ch.id, Message: ch.message, URL: ch.url})
		}
	}
	return out
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd tools && go test ./internal/errorlog -run TestAdvice -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add tools/internal/errorlog/advice.go tools/internal/errorlog/rules/advice.rules tools/internal/errorlog/rules/checks.rules tools/internal/errorlog/advice_test.go
git commit -m "feat(errorlog): category + boot-check advisories (TF 3226, IFI, SPN)"
```

---

### Task 6: Aggregation (`aggregate.go`)

**Files:**
- Create: `tools/internal/errorlog/aggregate.go`
- Test: `tools/internal/errorlog/aggregate_test.go`

**Interfaces:**
- Consumes: `[]Entry` (kept entries only).
- Produces:
  - `type Event struct { First, Last time.Time; Count int; Text string }`
  - `func Aggregate(kept []Entry) []Event` — collapses entries sharing a normalized signature into one `Event` (Count ≥ 2), preserving first-occurrence order; singletons keep their full text.

- [ ] **Step 1: Write the failing test**

```go
package errorlog

import "testing"

func TestAggregateCollapsesRepeats(t *testing.T) {
	text := ""
	for i := 0; i < 3; i++ {
		text += "2026-06-16 00:00:0" + string(rune('0'+i)) + ".00 Logon      Login failed for user 'PECHEUR\\SQL1$'. [CLIENT : 172.16.70.81]\n"
	}
	text += "2026-06-13 00:00:00.50 spid61     Erreur : 824, Gravité : 24.\n"
	events := Aggregate(Parse(text))
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[0].Count != 3 {
		t.Errorf("first event count = %d, want 3", events[0].Count)
	}
	if events[1].Count != 1 {
		t.Errorf("second event count = %d, want 1", events[1].Count)
	}
	if events[0].First.After(events[0].Last) {
		t.Error("First must be <= Last")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tools && go test ./internal/errorlog -run TestAggregate -v`
Expected: FAIL — `undefined: Aggregate`.

- [ ] **Step 3: Write the implementation**

```go
package errorlog

import (
	"regexp"
	"strings"
	"time"
)

var (
	reHex   = regexp.MustCompile(`0x[0-9A-Fa-f]+`)
	reIP    = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	reGUID  = regexp.MustCompile(`[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}`)
	reDigit = regexp.MustCompile(`\d+`)
	reSpid  = regexp.MustCompile(`\d+`)
)

// signature normalizes an entry so that repeated events with varying numbers,
// hex, GUIDs and IPs collapse together. Source keeps its shape minus the spid
// number (spid51 -> spid).
func signature(e Entry) string {
	s := e.Message()
	s = reGUID.ReplaceAllString(s, "#guid")
	s = reHex.ReplaceAllString(s, "0x#")
	s = reIP.ReplaceAllString(s, "#.#.#.#")
	s = reDigit.ReplaceAllString(s, "#")
	src := reSpid.ReplaceAllString(e.Source, "")
	return src + "|" + s
}

// Aggregate collapses kept entries that share a signature into a single Event
// with a count and time span; unique entries keep their full text. Order
// follows first occurrence.
func Aggregate(kept []Entry) []Event {
	type acc struct {
		idx     int
		first   Entry
		count   int
		firstTs time.Time
		lastTs  time.Time
	}
	groups := map[string]*acc{}
	var order []string
	for _, e := range kept {
		key := signature(e)
		g, ok := groups[key]
		if !ok {
			g = &acc{idx: len(order), first: e, firstTs: e.Time, lastTs: e.Time}
			groups[key] = g
			order = append(order, key)
		}
		g.count++
		if !e.Time.IsZero() {
			if g.firstTs.IsZero() || e.Time.Before(g.firstTs) {
				g.firstTs = e.Time
			}
			if e.Time.After(g.lastTs) {
				g.lastTs = e.Time
			}
		}
	}
	events := make([]Event, 0, len(order))
	for _, key := range order {
		g := groups[key]
		text := g.first.Text()
		if g.count > 1 {
			text = firstLine(g.first.Text())
		}
		events = append(events, Event{First: g.firstTs, Last: g.lastTs, Count: g.count, Text: text})
	}
	return events
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tools && go test ./internal/errorlog -run TestAggregate -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/internal/errorlog/aggregate.go tools/internal/errorlog/aggregate_test.go
git commit -m "feat(errorlog): aggregate repeated events with counts and time spans"
```

---

### Task 7: Redaction (`redact.go`)

**Files:**
- Create: `tools/internal/errorlog/redact.go`
- Test: `tools/internal/errorlog/redact_test.go`

**Interfaces:**
- Consumes: arbitrary output strings.
- Produces:
  - `type Redactor struct { ... }`
  - `func NewRedactor() *Redactor`
  - `func (r *Redactor) Scan(s string)` — register entities (databases, logins, IPs) found in `s`.
  - `func (r *Redactor) Apply(s string) string` — replace registered entities with stable tokens.
  - `func (r *Redactor) Legend() []string` — sorted `TOKEN = value` lines.

- [ ] **Step 1: Write the failing test**

```go
package errorlog

import (
	"strings"
	"testing"
)

func TestRedactConsistentTokens(t *testing.T) {
	r := NewRedactor()
	lines := []string{
		"Log was backed up. Database: sales.",
		"Login failed for user 'ACME\\svc'. [CLIENT : 10.0.0.5]",
		"Log was backed up. Database: sales.",
	}
	for _, l := range lines {
		r.Scan(l)
	}
	out0 := r.Apply(lines[0])
	out2 := r.Apply(lines[2])
	if out0 != out2 {
		t.Errorf("same DB must map identically: %q vs %q", out0, out2)
	}
	if strings.Contains(out0, "sales") {
		t.Errorf("db name not redacted: %q", out0)
	}
	out1 := r.Apply(lines[1])
	if strings.Contains(out1, "ACME\\svc") || strings.Contains(out1, "10.0.0.5") {
		t.Errorf("login/IP not redacted: %q", out1)
	}
	if len(r.Legend()) == 0 {
		t.Error("legend empty")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tools && go test ./internal/errorlog -run TestRedact -v`
Expected: FAIL — `undefined: NewRedactor`.

- [ ] **Step 3: Write the implementation**

```go
package errorlog

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	reDB    = regexp.MustCompile(`(?i)(?:Database|base de données)[\s\x{00a0}]*:?[\s\x{00a0}]*'?([A-Za-z0-9_]+)'?`)
	reLogin = regexp.MustCompile(`(?i)(?:user|utilisateur|login)[\s\x{00a0}]+'([^']+)'`)
	reIP2   = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
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

// Scan finds entities in s and assigns them stable tokens.
func (r *Redactor) Scan(s string) {
	for _, m := range reDB.FindAllStringSubmatch(s, -1) {
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
func (r *Redactor) Apply(s string) string {
	vals := make([]string, len(r.order))
	copy(vals, r.order)
	sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	for _, v := range vals {
		s = strings.ReplaceAll(s, v, r.tokens[v])
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tools && go test ./internal/errorlog -run TestRedact -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/internal/errorlog/redact.go tools/internal/errorlog/redact_test.go
git commit -m "feat(errorlog): consistent pseudonymization with legend"
```

---

### Task 8: Rendering (`render.go`)

**Files:**
- Create: `tools/internal/errorlog/render.go`
- Test: `tools/internal/errorlog/render_test.go`

**Interfaces:**
- Consumes: `Report`, `InstanceSummary`, `Advisory`, `Event`.
- Produces:
  - `type RenderOptions struct { Format string; ShowSummary bool; Legend []string }`
  - `func Render(r Report, opts RenderOptions) string` — `Format` is `"text"` (default) or `"md"`.

- [ ] **Step 1: Write the failing test**

```go
package errorlog

import (
	"strings"
	"testing"
	"time"
)

func sampleReport() Report {
	t0 := time.Date(2026, 6, 12, 15, 28, 0, 0, time.UTC)
	return Report{
		Summary: InstanceSummary{Product: "Microsoft SQL Server 2022 16.0.4255.1",
			Edition: "Standard Edition", RAMMB: 65535, AuthMode: "MIXED",
			Collation: "French_CI_AS", FirstTime: t0, LastTime: t0.Add(72 * time.Hour), StartTime: t0},
		Advisories:   []Advisory{{ID: "backup", Message: "105575 backups ... TF 3226", URL: "https://x"}},
		Total:        108713,
		Kept:         2,
		Dropped:      108711,
		DroppedByCat: map[string]int{"backup": 105575},
		Events: []Event{
			{First: t0, Last: t0.Add(2 * time.Hour), Count: 128, Text: "Login failed for user 'DB_1'"},
			{First: t0, Last: t0, Count: 1, Text: "2026-06-13 00:00:00 Erreur : 824"},
		},
	}
}

func TestRenderText(t *testing.T) {
	out := Render(sampleReport(), RenderOptions{Format: "text", ShowSummary: true})
	for _, want := range []string{"=== INSTANCE ===", "=== ADVISORIES ===", "=== COUNTS ===", "=== EVENTS ===", "[×128]", "TF 3226"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderMarkdown(t *testing.T) {
	out := Render(sampleReport(), RenderOptions{Format: "md", ShowSummary: true})
	if !strings.Contains(out, "## Instance") || !strings.Contains(out, "| ") {
		t.Errorf("markdown output malformed:\n%s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tools && go test ./internal/errorlog -run TestRender -v`
Expected: FAIL — `undefined: Render`.

- [ ] **Step 3: Write the implementation**

```go
package errorlog

import (
	"fmt"
	"sort"
	"strings"
)

// RenderOptions controls output formatting.
type RenderOptions struct {
	Format      string // "text" (default) or "md"
	ShowSummary bool
	Legend      []string // redaction legend lines; empty when redaction is off
}

// Render turns a Report into a compact text digest or a Markdown report.
func Render(r Report, opts RenderOptions) string {
	if opts.Format == "md" {
		return renderMarkdown(r, opts)
	}
	return renderText(r, opts)
}

func eventLine(e Event) string {
	const f = "2006-01-02 15:04"
	if e.Count > 1 {
		return fmt.Sprintf("[×%d] %s–%s  %s", e.Count, e.First.Format(f), e.Last.Format(f), firstLine(e.Text))
	}
	return e.Text
}

func sortedCats(m map[string]int) []string {
	cats := make([]string, 0, len(m))
	for c := range m {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	return cats
}

func renderText(r Report, opts RenderOptions) string {
	var b strings.Builder
	if opts.ShowSummary {
		s := r.Summary
		b.WriteString("=== INSTANCE ===\n")
		fmt.Fprintf(&b, "%s %s\n", s.Product, s.Edition)
		fmt.Fprintf(&b, "RAM %d MB · collation %s · auth %s · UTC %s\n", s.RAMMB, s.Collation, s.AuthMode, s.UTCAdjust)
		if !s.FirstTime.IsZero() {
			fmt.Fprintf(&b, "Window: %s\n", s.window())
		}
	}
	if len(r.Advisories) > 0 {
		b.WriteString("=== ADVISORIES ===\n")
		for _, a := range r.Advisories {
			fmt.Fprintf(&b, "[%s] %s\n   → %s\n", a.ID, a.Message, a.URL)
		}
	}
	b.WriteString("=== COUNTS ===\n")
	fmt.Fprintf(&b, "%d entries → %d kept, %d dropped\n", r.Total, r.Kept, r.Dropped)
	if len(r.DroppedByCat) > 0 {
		var parts []string
		for _, c := range sortedCats(r.DroppedByCat) {
			parts = append(parts, fmt.Sprintf("%s=%d", c, r.DroppedByCat[c]))
		}
		fmt.Fprintf(&b, "dropped: %s\n", strings.Join(parts, ", "))
	}
	b.WriteString("=== EVENTS ===\n")
	for _, e := range r.Events {
		b.WriteString(eventLine(e) + "\n")
	}
	if len(opts.Legend) > 0 {
		b.WriteString("=== REDACTION ===\n")
		b.WriteString(strings.Join(opts.Legend, "\n") + "\n")
	}
	return b.String()
}

func renderMarkdown(r Report, opts RenderOptions) string {
	var b strings.Builder
	if opts.ShowSummary {
		s := r.Summary
		b.WriteString("## Instance\n\n")
		b.WriteString("| Field | Value |\n| --- | --- |\n")
		fmt.Fprintf(&b, "| Product | %s |\n", s.Product)
		fmt.Fprintf(&b, "| Edition | %s |\n", s.Edition)
		fmt.Fprintf(&b, "| RAM (MB) | %d |\n", s.RAMMB)
		fmt.Fprintf(&b, "| Collation | %s |\n", s.Collation)
		fmt.Fprintf(&b, "| Auth mode | %s |\n", s.AuthMode)
		fmt.Fprintf(&b, "| UTC adjustment | %s |\n", s.UTCAdjust)
		fmt.Fprintf(&b, "| Service account | %s |\n", s.ServiceAcct)
		fmt.Fprintf(&b, "| TCP ports | %s |\n", strings.Join(s.TCPPorts, ", "))
		fmt.Fprintf(&b, "| SPN | %s |\n", s.SPN)
		fmt.Fprintf(&b, "| IFI | %s |\n", s.IFI)
		fmt.Fprintf(&b, "| AG listeners | %s |\n", strings.Join(s.AGListeners, ", "))
		if !s.FirstTime.IsZero() {
			fmt.Fprintf(&b, "| Log window | %s |\n", s.window())
		}
		b.WriteString("\n")
	}
	if len(r.Advisories) > 0 {
		b.WriteString("## Advisories\n\n")
		for _, a := range r.Advisories {
			fmt.Fprintf(&b, "- **%s** — %s ([ref](%s))\n", a.ID, a.Message, a.URL)
		}
		b.WriteString("\n")
	}
	b.WriteString("## Counts\n\n")
	fmt.Fprintf(&b, "%d entries → **%d kept**, %d dropped\n\n", r.Total, r.Kept, r.Dropped)
	if len(r.DroppedByCat) > 0 {
		b.WriteString("| Category | Dropped |\n| --- | --- |\n")
		for _, c := range sortedCats(r.DroppedByCat) {
			fmt.Fprintf(&b, "| %s | %d |\n", c, r.DroppedByCat[c])
		}
		b.WriteString("\n")
	}
	b.WriteString("## Events\n\n")
	for _, e := range r.Events {
		b.WriteString("- " + eventLine(e) + "\n")
	}
	if len(opts.Legend) > 0 {
		b.WriteString("\n## Redaction legend\n\n")
		for _, l := range opts.Legend {
			b.WriteString("- " + l + "\n")
		}
	}
	return b.String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tools && go test ./internal/errorlog -run TestRender -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/internal/errorlog/render.go tools/internal/errorlog/render_test.go
git commit -m "feat(errorlog): text and markdown renderers"
```

---

### Task 9: Input acquisition (`input.go`)

**Files:**
- Create: `tools/internal/errorlog/input.go`
- Test: `tools/internal/errorlog/input_test.go`

**Interfaces:**
- Consumes: a path or `"-"`.
- Produces:
  - `type Source struct { Name string; Data []byte }`
  - `func Acquire(path string) ([]Source, error)` — resolves stdin (`-`), a `.zip` (all `ERRORLOG*` entries, else all entries), a `.gz`, a plain file, or a directory (`ERRORLOG*` sorted). Returns raw (undecoded) bytes per source.

- [ ] **Step 1: Write the failing test**

```go
package errorlog

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquirePlainAndZip(t *testing.T) {
	dir := t.TempDir()

	plain := filepath.Join(dir, "ERRORLOG")
	if err := os.WriteFile(plain, encodeUTF16LE("hello\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcs, err := Acquire(plain)
	if err != nil || len(srcs) != 1 {
		t.Fatalf("Acquire plain: %v, n=%d", err, len(srcs))
	}
	if Decode(srcs[0].Data) != "hello\n" {
		t.Errorf("plain data = %q", Decode(srcs[0].Data))
	}

	zpath := filepath.Join(dir, "ERRORLOG.zip")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("ERRORLOG")
	w.Write(encodeUTF16LE("zipped\r\n"))
	zw.Close()
	os.WriteFile(zpath, buf.Bytes(), 0o644)

	srcs, err = Acquire(zpath)
	if err != nil || len(srcs) != 1 {
		t.Fatalf("Acquire zip: %v, n=%d", err, len(srcs))
	}
	if Decode(srcs[0].Data) != "zipped\n" {
		t.Errorf("zip data = %q", Decode(srcs[0].Data))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tools && go test ./internal/errorlog -run TestAcquire -v`
Expected: FAIL — `undefined: Acquire`.

- [ ] **Step 3: Write the implementation**

```go
package errorlog

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Source is one acquired ERRORLOG stream, still raw (undecoded) bytes.
type Source struct {
	Name string
	Data []byte
}

// Acquire resolves a path to one or more raw ERRORLOG byte streams. It handles
// stdin ("-"), .zip archives, .gz files, plain files, and directories of
// ERRORLOG* files.
func Acquire(path string) ([]Source, error) {
	if path == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, err
		}
		return []Source{{Name: "<stdin>", Data: data}}, nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return acquireDir(path)
	}

	switch strings.ToLower(filepath.Ext(path)) {
	case ".zip":
		return acquireZip(path)
	case ".gz":
		data, err := acquireGzip(path)
		if err != nil {
			return nil, err
		}
		return []Source{{Name: path, Data: data}}, nil
	default:
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return []Source{{Name: path, Data: data}}, nil
	}
}

func acquireDir(dir string) ([]Source, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "ERRORLOG*"))
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no ERRORLOG* files in %s", dir)
	}
	sort.Strings(matches)
	var srcs []Source
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			return nil, err
		}
		srcs = append(srcs, Source{Name: m, Data: data})
	}
	return srcs, nil
}

func acquireZip(path string) ([]Source, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	pick := func(prefixOnly bool) []Source {
		var srcs []Source
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			if prefixOnly && !strings.Contains(strings.ToUpper(filepath.Base(f.Name)), "ERRORLOG") {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				continue
			}
			data, _ := io.ReadAll(rc)
			rc.Close()
			srcs = append(srcs, Source{Name: f.Name, Data: data})
		}
		return srcs
	}

	srcs := pick(true)
	if len(srcs) == 0 {
		srcs = pick(false) // fall back to all entries
	}
	if len(srcs) == 0 {
		return nil, fmt.Errorf("zip %s has no readable entries", path)
	}
	sort.Slice(srcs, func(i, j int) bool { return srcs[i].Name < srcs[j].Name })
	return srcs, nil
}

func acquireGzip(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gr.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, gr); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tools && go test ./internal/errorlog -run TestAcquire -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/internal/errorlog/input.go tools/internal/errorlog/input_test.go
git commit -m "feat(errorlog): input acquisition (zip/gz/plain/dir/stdin)"
```

---

### Task 10: CLI wiring (`main.go`, replaces v1)

**Files:**
- Modify (full rewrite): `tools/cmd/errorlog-parse/main.go`
- Test: `tools/internal/errorlog/pipeline_test.go` (tests the exported `Run` pipeline func)

**Interfaces:**
- Consumes: everything above.
- Produces (in package `errorlog`, new file `pipeline.go`):
  - `type Options struct { MinSeverity int; Format string; Redact, ShowLegend, Aggregate, ShowSummary bool; From, To time.Time; RulesDir string }`
  - `func Process(sources []Source, opts Options) (string, error)` — the full pipeline; returns the rendered digest.
- `main.go` only parses flags, calls `Acquire`, then `Process`, and prints.

- [ ] **Step 1: Write the failing test**

```go
package errorlog

import (
	"strings"
	"testing"
)

func TestProcessEndToEnd(t *testing.T) {
	raw := encodeUTF16LE(
		"2026-06-12 15:28:18.32 Server     Microsoft SQL Server 2022 (RTM-CU25) - 16.0.4255.1 (X64)\r\n" +
			"2026-06-12 15:28:18.34 Server     Authentication mode is MIXED.\r\n" +
			"2026-06-12 15:28:18.38 Server     Detected 65535 MB of RAM.\r\n" +
			"2026-06-12 15:28:18.66 Server     Default collation: French_CI_AS\r\n" +
			"2026-06-12 15:28:19.86 Server     Database Instant File Initialization: désactivé.\r\n" +
			backups(600) +
			"2026-06-16 00:00:01.22 Logon      Login failed for user 'ACME\\SQL1$'. [CLIENT : 10.0.0.5]\r\n" +
			"2026-06-13 00:00:00.50 spid61     Erreur : 824, Gravité : 24, État : 2.\r\n")

	out, err := Process([]Source{{Name: "t", Data: raw}}, Options{
		MinSeverity: 16, Format: "text", Aggregate: true, ShowSummary: true,
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	for _, want := range []string{"French_CI_AS", "trace flag 3226", "Instant File Initialization", "Login failed", "Erreur", "backup="} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "Log was backed up") {
		t.Error("backup noise leaked into events")
	}
}

func backups(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("2026-06-15 02:30:54.84 Backup     Log was backed up. Database: sales.\r\n")
	}
	return b.String()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tools && go test ./internal/errorlog -run TestProcessEndToEnd -v`
Expected: FAIL — `undefined: Process`.

- [ ] **Step 3: Write `pipeline.go`**

```go
package errorlog

import "time"

// Options configures the full processing pipeline.
type Options struct {
	MinSeverity int
	Format      string // "text" | "md"
	Redact      bool
	ShowLegend  bool
	Aggregate   bool
	ShowSummary bool
	From, To    time.Time
	RulesDir    string
}

// Process runs the pipeline over the acquired sources and returns the rendered
// digest.
func Process(sources []Source, opts Options) (string, error) {
	rs, err := LoadEmbeddedRules()
	if err != nil {
		return "", err
	}
	if opts.RulesDir != "" {
		if err := rs.LoadDir(opts.RulesDir); err != nil {
			return "", err
		}
	}
	advisor, err := LoadEmbeddedAdvisories()
	if err != nil {
		return "", err
	}

	var allEntries []Entry
	for _, src := range sources {
		allEntries = append(allEntries, Parse(Decode(src.Data))...)
	}

	report := Report{DroppedByCat: map[string]int{}}
	report.Summary = Summarize(allEntries)

	var kept []Entry
	for _, e := range allEntries {
		report.Total++
		if !inWindow(e, opts.From, opts.To) {
			continue
		}
		keep, cat := rs.Classify(e, opts.MinSeverity)
		if keep {
			kept = append(kept, e)
		} else {
			report.DroppedByCat[cat]++
		}
	}
	report.Kept = len(kept)
	report.Dropped = report.Total - report.Kept

	if opts.Aggregate {
		report.Events = Aggregate(kept)
	} else {
		report.Events = make([]Event, 0, len(kept))
		for _, e := range kept {
			report.Events = append(report.Events, Event{First: e.Time, Last: e.Time, Count: 1, Text: e.Text()})
		}
	}

	report.Advisories = advisor.Evaluate(report.DroppedByCat, BootText(allEntries, 400))

	var legend []string
	if opts.Redact {
		red := NewRedactor()
		for i := range report.Events {
			red.Scan(report.Events[i].Text)
		}
		red.Scan(report.Summary.ServiceAcct)
		for i := range report.Events {
			report.Events[i].Text = red.Apply(report.Events[i].Text)
		}
		report.Summary.ServiceAcct = red.Apply(report.Summary.ServiceAcct)
		if opts.ShowLegend {
			legend = red.Legend()
		}
	}

	return Render(report, RenderOptions{
		Format:      opts.Format,
		ShowSummary: opts.ShowSummary,
		Legend:      legend,
	}), nil
}

// inWindow reports whether an entry's timestamp is within [From, To]. Zero
// bounds are open; entries with an unparseable timestamp are always included
// (they are usually boot/continuation context).
func inWindow(e Entry, from, to time.Time) bool {
	if e.Time.IsZero() {
		return true
	}
	if !from.IsZero() && e.Time.Before(from) {
		return false
	}
	if !to.IsZero() && e.Time.After(to) {
		return false
	}
	return true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tools && go test ./internal/errorlog -run TestProcessEndToEnd -v`
Expected: PASS.

- [ ] **Step 5: Rewrite `main.go`**

```go
// Command errorlog-parse reads one or more SQL Server ERRORLOG files and writes
// a compact digest to stdout, dropping routine noise so an AI skill spends
// tokens only on entries that matter.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/errorlog"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "errorlog-parse:", err)
		os.Exit(1)
	}
}

func run() error {
	sev := flag.Int("severity", 16, "keep entries with Severity >= n")
	format := flag.String("format", "text", "output format: text or md")
	redact := flag.Bool("redact", false, "pseudonymize databases, logins and IPs")
	noLegend := flag.Bool("no-legend", false, "with -redact, hide the mapping legend")
	noAggregate := flag.Bool("no-aggregate", false, "do not collapse repeated entries")
	noSummary := flag.Bool("no-summary", false, "omit the instance summary")
	rulesDir := flag.String("rules", "", "extra directory of *.rules packs")
	fromStr := flag.String("from", "", "keep entries >= datetime (2006-01-02 or RFC3339)")
	toStr := flag.String("to", "", "keep entries <= datetime")
	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()
		return fmt.Errorf("expected exactly one <path> argument")
	}

	from, err := parseWhen(*fromStr)
	if err != nil {
		return fmt.Errorf("bad -from: %w", err)
	}
	to, err := parseWhen(*toStr)
	if err != nil {
		return fmt.Errorf("bad -to: %w", err)
	}

	sources, err := errorlog.Acquire(flag.Arg(0))
	if err != nil {
		return err
	}

	out, err := errorlog.Process(sources, errorlog.Options{
		MinSeverity: *sev,
		Format:      *format,
		Redact:      *redact,
		ShowLegend:  *redact && !*noLegend,
		Aggregate:   !*noAggregate,
		ShowSummary: !*noSummary,
		From:        from,
		To:          to,
		RulesDir:    *rulesDir,
	})
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

// parseWhen accepts an empty string (zero time), a date, or an RFC3339-ish
// datetime.
func parseWhen(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{"2006-01-02", "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized datetime %q", s)
}
```

- [ ] **Step 6: Verify the build and full package tests**

Run: `cd tools && go build ./... && go test ./... && go vet ./...`
Expected: build OK, all tests PASS, vet clean.

- [ ] **Step 7: Commit**

```bash
git add tools/internal/errorlog/pipeline.go tools/internal/errorlog/pipeline_test.go tools/cmd/errorlog-parse/main.go
git commit -m "feat(errorlog): pipeline + CLI flags (replaces v1 main)"
```

---

### Task 11: Golden end-to-end fixture

**Files:**
- Create: `tools/internal/errorlog/testdata/ERRORLOG_synthetic.txt` (UTF-8 source used to build a UTF-16 fixture in the test; **synthetic data only**)
- Create: `tools/internal/errorlog/golden_test.go`
- Create: `tools/internal/errorlog/testdata/expected_text.txt`

**Interfaces:**
- Consumes: `Process`.
- Produces: a regression test comparing `Process` output to a committed golden file.

- [ ] **Step 1: Create the synthetic source fixture**

Create `tools/internal/errorlog/testdata/ERRORLOG_synthetic.txt` with a realistic mix (fake names only): boot lines (version, auth, RAM, collation, IFI désactivé), ~600 `Log was backed up. Database: demo_sales.` lines, a burst of 3 identical `Login failed` lines from one IP, one French `Erreur : 824, Gravité : 24` with a continuation detail line, and a couple of unknown informational lines. Use NBSP (` `) before colons in the French lines.

- [ ] **Step 2: Write the golden test**

```go
package errorlog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGoldenDigest(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "ERRORLOG_synthetic.txt"))
	if err != nil {
		t.Fatal(err)
	}
	raw := encodeUTF16LE(string(src)) // exercise the UTF-16 decode path

	got, err := Process([]Source{{Name: "synthetic", Data: raw}}, Options{
		MinSeverity: 16, Format: "text", Aggregate: true, ShowSummary: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	goldenPath := filepath.Join("testdata", "expected_text.txt")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("golden mismatch. Re-run with UPDATE_GOLDEN=1 to refresh if intended.\n--- got ---\n%s", got)
	}
}
```

- [ ] **Step 3: Generate the golden file**

Run: `cd tools && UPDATE_GOLDEN=1 go test ./internal/errorlog -run TestGoldenDigest`
Then open `tools/internal/errorlog/testdata/expected_text.txt` and **manually verify** it: boot summary present, one backup advisory (TF 3226), one ifi-off advisory, the login burst aggregated as `[×3]`, the `Erreur : 824` kept, and no `Log was backed up` line in EVENTS.

- [ ] **Step 4: Run the test to verify it passes without the env var**

Run: `cd tools && go test ./internal/errorlog -run TestGoldenDigest -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/internal/errorlog/testdata/ tools/internal/errorlog/golden_test.go
git commit -m "test(errorlog): golden end-to-end digest on synthetic UTF-16 fixture"
```

---

### Task 12: Quality gate — lint, build script, code review

**Files:**
- Create: `tools/.golangci.yml`
- Modify: `scripts/build-tools.ps1` (unchanged mapping still builds `errorlog-parse`; verify it runs the new tests)
- Verify: `plugins/sqlserver-toolkit/skills/errorlog-diagnostics/SKILL.md` still references `errorlog-parse` and the new flags/output.

**Interfaces:** none (process task).

- [ ] **Step 1: Add golangci-lint config**

```yaml
# tools/.golangci.yml
run:
  timeout: 3m
linters:
  enable:
    - govet
    - errcheck
    - staticcheck
    - ineffassign
    - unused
    - gofmt
    - revive
```

- [ ] **Step 2: Run the linter**

Run: `cd tools && golangci-lint run ./...`
Expected: no issues. Fix any reported. (If `golangci-lint` is not installed: `go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest`.)

- [ ] **Step 3: Rebuild the binary into the plugin and smoke-test the real sample**

Run:
```powershell
./scripts/build-tools.ps1
# smoke test against the real 83 MB sample (unzip temp/ERRORLOG.zip first if needed)
```
Then run the built binary on the real ERRORLOG and eyeball the digest: instance summary correct, backup advisory shows the true count, login-failed burst aggregated, no backup lines in EVENTS.

- [ ] **Step 4: Apply relevant golang-skills review**

Invoke the `golang-skills:go-code-review` skill over the new package and address findings (naming, error handling, docs, testing idioms).

- [ ] **Step 5: Run `/code-review` at effort xhigh**

Run `/code-review high` (or `ultra`) on the branch diff. Triage and fix all confirmed correctness findings before merge, per the spec's quality gate.

- [ ] **Step 6: Final verification and commit**

Run: `cd tools && go test ./... && go vet ./... && golangci-lint run ./...`
Expected: all green.

```bash
git add tools/.golangci.yml
git commit -m "chore(errorlog): golangci-lint config and quality gate"
```

---

## Self-Review

**Spec coverage** (each spec section → task):
- Decode UTF-16/BOM/CRLF → Task 1. Parse/continuations → Task 2. Rule engine + `.rules` packs + severity/signal/noise/unknown precedence → Task 3. Instance summary (compact + detailed fields, localized values) → Task 4 (+ render Task 8). Advisories: category-count (`advice.rules`, TF 3226) + boot checks (`checks.rules`, IFI/SPN) → Task 5. Aggregation → Task 6. Redaction (opt-in, consistent, legend) → Task 7. Text/Markdown output → Task 8. Input zip/gz/plain/dir/stdin → Task 9. CLI flags (`--from/--to/--severity/--format/--redact/--no-legend/--no-aggregate/--lang→--rules/--no-summary`) → Task 10. Golden test with synthetic UTF-16 fixture → Task 11. Go rules / golangci-lint / `/code-review` xhigh → Task 12.
- **Gap noted:** the spec's `--lang <codes>` flag (restrict packs) is **not** implemented (all packs always apply). This is acceptable for v2 (mixed logs need all packs) and is consistent with §11 "auto-detection out of scope"; `--rules` covers extension. If `--lang` is required, add a filter in `LoadEmbeddedRules`. Left out intentionally to honor KISS.

**Placeholder scan:** No TBD/TODO; every code step has complete code. The `var _ = time.Time{}` guard in Task 4 is annotated for removal once `time` is used (it is, via struct fields) — delete it during implementation.

**Type consistency:** `Entry`, `Event`, `Advisory`, `InstanceSummary`, `Report`, `RuleSet`, `Advisor`, `Redactor`, `Source`, `Options`, `RenderOptions` are each defined once and consumed with matching signatures. `firstLine` is defined in Task 6 and reused in Task 8 (same package). `tabRe` defined in Task 3, reused in Task 5 (same package). `encodeUTF16LE` test helper defined in Task 1, reused in Tasks 9–11.

**Note for implementer:** `firstLine` (Task 6) and `tabRe` (Task 3) live in the same package, so they are shared, not redefined. Do not redeclare them.
