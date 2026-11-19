package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

// exitFleetPartial is the multi-profile code for "at least one profile
// attempted failed, or the deadline was reached".
const exitFleetPartial = 5

// fleetResult is the one JSON object of a multi-profile run (spec section 5.5).
type fleetResult struct {
	Mode                  string         `json:"mode"`
	Selection             string         `json:"selection"`
	SelectionFingerprint  string         `json:"selection_fingerprint"`
	ProfilesSelected      int            `json:"profiles_selected"`
	ProfilesOK            int            `json:"profiles_ok"`
	ProfilesFailed        int            `json:"profiles_failed"`
	ProfilesSkipped       int            `json:"profiles_skipped"`
	ProfilesColumnsDiffer int            `json:"profiles_columns_differ"`
	ProfilesNoResultSet   int            `json:"profiles_no_result_set"`
	DeadlineReached       bool           `json:"deadline_reached"`
	ElapsedMS             int64          `json:"elapsed_ms"`
	Columns               []sqlq.Column  `json:"columns"`
	Rows                  []sqlq.Row     `json:"rows"`
	RowCount              int            `json:"rowcount"`
	Truncated             bool           `json:"truncated"`
	Profiles              []fleetProfile `json:"profiles"`
	Messages              []string       `json:"messages"`
	Warnings              []string       `json:"warnings"`
	Error                 *sqlq.SQLError `json:"error"`
}

// fleetProfile is what a single-profile run would have said, without its
// rows and columns when they went into the top-level rows.
type fleetProfile struct {
	Profile         string           `json:"profile"`
	Server          string           `json:"server"`
	Database        string           `json:"database"`
	ElapsedMS       int64            `json:"elapsed_ms"`
	ConnectAttempts int              `json:"connect_attempts"`
	Columns         *[]sqlq.Column   `json:"columns,omitempty"`
	Rows            *[]sqlq.Row      `json:"rows,omitempty"`
	RowCount        int              `json:"rowcount"`
	Truncated       bool             `json:"truncated"`
	Incomplete      bool             `json:"incomplete"`
	InRows          bool             `json:"in_rows"`
	ColumnsDiffer   bool             `json:"columns_differ"`
	MoreResults     []sqlq.ResultSet `json:"more_results"`
	Messages        []string         `json:"messages"`
	Warnings        []string         `json:"warnings"`
	Error           *sqlq.SQLError   `json:"error"`
	ExitCode        int              `json:"exit_code"`
}

// fleetMode says whether this invocation selects several profiles.
func fleetMode(o options) bool { return o.profilesMatch != "" || o.all }

// checkFleetFlags refuses, before anything is read, the combinations the
// multi-profile mode does not allow (spec sections 5.1 and 5.3).
func checkFleetFlags(o options) error {
	if o.profilesMatch != "" && o.all {
		return errors.New("give -profiles-match or -all, not both")
	}
	if o.profileName != "" {
		return errors.New("-profile selects one profile; -profiles-match and -all select several: give one of them")
	}
	if o.listProfiles {
		return nil
	}
	switch {
	case o.allowWrite:
		return errors.New("-allow-write is refused on several profiles: writing to a fleet in one call is not supported")
	case o.saveQuery != "":
		return errors.New("-save-query saves for one profile: use -profile")
	case o.wantPlan:
		return errors.New("-plan is refused on several profiles: run the profile whose plan you need with -profile")
	}
	return nil
}

// controlCharacter refuses a profile argument holding a control character:
// a name read from a CRLF file by a shell loop ends in CR, and would
// otherwise be reported as an unknown profile or an empty selection.
func controlCharacter(flagName, value string) error {
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("%s contains a control character (CR?): strip it", flagName)
	}
	return nil
}

