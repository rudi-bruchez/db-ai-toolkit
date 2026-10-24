package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const plan = `<ShowPlanXML xmlns="http://schemas.microsoft.com/sqlserver/2004/07/showplan"><BatchSequence/></ShowPlanXML>`

func TestRunWritesThePage(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "lookup.sqlplan")
	if err := os.WriteFile(in, []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "page.html")
	var stdout bytes.Buffer
	if err := run([]string{"-no-open", "-theme", "light", "-o", out, in}, nil, &stdout); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout.String()) != out {
		t.Errorf("printed %q, want the page path", stdout.String())
	}
	page, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(page, []byte(`"name":"lookup.sqlplan"`)) || !bytes.Contains(page, []byte(`data-theme="light"`)) {
		t.Error("page lacks the plan name or the theme")
	}
}

func TestRunReadsSqlqFromStdin(t *testing.T) {
	out := filepath.Join(t.TempDir(), "page.html")
	stdin := strings.NewReader(`{"rows":[],"plan":"<ShowPlanXML xmlns=\"x\"/>","error":null}`)
	if err := run([]string{"-no-open", "-o", out}, stdin, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	page, _ := os.ReadFile(out)
	if !bytes.Contains(page, []byte(`"name":"stdin"`)) {
		t.Error("page lacks the stdin plan")
	}
}

func TestRunNamesTheFileThatFails(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(bad, []byte("SELECT 1"), 0o644)
	err := run([]string{"-no-open", bad}, nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "notes.txt") {
		t.Errorf("got %v, want an error naming notes.txt", err)
	}
}
