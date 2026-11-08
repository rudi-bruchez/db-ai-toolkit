package evaluate

import (
	"context"
	"strings"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
)

// Recorder is the fake connection of a recording session: it records every
// statement sent, in order, and answers from what the test wrote in advance.
// It is exported for the unit tests of evaluate and of the lab mode.
type Recorder struct {
	// Statements are the statements sent, Exec and Query alike.
	Statements []string
	// Plans maps a Call to the ShowPlanXML document it returns, under
	// SHOWPLAN_XML and AUTOPILOT alike. A Call without an entry returns no
	// row, which the session takes as ErrNotAPlan.
	Plans map[string]string
	// Results maps any other query (a Resolve query, the sys.stats read) to
	// its rows; a query without an entry returns no row.
	Results map[string][][]any
	// Fail maps a statement to the error it returns instead of running.
	Fail map[string]error
}

// Count returns how many statements sent contain sub.
func (r *Recorder) Count(sub string) int {
	n := 0
	for _, s := range r.Statements {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

// Exec records stmt.
func (r *Recorder) Exec(_ context.Context, stmt string) error {
	r.Statements = append(r.Statements, stmt)
	return r.Fail[stmt]
}

// Query records q and returns its plan or its rows.
func (r *Recorder) Query(_ context.Context, q string) ([][]any, error) {
	r.Statements = append(r.Statements, q)
	if err := r.Fail[q]; err != nil {
		return nil, err
	}
	if doc, ok := r.Plans[q]; ok {
		return [][]any{{doc}}, nil
	}
	return r.Results[q], nil
}

// NewRecordingSession returns a session whose statements go to a Recorder
// instead of a server, for database_id 5. It sends nothing at creation, and
// replacing its connection keeps the same Recorder.
func NewRecordingSession(inst conn.Instance) (*Session, *Recorder) {
	r := &Recorder{Plans: map[string]string{}, Results: map[string][][]any{}, Fail: map[string]error{}}
	s := &Session{
		ex:             r,
		reopen:         func(context.Context) (execer, error) { return r, nil },
		abandon:        func() {},
		dbID:           5,
		enterprise:     inst.IsEnterpriseFeatures,
		compileTimeout: time.Minute,
		lockTimeout:    2000,
	}
	return s, r
}