// selectProfiles returns the names chosen by -profiles-match or -all, in
// name order, and the label of the selection.
func selectProfiles(profiles sqlq.Profiles, o options) ([]string, string, error) {
	if o.all {
		return profiles.Names(), "*all*", nil
	}
	if _, err := path.Match(o.profilesMatch, ""); err != nil {
		return nil, "", fmt.Errorf("-profiles-match %q is not a valid pattern: %v", o.profilesMatch, err)
	}
	var names []string
	for _, n := range profiles.Names() {
		if ok, _ := path.Match(o.profilesMatch, n); ok {
			names = append(names, n)
		}
	}
	return names, o.profilesMatch, nil
}

// fingerprint is the first 12 hexadecimal characters of the SHA-256 of the
// sorted name and environment of the chosen profiles: a yes given on a
// preview covers exactly the profiles it showed.
func fingerprint(profiles sqlq.Profiles, names []string) string {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	h := sha256.New()
	for _, n := range sorted {
		fmt.Fprintf(h, "%s\x00%s\n", n, profiles[n].Environment)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

func anyProd(profiles sqlq.Profiles, names []string) bool {
	for _, n := range names {
		if profiles[n].Environment == "prod" {
			return true
		}
	}
	return false
}

// listSelection is -list-profiles with -profiles-match or -all.
func listSelection(profiles sqlq.Profiles, o options) int {
	names, label, err := selectProfiles(profiles, o)
	if err != nil {
		return fail(exitUsage, err)
	}
	subset := sqlq.Profiles{}
	for _, n := range names {
		subset[n] = profiles[n]
	}
	return emit(map[string]any{
		"profiles":              describeProfiles(subset),
		"selection":             label,
		"selection_fingerprint": fingerprint(profiles, names),
	})
}

// fleetSaved resolves a -saved entry for a fleet: only an entry every chosen
// profile resolves to the same generic file runs (spec section 5.3).
func fleetSaved(o options, profiles sqlq.Profiles, names []string) (string, []any, string, []string, error) {
	cfg := catalogConfig(o, profiles.Names())
	cfg.Profile = ""
	var registryMsg string
	cfg.Registry, registryMsg = sqlq.LoadRegistry(registryPath())
	cat := sqlq.LoadCatalog(cfg)
	if err := needCanon(cat); err != nil {
		return "", nil, "", nil, err
	}
	e, ok := cat.Find(o.saved)
	if !ok {
		return "", nil, "", nil, missingQueryError(cat, cfg, o.saved)
	}
	for _, n := range names {
		pc := cfg
		pc.Profile = n
		pe, ok := sqlq.LoadCatalog(pc).Find(o.saved)
		if !ok || pe.Source != e.Source || pe.Path != e.Path || pe.Hash != e.Hash {
			return "", nil, "", nil, fmt.Errorf("query %q is saved for, or hidden in, some of the selected profiles: "+
				"only an entry common to them all runs on several profiles; move it to _generic, or run each profile with -profile", o.saved)
		}
	}
	text, named, _, err := prepareSaved(e, o.params)
	if err != nil {
		return "", nil, "", nil, err
	}
	msgs := append([]string(nil), cat.Messages...)
	if registryMsg != "" {
		msgs = append(msgs, registryMsg)
	}
	return text, named, e.Hash, msgs, nil
}

// worstOpen is the longest a connection can take to open with these options.
func worstOpen(o options) time.Duration {
	n := time.Duration(o.connectRetries + 1)
	return n*time.Duration(o.connectTimeoutSec)*time.Second + time.Duration(o.connectRetries)*retryDelay
}

func runFleet(o options, profiles sqlq.Profiles) int {
	started := time.Now()
	names, label, err := selectProfiles(profiles, o)
	if err != nil {
		return fail(exitUsage, err)
	}
	if len(names) == 0 {
		return fail(exitUsage, fmt.Errorf("no profile matches the selection (%d defined; use -list-profiles)", len(profiles)))
	}
	fp := fingerprint(profiles, names)
	switch {
	case o.selectionFingerprint != "" && o.selectionFingerprint != fp:
		return fail(exitUsage, errors.New("the selection changed since its preview (fingerprint differs): "+
			"run -list-profiles with the same selection again and ask again"))
	case o.selectionFingerprint == "" && anyProd(profiles, names):
		return fail(exitUsage, errors.New("the selection contains a prod profile: preview it with -list-profiles "+
			"and the same selection, get the user's yes, then pass -selection-fingerprint"))
	}

	var sqlText, entryHash string
	var named []any
	var catalogMsgs []string
	if o.saved != "" {
		if !sqlq.ValidQueryName(o.saved) {
			return fail(exitUsage, fmt.Errorf("query name %q must match ^[a-z][a-z0-9-]{1,48}$", o.saved))
		}
		sqlText, named, entryHash, catalogMsgs, err = fleetSaved(o, profiles, names)
	} else {
		sqlText, err = readQuery(o.query, o.file, "")
		if err == nil {
			named, err = namedArgs(o.params)
		}
	}
	if err != nil {
		return fail(exitUsage, err)
	}
	// -allow-write is refused here, so any write is refused whatever the modes.
	if code, err := guard(sqlText, sqlq.Profile{Name: label, Mode: sqlq.ModeReadOnly}, false); err != nil {
		return fail(code, err)
	}

	out := fleetResult{Mode: "multi", Selection: label, SelectionFingerprint: fp,
		ProfilesSelected: len(names), Messages: catalogMsgs}
	deadline := time.Duration(o.deadlineSec) * time.Second
	if worstOpen(o)+time.Duration(o.timeoutSec)*time.Second > deadline {
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"-timeout plus the worst connection opening (%g s) exceeds -deadline (%d s): a slow profile ends as deadline",
			(worstOpen(o)+time.Duration(o.timeoutSec)*time.Second).Seconds(), o.deadlineSec))
	}

	// Secrets first, one profile after the other: Resolver is not safe for
	// concurrent use, and each resolution gets its own warning sink.
	results := make([]sqlq.Result, len(names))
	codes := make([]int, len(names))
	attempt := make([]bool, len(names))
	secrets := make([]string, len(names))
	resolver := sqlq.NewResolver(os.Getenv, resolveCredentialsPath(), nil)
	for i, n := range names {
		p := profiles[n]
		if o.database != "" {
			p.Database = o.database
		}
		results[i] = sqlq.Result{Profile: n, Server: p.Server, Database: p.Database}
		if reason := unusableHere(p); reason != "" {
			results[i].Error = &sqlq.SQLError{Message: "profile cannot run here: " + reason, Category: catUsage}
			codes[i] = exitUsage
			continue
		}
		var warns []string
		resolver.Warn = func(m string) { warns = append(warns, m) }
		s, err := resolver.Resolve(p)
		results[i].Warnings = warns
		if err != nil {
			results[i].Error = &sqlq.SQLError{Message: err.Error(), Category: catUsage}
			codes[i] = exitUsage
			attempt[i] = true
			continue
		}
		secrets[i] = s
		attempt[i] = true
	}

	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	sem := make(chan struct{}, o.parallel)
	var wg sync.WaitGroup
	for i, n := range names {
		if !attempt[i] || codes[i] != 0 {
			continue
		}
		p := profiles[n]
		if o.database != "" {
			p.Database = o.database
		}
		wg.Add(1)
		go func(i int, p sqlq.Profile) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
			}
			if ctx.Err() != nil {
				results[i].Error = &sqlq.SQLError{Message: "not started: the -deadline was reached", Category: catDeadline}
				codes[i] = exitFleetPartial
				return
			}
			defer func() { <-sem }()
			secret := secrets[i]
			warns := results[i].Warnings
			res, code := execute(ctx, p, sqlText, named, o, func(sqlq.Profile) (string, error) { return secret, nil })
			res.Warnings = append(warns, res.Warnings...)
			results[i], codes[i] = res, code
		}(i, p)
	}
	wg.Wait()
	out.DeadlineReached = ctx.Err() != nil

	aggregate(&out, names, results, codes, attempt)
	if entryHash != "" {
		for i, n := range names {
			if codes[i] == exitOK {
				var r sqlq.Result
				recordRun(&r, entryHash, profiles[n])
				out.Messages = append(out.Messages, r.Messages...)
				break
			}
		}
	}
	out.ElapsedMS = time.Since(started).Milliseconds()
	_ = emit(out)
	attempted := 0
	for i := range names {
		if attempt[i] {
			attempted++
		}
	}
	switch {
	case attempted == 0:
		return exitUsage
	case out.ProfilesFailed > 0 || out.DeadlineReached:
		return exitFleetPartial
	}
	return exitOK
}

