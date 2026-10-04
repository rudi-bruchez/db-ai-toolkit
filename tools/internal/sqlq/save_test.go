package sqlq

import (
	"strings"
	"testing"
)

func TestSaveRefusesCommentBreakingSummary(t *testing.T) {
	for _, s := range []string{"a /* b", "a */ SELECT 1; /*", "line\nbreak", "cr\rhere", "", "   ",
		"nul\x00here", "sep\u2028here", "Heavy: yes", "Parameters: @x.", "bad\xffutf8"} {
		if _, err := SavedFileContent(s, "SELECT 1;"); err == nil {
			t.Errorf("summary %q accepted", s)
		}
	}
}

func TestSavedFileReparsesAsValidEntry(t *testing.T) {
	sqlText := "SELECT TOP (5) name FROM sys.objects WHERE name LIKE @pattern AND type = @Type;"
	got, err := SavedFileContent("Objects matching a pattern.", sqlText)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "\n"+sqlText) {
		t.Errorf("the executed bytes must follow the header unchanged:\n%s", got)
	}
	h, err := ParseBlockHeader(got)
	if err != nil || h.Summary != "Objects matching a pattern." || strings.Join(h.Params, ",") != "pattern,type" {
		t.Errorf("reparsed %+v, %v", h, err)
	}
	if strings.Join(QueryParams(got), ",") != "pattern,type" {
		t.Errorf("params of the saved file: %v", QueryParams(got))
	}
}
