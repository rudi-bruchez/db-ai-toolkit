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
	if err == nil || !strings.Contains(err.Error(), `"haevy"`) {
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
