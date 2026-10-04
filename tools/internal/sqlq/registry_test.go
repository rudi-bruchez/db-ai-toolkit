package sqlq

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "verified.json")
	r, msg := LoadRegistry(path)
	if msg != "" || r.Lookup("x") != nil {
		t.Fatalf("absent registry should be empty and silent, got %q", msg)
	}
	if err := RecordVerified(path, "aaa", Verified{Date: "2026-10-04", Profile: "p1"}); err != nil {
		t.Fatal(err)
	}
	if err := RecordVerified(path, "bbb", Verified{Date: "2026-10-05", Profile: "p2"}); err != nil {
		t.Fatal(err)
	}
	r, _ = LoadRegistry(path)
	if v := r.Lookup("aaa"); v == nil || v.Profile != "p1" {
		t.Errorf("aaa lost: %+v", v)
	}
	if v := r.Lookup("bbb"); v == nil || v.Date != "2026-10-05" {
		t.Errorf("bbb: %+v", v)
	}
}

func TestRegistryCorruptIsTreatedAsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verified.json")
	os.WriteFile(path, []byte("{not json"), 0o600)
	r, msg := LoadRegistry(path)
	if msg == "" || r.Lookup("x") != nil {
		t.Errorf("corrupt registry: msg %q", msg)
	}
	if err := RecordVerified(path, "ccc", Verified{Date: "d", Profile: "p"}); err != nil {
		t.Fatal(err)
	}
	if r, msg = LoadRegistry(path); msg != "" || r.Lookup("ccc") == nil {
		t.Errorf("rewritten registry should load cleanly: %q", msg)
	}
}

func TestContentHashIsStable(t *testing.T) {
	if ContentHash([]byte("SELECT 1;")) != ContentHash([]byte("SELECT 1;")) ||
		ContentHash([]byte("SELECT 1;")) == ContentHash([]byte("SELECT 2;")) {
		t.Error("hash must depend on content only")
	}
}

func TestRegistryJSONNullIsTreatedAsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verified.json")
	os.WriteFile(path, []byte("null"), 0o600)
	if err := RecordVerified(path, "ddd", Verified{Date: "d", Profile: "p"}); err != nil {
		t.Fatal(err)
	}
	if r, msg := LoadRegistry(path); msg != "" || r.Lookup("ddd") == nil {
		t.Errorf("registry after null: %q", msg)
	}
}

func TestRecordVerifiedErrorCarriesNoPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "readonly")
	os.Mkdir(dir, 0o500)
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	err := RecordVerified(filepath.Join(dir, "verified.json"), "eee", Verified{Date: "d", Profile: "p"})
	if err == nil {
		t.Skip("directory is writable despite 0500 (running as root?)")
	}
	if strings.Contains(err.Error(), dir) {
		t.Errorf("error publishes a path: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("failed write left files behind: %v", entries)
	}
}
