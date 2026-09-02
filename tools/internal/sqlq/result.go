package sqlq

import "encoding/json"

// Column describes one column of a result set.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// SQLError is what SQL Server said went wrong. Unlike sqlcmd's exit code and
// loose text, this keeps the fields that let you find the fault: the error
// number, and the line and procedure it fired from.
type SQLError struct {
	Number    int32  `json:"number"`
	Severity  uint8  `json:"severity"`
	State     uint8  `json:"state"`
	Line      int32  `json:"line"`
	Procedure string `json:"procedure"`
	Message   string `json:"message"`
}

// Result is the single JSON object sqlq writes to stdout, on success and on
// failure alike, so a caller never has two shapes to parse.
type Result struct {
	Profile   string    `json:"profile"`
	Server    string    `json:"server"`
	Database  string    `json:"database"`
	ElapsedMS int64     `json:"elapsed_ms"`
	Columns   []Column  `json:"columns"`
	Rows      []Row     `json:"rows"`
	RowCount  int       `json:"rowcount"`
	Truncated bool      `json:"truncated"`
	Messages  []string  `json:"messages"`
	Plan      *string   `json:"plan"`
	Error     *SQLError `json:"error"`
}

// Row is one result row, keyed by column name.
type Row = map[string]any

// MarshalJSON guarantees the slice fields are arrays rather than null, so the
// skill reading this never has to special-case an absent collection.
func (r Result) MarshalJSON() ([]byte, error) {
	type alias Result // avoid recursing into this method
	out := alias(r)
	if out.Columns == nil {
		out.Columns = []Column{}
	}
	if out.Rows == nil {
		out.Rows = []Row{}
	}
	if out.Messages == nil {
		out.Messages = []string{}
	}
	return json.Marshal(out)
}

// RowSet accumulates rows up to a cap while still counting everything the
// server sent, so a truncated answer still reports the true size.
type RowSet struct {
	MaxRows   int
	Rows      []Row
	RowCount  int
	Truncated bool
}

// NewRowSet returns a RowSet keeping at most maxRows rows. A maxRows of 0 or
// less keeps everything.
func NewRowSet(maxRows int) *RowSet {
	return &RowSet{MaxRows: maxRows, Rows: []Row{}}
}

// Add records a row, keeping it only if the cap allows.
func (rs *RowSet) Add(row Row) {
	rs.RowCount++
	if rs.MaxRows > 0 && len(rs.Rows) >= rs.MaxRows {
		rs.Truncated = true
		return
	}
	rs.Rows = append(rs.Rows, row)
}
