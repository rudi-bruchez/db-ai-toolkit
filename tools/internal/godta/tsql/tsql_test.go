package tsql

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/templates.golden")

func TestQuoteName(t *testing.T) {
	for in, want := range map[string]string{
		"Order]s": "[Order]]s]",
		"Café":    "[Café]",
		"a[b":     "[a[b]",
	} {
		if got := QuoteName(in); got != want {
			t.Errorf("QuoteName(%q) = %q, want %q", in, got, want)
		}
	}
	// 128 characters is the limit, as for QUOTENAME; non-ASCII counts one each.
	if got := QuoteName(strings.Repeat("é", 128)); got != "["+strings.Repeat("é", 128)+"]" {
		t.Errorf("QuoteName of 128 characters: %q", got)
	}
	defer func() {
		if recover() == nil {
			t.Error("QuoteName of 129 characters did not panic")
		}
	}()
	QuoteName(strings.Repeat("x", 129))
}

func TestQuoteString(t *testing.T) {
	for in, want := range map[string]string{
		"a'b":  "N'a''b'",
		"Café": "N'Café'",
		"":     "N''",
	} {
		if got := QuoteString(in); got != want {
			t.Errorf("QuoteString(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLikePrefix(t *testing.T) {
	for in, want := range map[string]string{
		"godta_": "godta[_]%",
		"a[b%":   "a[[]b[%]%",
	} {
		if got := LikePrefix(in); got != want {
			t.Errorf("LikePrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

// goldenTable is the awkward name of point 1 of the plan's watch list.
var goldenTable = Obj{Schema: "dbo", Name: "Ord]er s"}

var goldenIndex = IndexDef{
	Table:           goldenTable,
	Name:            "godta_ab12cd34_1",
	Keys:            []KeyCol{{Name: "a"}, {Name: "b", Desc: true}},
	Include:         []string{"c"},
	Filter:          "a = 933",
	DataCompression: "PAGE",
	FillFactor:      90,
	OnTarget:        "[PRIMARY]",
}

// goldenMoves has enough files that a map iteration order coming out sorted
// by chance is unlikely, and TestTemplatesGolden renders it repeatedly.
var goldenMoves = map[string]string{
	"Café_log": "/var/opt/mssql/data/godta_ab12cd34_O'Brien_log.ldf",
	"Café":     "/var/opt/mssql/data/godta_ab12cd34_O'Brien.mdf",
	"Café_2":   "/var/opt/mssql/data/godta_ab12cd34_O'Brien_2.ndf",
	"Archive":  "/var/opt/mssql/data/godta_ab12cd34_O'Brien_a.ndf",
}

// goldenCases returns every template's output, in a fixed order.
func goldenCases() [][2]string {
	unique := goldenIndex
	unique.Unique = true
	unique.Filter = ""
	unique.Include = nil
	unique.DataCompression = ""
	unique.FillFactor = 0
	unique.OnTarget = ""
	backup := "/var/opt/mssql/backup/godta_ab12cd34_O'Brien.bak"
	cases := [][2]string{
		{"Obj.Quoted", goldenTable.Quoted()},
		{"CreateHypothetical", CreateHypothetical(goldenIndex)},
		{"CreateHypothetical unique", CreateHypothetical(unique)},
		{"CreateIndex", CreateIndex(goldenIndex)},
		{"CreateIndex unique without options", CreateIndex(unique)},
		{"CreateIndexDropExisting", CreateIndexDropExisting(goldenIndex)},
		{"CreateIndexDropExisting without options", CreateIndexDropExisting(unique)},
		{"DropIndex", DropIndex(goldenTable, "godta_ab12cd34_1")},
		{"CreateStatistics", CreateStatistics(goldenTable, "godta_ab12cd34_s'1", []string{"a", "x y"})},
		{"DropStatistics", DropStatistics(goldenTable, "godta_ab12cd34_s'1")},
		{"UpdateStatisticsStream", UpdateStatisticsStream(goldenTable, "godta_ab12cd34_1", []byte{0x01, 0xab, 0x00, 0xff})},
		{"ShowStatisticsStream", ShowStatisticsStream(goldenTable, "godta_ab12cd34_s'1")},
		{"AutopilotReset", AutopilotReset(14)},
		{"AutopilotIndex", AutopilotIndex(14, 975342539, 2, 3308)},
		{"AutopilotView", AutopilotView(14, 700581584)},
		{"SetLockTimeout", SetLockTimeout(5000)},
		{"CloneDatabase", CloneDatabase("Café]db", "godta_ab12cd34_Café]db")},
		{"BackupDatabase compressed", BackupDatabase("godta_ab12cd34_Café]db", backup, true)},
		{"BackupDatabase uncompressed", BackupDatabase("godta_ab12cd34_Café]db", backup, false)},
		{"ReadFile", ReadFile(backup)},
		{"DeleteBackupFile", DeleteBackupFile(backup)},
		{"DropDatabase", DropDatabase("godta_ab12cd34_Café]db")},
		{"RestoreFileList", RestoreFileList(backup)},
		{"RestoreDatabase", RestoreDatabase("godta_ab12cd34_Café]db", backup, goldenMoves)},
		{"RestoreDatabase without moves", RestoreDatabase("godta_ab12cd34_Café]db", backup, nil)},
		{"SetReadWrite", SetReadWrite("godta_ab12cd34_Café]db")},
		{"SetScopedConfig bit", SetScopedConfig(ScopedConfig{Name: "LEGACY_CARDINALITY_ESTIMATION", Value: "1", BaseType: "bit"})},
		{"SetScopedConfig int", SetScopedConfig(ScopedConfig{Name: "MAXDOP", Value: "2", BaseType: "int"})},
		{"SetScopedConfig nvarchar", SetScopedConfig(ScopedConfig{Name: "ELEVATE_ONLINE", Value: "WHEN_SUPPORTED", BaseType: "nvarchar"})},
		{"RunPattern", RunPattern()},
	}
	for i, s := range SetAutoStatsOff("godta_ab12cd34_Café]db") {
		cases = append(cases, [2]string{fmt.Sprintf("SetAutoStatsOff %d", i+1), s})
	}
	return cases
}

func TestTemplatesGolden(t *testing.T) {
	first := RestoreDatabase("d", "/b", goldenMoves)
	for range 20 {
		if s := RestoreDatabase("d", "/b", goldenMoves); s != first {
			t.Fatalf("RestoreDatabase is not deterministic:\n%s\n%s", first, s)
		}
	}
	var b strings.Builder
	for _, c := range goldenCases() {
		fmt.Fprintf(&b, "-- %s\n%s\n\n", c[0], c[1])
	}
	got := b.String()
	path := filepath.Join("testdata", "templates.golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update once, then review the file by hand)", err)
	}
	if got != string(want) {
		gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for i := 0; i < len(gl) || i < len(wl); i++ {
			var g, w string
			if i < len(gl) {
				g = gl[i]
			}
			if i < len(wl) {
				w = wl[i]
			}
			if g != w {
				t.Fatalf("templates differ from %s at line %d:\n got  %q\n want %q", path, i+1, g, w)
			}
		}
	}
}

func TestAutopilotTemplates(t *testing.T) {
	for got, want := range map[string]string{
		AutopilotReset(14):                  "DBCC AUTOPILOT(5, 14, 0, 0, 0, 0, 0)",
		AutopilotIndex(14, 975342539, 2, 0): "DBCC AUTOPILOT(0, 14, 975342539, 2, 0)",
		AutopilotView(14, 700581584):        "DBCC AUTOPILOT(6, 14, 700581584, 1, 0)",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestBackupCompression(t *testing.T) {
	if s := BackupDatabase("d", "/b/d.bak", false); strings.Contains(s, "COMPRESSION") {
		t.Errorf("uncompressed backup asks for compression (Express refuses it): %s", s)
	}
	if s := BackupDatabase("d", "/b/d.bak", true); !strings.Contains(s, "COMPRESSION") {
		t.Errorf("compressed backup lacks COMPRESSION: %s", s)
	}
}

func TestRunPattern(t *testing.T) {
	for s, want := range map[string]bool{
		"godta_ab12cd34_1":  true,
		"godta_t":           false,
		"godta_filt":        false,
		"godta_it_ab12cd34": false,
		"godtaXab12cd34_1":  false,
		"godta_ab12cd34":    false, // no separator after the run ID
		"godta_ab12cd34x1":  false, // a ninth character instead of _
		"godta_ab12cd34_":   true,  // LIKE's % matches the empty string
		"godta_AB12CD34_1":  false, // run IDs are lower case
		"godta_ab-2cd34_1":  false,
	} {
		if got := IsRunName(s); got != want {
			t.Errorf("IsRunName(%q) = %v, want %v", s, got, want)
		}
	}
	want := "godta[_][a-z0-9][a-z0-9][a-z0-9][a-z0-9][a-z0-9][a-z0-9][a-z0-9][a-z0-9][_]%"
	if got := RunPattern(); got != want {
		t.Errorf("RunPattern() = %q, want %q", got, want)
	}
}

func TestScopedConfigBit(t *testing.T) {
	for got, want := range map[string]string{
		SetScopedConfig(ScopedConfig{Name: "LEGACY_CARDINALITY_ESTIMATION", Value: "0", BaseType: "bit"}): "ALTER DATABASE SCOPED CONFIGURATION SET LEGACY_CARDINALITY_ESTIMATION = OFF",
		SetScopedConfig(ScopedConfig{Name: "MAXDOP", Value: "2", BaseType: "int"}):                        "ALTER DATABASE SCOPED CONFIGURATION SET MAXDOP = 2",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}
