package sqlq

import (
	"reflect"
	"strings"
	"testing"
)

func TestBlockHeaderSummaryIsFirstParagraph(t *testing.T) {
	h, err := ParseBlockHeader("\n/*  Missing indexes in the current database, beside the\n    indexes that already exist.\n\n    No parameter. Read-only.\n*/\nSELECT 1;")
	if err != nil {
		t.Fatal(err)
	}
	if h.Summary != "Missing indexes in the current database, beside the indexes that already exist." {
		t.Errorf("summary = %q", h.Summary)
	}
	if h.HasParamsLine {
		t.Errorf("'No parameter.' is not a Parameters line")
	}
}

func TestParametersLineIsOptionalButParsed(t *testing.T) {
	h, _ := ParseBlockHeader("/* S.\n\n    Parameter: @name  - the object.\n    Heavy: yes\n*/\nSELECT 1;")
	if !h.HasParamsLine || !reflect.DeepEqual(h.Params, []string{"name"}) || !h.Heavy {
		t.Errorf("got %+v", h)
	}
	h, _ = ParseBlockHeader("/* S.\n\n    Parameters: none.\n*/\nSELECT 1;")
	if !h.HasParamsLine || len(h.Params) != 0 {
		t.Errorf("Parameters: none. -> %+v", h)
	}
	if _, err := ParseBlockHeader("SELECT 1; /* late */"); err == nil {
		t.Errorf("a file not opening with a comment block must be refused")
	}
}

const markedTemplate = `-----------------------------------------------------------------
-- Get sessions from a specific host
-- sqlq: name=sessions-from-host params=hostname heavy
--
-- rudi@babaluga.com, go ahead license
-----------------------------------------------------------------

DECLARE @hostname sysname = N'%';
SELECT 1;
`

func TestMarkedHeaderParses(t *testing.T) {
	h, found, err := ParseMarkedHeader(markedTemplate)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	want := MarkedHeader{Summary: "Get sessions from a specific host",
		Marker: Marker{Name: "sessions-from-host", Params: []string{"hostname"}, Heavy: true, Line: 3}}
	if !reflect.DeepEqual(h, want) {
		t.Errorf("got %+v, want %+v", h, want)
	}
}

func TestUnmarkedScriptIsAbsent(t *testing.T) {
	_, found, err := ParseMarkedHeader("-- just a script\nSELECT 1;")
	if found || err != nil {
		t.Errorf("found=%v err=%v; a file without marker must simply be absent", found, err)
	}
}

func TestMarkerAttemptIsCaseAndSpaceInsensitive(t *testing.T) {
	_, found, err := ParseMarkedHeader("-- Summary line\n--SQLQ: name=x-y\nSELECT 1;")
	if !found || err != nil {
		t.Errorf("found=%v err=%v", found, err)
	}
}

func TestMarkerOutsideHeaderIsRejected(t *testing.T) {
	_, found, err := ParseMarkedHeader("SET NOCOUNT ON;\n-- Summary\n-- sqlq: name=x-y\nSELECT 1;")
	if !found || err == nil || !strings.Contains(err.Error(), "outside the header") {
		t.Errorf("found=%v err=%v", found, err)
	}
	_, _, err = ParseMarkedHeader("-- S\n-- sqlq: name=a-b\n-- sqlq: name=c-d\nSELECT 1;")
	if err == nil {
		t.Errorf("two markers must be refused")
	}
}

func TestUnknownMarkerKeyIsRejected(t *testing.T) {
	_, _, err := ParseMarkedHeader("-- S\n-- sqlq: name=a-b haevy\nSELECT 1;")
	if err == nil || !strings.Contains(err.Error(), "unknown marker key at position 2") || strings.Contains(err.Error(), "haevy") {
		t.Errorf("err = %v", err)
	}
	_, _, err = ParseMarkedHeader("-- S\n-- sqlq: params=a\nSELECT 1;")
	if err == nil {
		t.Errorf("a marker without name must be refused")
	}
	_, _, err = ParseMarkedHeader("-- S\n-- sqlq: name=a-b params=hostname,HostName\nSELECT 1;")
	if err == nil {
		t.Errorf("a parameter listed twice must be refused")
	}
}

