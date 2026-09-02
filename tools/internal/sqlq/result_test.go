package sqlq

import (
	"encoding/json"
	"testing"
)

func TestRowSetTruncatesButKeepsTheRealCount(t *testing.T) {
	rs := NewRowSet(2)
	for i := 0; i < 5; i++ {
		rs.Add(map[string]any{"n": i})
	}
	if len(rs.Rows) != 2 {
		t.Errorf("kept %d rows; want 2", len(rs.Rows))
	}
	if rs.RowCount != 5 {
		t.Errorf("RowCount = %d; the real count must survive truncation, want 5", rs.RowCount)
	}
	if !rs.Truncated {
		t.Error("Truncated should be true once rows were dropped")
	}
}

func TestRowSetUntruncated(t *testing.T) {
	rs := NewRowSet(10)
	rs.Add(map[string]any{"n": 1})
	if rs.Truncated {
		t.Error("Truncated should stay false when everything fits")
	}
	if rs.RowCount != 1 || len(rs.Rows) != 1 {
		t.Errorf("RowCount=%d rows=%d; want 1 and 1", rs.RowCount, len(rs.Rows))
	}
}

func TestRowSetUnlimitedWhenMaxRowsIsZero(t *testing.T) {
	rs := NewRowSet(0)
	for i := 0; i < 100; i++ {
		rs.Add(map[string]any{"n": i})
	}
	if len(rs.Rows) != 100 || rs.Truncated {
		t.Errorf("maxRows 0 means unlimited; got %d rows, truncated=%v", len(rs.Rows), rs.Truncated)
	}
}

func TestResultAlwaysMarshalsRowsAsAnArray(t *testing.T) {
	// A nil slice would serialise as null and force the skill to handle two
	// shapes. It must always be an array.
	raw, err := json.Marshal(Result{})
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"rows", "columns", "messages"} {
		if _, ok := back[field].([]any); !ok {
			t.Errorf("field %q marshalled as %#v; want an array", field, back[field])
		}
	}
}

func TestResultOmitsPlanAndErrorWhenAbsent(t *testing.T) {
	raw, _ := json.Marshal(Result{})
	var back map[string]any
	_ = json.Unmarshal(raw, &back)
	if back["error"] != nil {
		t.Errorf("error = %#v; want null on success", back["error"])
	}
	if back["plan"] != nil {
		t.Errorf("plan = %#v; want null when no plan was captured", back["plan"])
	}
}
