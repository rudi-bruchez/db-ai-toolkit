package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

// A resolver warning used to go to stderr, so a caller merging stderr into
// stdout (2>&1) got a first line that was not JSON. It must be in the JSON,
// and stderr must stay empty, also when the resolution then fails.
func TestResolverWarningIsInTheJSONNotOnStderr(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "credentials.json")
	body := fmt.Sprintf(`{
	  "managedBy": "registered-servers",
	  "source": { "path": %q, "sha256": "aaa", "sizeBytes": 1, "lastWriteTime": "2026-09-17T10:00:00.0000000+02:00" },
	  "credentials": { "g/s": { "blob": %q, "boundTo": { "server": "SRV01", "login": "svc" } } }
	}`, filepath.Join(dir, "gone", "RegSrvr17.xml"), base64.StdEncoding.EncodeToString([]byte("blob")))
	if err := os.WriteFile(store, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	profiles := filepath.Join(dir, "profiles.json")
	if err := os.WriteFile(profiles, []byte(`{"p": {"server": "SRV01", "database": "D", "auth": "sql",
	  "user": "svc", "passwordDpapi": "g/s", "mode": "readonly"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MSSQL_CREDENTIALS", store)

	stderr := captureStderr(t)
	out, code := captureRun(t, options{profileName: "p", profilesPath: profiles, query: "SELECT 1",
		maxRows: 50, timeoutSec: 30})
	if got := stderr(); got != "" {
		t.Errorf("stderr must stay empty, got %q", got)
	}
	var res sqlq.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, out)
	}
	if code != exitUsage || res.Error == nil {
		t.Fatalf("code %d, error %+v; want the resolution to fail with code 1", code, res.Error)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "is gone") {
		t.Errorf("warnings = %q; want the stale-source warning", res.Warnings)
	}
}

// warnings is always an array, as messages is, the failure shape of fail included.
func TestWarningsIsAnArrayEvenEmpty(t *testing.T) {
	for _, v := range []sqlq.Result{{}, {Error: &sqlq.SQLError{Message: "x"}}} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"warnings":[]`) {
			t.Errorf("warnings is not an empty array: %s", b)
		}
	}
}

// captureStderr redirects os.Stderr until the returned function is called,
// which restores it and returns what was written.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	done := false
	read := func() string {
		if done {
			return ""
		}
		done = true
		w.Close()
		os.Stderr = old
		b, _ := io.ReadAll(r)
		return string(b)
	}
	t.Cleanup(func() { read() })
	return read
}