// aggregate builds the top level from the per-profile results (spec 5.5).
func aggregate(out *fleetResult, names []string, results []sqlq.Result, codes []int, attempt []bool) {
	schema := func(cols []sqlq.Column) string {
		var b strings.Builder
		for _, c := range cols {
			b.WriteString(c.Name + "\x00" + c.Type + "\x01")
		}
		return b.String()
	}
	// The reference schema is the one most successful profiles share; a tie
	// goes to the first in name order. A profile without any result set never
	// counts.
	count := map[string]int{}
	ref, best := "", 0
	for i := range names {
		if codes[i] == exitOK && len(results[i].Columns) > 0 {
			s := schema(results[i].Columns)
			count[s]++
			if count[s] > best {
				ref, best = s, count[s]
			}
		}
	}
	var refCols []sqlq.Column
	key := "profile"
	for i := range names {
		if refCols == nil && codes[i] == exitOK && len(results[i].Columns) > 0 && schema(results[i].Columns) == ref {
			refCols = results[i].Columns
		}
		for _, c := range results[i].Columns {
			if strings.EqualFold(c.Name, "profile") {
				key = "sqlq_profile"
			}
		}
	}
	out.Columns = append([]sqlq.Column{{Name: key, Type: "PROFILE"}}, refCols...)
	out.Rows = []sqlq.Row{}
	for i, n := range names {
		r := results[i]
		fp := fleetProfile{Profile: n, Server: r.Server, Database: r.Database, ElapsedMS: r.ElapsedMS,
			ConnectAttempts: r.ConnectAttempts, RowCount: r.RowCount, Truncated: r.Truncated,
			Incomplete: r.Incomplete, MoreResults: r.MoreResults, Messages: r.Messages,
			Warnings: r.Warnings, Error: r.Error, ExitCode: codes[i]}
		if fp.MoreResults == nil {
			fp.MoreResults = []sqlq.ResultSet{}
		}
		if fp.Messages == nil {
			fp.Messages = []string{}
		}
		if fp.Warnings == nil {
			fp.Warnings = []string{}
		}
		switch {
		case !attempt[i]:
			out.ProfilesSkipped++
		case codes[i] != exitOK:
			out.ProfilesFailed++
		default:
			out.ProfilesOK++
		}
		inRows := codes[i] == exitOK && len(r.Columns) > 0 && schema(r.Columns) == ref
		switch {
		case codes[i] == exitOK && len(r.Columns) == 0:
			out.ProfilesNoResultSet++
		case codes[i] == exitOK && !inRows:
			out.ProfilesColumnsDiffer++
			fp.ColumnsDiffer = true
		}
		if inRows {
			fp.InRows = true
			for _, row := range r.Rows {
				merged := make(sqlq.Row, len(row)+1)
				for k, v := range row {
					merged[k] = v
				}
				merged[key] = n
				out.Rows = append(out.Rows, merged)
			}
			out.RowCount += r.RowCount
			out.Truncated = out.Truncated || r.Truncated || r.Incomplete
		} else if len(r.Columns) > 0 || len(r.Rows) > 0 {
			cols, rows := r.Columns, r.Rows
			if rows == nil {
				rows = []sqlq.Row{}
			}
			fp.Columns, fp.Rows = &cols, &rows
		}
		out.Profiles = append(out.Profiles, fp)
	}
	if out.Messages == nil {
		out.Messages = []string{}
	}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	if out.Profiles == nil {
		out.Profiles = []fleetProfile{}
	}
}
