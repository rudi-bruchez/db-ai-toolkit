package sqlq

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDeclaredVariableIsOnlyTheDeclareTarget(t *testing.T) {
	got := QueryParams("DECLARE @local INT = @cutoff, @other INT = @Limit;\nSELECT @local, @other, @@ROWCOUNT, @Cutoff;")
	if want := []string{"cutoff", "limit"}; !reflect.DeepEqual(got, want) {
		t.Errorf("QueryParams = %v, want %v", got, want)
	}
	// A DECLARE without semicolon ends at the next statement keyword: the comma
	// of the SELECT list does not declare @b.
	got = QueryParams("DECLARE @a int = 1\nSELECT @a, @b")
	if want := []string{"b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("QueryParams = %v, want %v", got, want)
	}
	if got := QueryParams("SELECT 1 WHERE 'x' LIKE '%@@ROWCOUNT%' AND N'@notaparam' = N'';"); len(got) != 0 {
		t.Errorf("literals must not yield parameters: %v", got)
	}
	if got := QueryParams("DECLARE @a nvarchar(10) = LEFT(@src, @len);\nSELECT @a;"); strings.Join(got, ",") != "src,len" {
		t.Errorf("a comma inside parentheses must not declare: %v", got)
	}
}

// The header of every bundled query must name exactly the parameters its SQL
// references, when it has a Parameters line at all.
func TestDeclaredParametersMatchTheSQL(t *testing.T) {
	paths, _ := filepath.Glob(filepath.Join(bundledQueriesDir, "*.sql"))
	if len(paths) == 0 {
		t.Fatal("no bundled queries found")
	}
	for _, p := range paths {
		b, _ := os.ReadFile(p)
		h, err := ParseBlockHeader(string(StripBOM(b)))
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(p), err)
			continue
		}
		if !h.HasParamsLine {
			continue
		}
		got := QueryParams(string(b))
		if len(got) == 0 && len(h.Params) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, h.Params) {
			t.Errorf("%s: header says %v, SQL references %v", filepath.Base(p), h.Params, got)
		}
	}
}

func TestDirtyReadsDetectedOnTokens(t *testing.T) {
	yes := []string{
		"SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;",
		"SET TRANSACTION ISOLATION LEVEL READ\n    UNCOMMITTED;",
		"SELECT 1 FROM sys.objects WITH (READUNCOMMITTED);",
		"SELECT 1 FROM t WITH (nolock);",
	}
	for _, s := range yes {
		if !DirtyReads(s) {
			t.Errorf("DirtyReads(%q) = false", s)
		}
	}
	no := []string{
		"SELECT 1; -- READ UNCOMMITTED",
		"SELECT 'NOLOCK' AS hint;",
		"SET TRANSACTION ISOLATION LEVEL READ COMMITTED;",
	}
	for _, s := range no {
		if DirtyReads(s) {
			t.Errorf("DirtyReads(%q) = true", s)
		}
	}
}