func TestSummaryIsNearestLineAboveMarker(t *testing.T) {
	src := "-- https://example.com/provenance\n-- Wait statistics\n--\n-- sqlq: name=w-s\n-- rudi@babaluga.com, go ahead license\nSELECT 1;"
	h, _, err := ParseMarkedHeader(src)
	if err != nil || h.Summary != "Wait statistics" {
		t.Errorf("summary = %q, err = %v", h.Summary, err)
	}
	_, _, err = ParseMarkedHeader("-- https://example.com\n-- sqlq: name=w-s\n-- licence\nSELECT 1;")
	if err == nil || !strings.Contains(err.Error(), "no summary") {
		t.Errorf("a URL is not a summary: err = %v", err)
	}
}

func TestBOMIsStripped(t *testing.T) {
	b := StripBOM([]byte("\xEF\xBB\xBF-- S\n-- sqlq: name=a-b\nSELECT 1;"))
	if _, found, err := ParseMarkedHeader(string(b)); !found || err != nil {
		t.Errorf("found=%v err=%v", found, err)
	}
}

func TestHeaderAcceptsCRLF(t *testing.T) {
	src := strings.ReplaceAll(markedTemplate, "\n", "\r\n")
	h, found, err := ParseMarkedHeader(src)
	if !found || err != nil || h.Summary != "Get sessions from a specific host" || h.Marker.Name != "sessions-from-host" {
		t.Errorf("CRLF: %+v found=%v err=%v", h, found, err)
	}
}

func TestMarkerAttemptWithoutColonOrASCIISpaceIsRejected(t *testing.T) {
	for _, src := range []string{
		"-- S\n-- sqlq name=x-y\nSELECT 1;",
		"-- S\n--\u00a0sqlq: name=x-y\nSELECT 1;",
		"-- S\n-- sqlq\u2003: name=x-y\nSELECT 1;",
	} {
		if _, found, err := ParseMarkedHeader(src); !found || err == nil {
			t.Errorf("%q: found=%v err=%v, want a rejected marker", src, found, err)
		}
	}
	for _, src := range []string{
		"-- S\nSELECT 'TSQLQuery -- sqlqx';",
		"-- sqlqueries are elsewhere\nSELECT 1;",
		"-- S\nSELECT 1; -- sqlq: name=x-y",
	} {
		if _, found, err := ParseMarkedHeader(src); found {
			t.Errorf("%q: found=%v err=%v, not a marker attempt", src, found, err)
		}
	}
}

func TestLoneCRSplitsHeaderLines(t *testing.T) {
	h, found, err := ParseMarkedHeader("-- Summary\r-- sqlq: name=a-b\rSELECT 1;\r")
	if !found || err != nil || h.Marker.Line != 2 || h.Summary != "Summary" {
		t.Errorf("lone CR: %+v found=%v err=%v", h, found, err)
	}
}

func TestHeavyLineMustSayYesOrNo(t *testing.T) {
	for line, heavy := range map[string]bool{"Heavy: yes": true, "  HEAVY :  Yes ": true, "Heavy: no": false} {
		h, err := ParseBlockHeader("/* S.\n\n" + line + "\n*/\nSELECT 1;")
		if err != nil || h.Heavy != heavy {
			t.Errorf("%q: heavy=%v err=%v", line, h.Heavy, err)
		}
	}
	for _, line := range []string{"Heavy: yes.", "Heavy: oui", "Heavy:"} {
		if _, err := ParseBlockHeader("/* S.\n\n" + line + "\n*/\nSELECT 1;"); err == nil {
			t.Errorf("%q accepted: a typo must not pass for its absence", line)
		}
	}
}

func TestMarkerReasonsDoNotQuoteTheAuthor(t *testing.T) {
	for src, want := range map[string]string{
		`-- S` + "\n" + `-- sqlq: name=bk C:\Users\rudi\acme\secret.xel` + "\nSELECT 1;": "unknown marker key at position 2",
		"-- S\n-- sqlq: name=a-b params=SRV-ACME-PROD01\nSELECT 1;":                      "invalid parameter name at position 1",
		"-- S\n-- sqlq: name=a-b params=ok,acme/prod\nSELECT 1;":                         "invalid parameter name at position 2",
	} {
		_, _, err := ParseMarkedHeader(src)
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", src, err, want)
		}
		if err != nil && (strings.Contains(err.Error(), "acme") || strings.Contains(err.Error(), "ACME")) {
			t.Errorf("reason quotes the author: %v", err)
		}
	}
}

func TestMarkerKeyGivenTwiceIsRejected(t *testing.T) {
	for _, line := range []string{"name=a name=b", "name=a params=x params=y"} {
		if _, err := parseMarker(line); err == nil {
			t.Errorf("parseMarker(%q) accepted", line)
		}
	}
}
