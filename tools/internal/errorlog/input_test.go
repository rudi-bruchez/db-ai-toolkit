package errorlog

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquirePlainAndZip(t *testing.T) {
	dir := t.TempDir()

	plain := filepath.Join(dir, "ERRORLOG")
	if err := os.WriteFile(plain, encodeUTF16LE("hello\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcs, err := Acquire(plain)
	if err != nil || len(srcs) != 1 {
		t.Fatalf("Acquire plain: %v, n=%d", err, len(srcs))
	}
	if Decode(srcs[0].Data) != "hello\n" {
		t.Errorf("plain data = %q", Decode(srcs[0].Data))
	}

	zpath := filepath.Join(dir, "ERRORLOG.zip")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("ERRORLOG")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(encodeUTF16LE("zipped\r\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zpath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	srcs, err = Acquire(zpath)
	if err != nil || len(srcs) != 1 {
		t.Fatalf("Acquire zip: %v, n=%d", err, len(srcs))
	}
	if Decode(srcs[0].Data) != "zipped\n" {
		t.Errorf("zip data = %q", Decode(srcs[0].Data))
	}
}
