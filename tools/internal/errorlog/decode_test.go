package errorlog

import (
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

// encodeUTF16LE builds a UTF-16LE byte slice with BOM and CRLF endings,
// mimicking a real SQL Server ERRORLOG on Windows.
func encodeUTF16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := []byte{0xFF, 0xFE}
	for _, v := range u {
		b = binary.LittleEndian.AppendUint16(b, v)
	}
	return b
}

func TestDecodeUTF16LEWithBOMAndCRLF(t *testing.T) {
	raw := encodeUTF16LE("Erreur : 976\r\nGravité : 14\r\n")
	got := Decode(raw)
	want := "Erreur : 976\nGravité : 14\n"
	if got != want {
		t.Fatalf("Decode = %q, want %q", got, want)
	}
}

func TestDecodeUTF8Passthrough(t *testing.T) {
	if got := Decode([]byte("plain\r\ntext")); got != "plain\ntext" {
		t.Fatalf("Decode = %q", got)
	}
	if got := Decode([]byte{0xEF, 0xBB, 0xBF, 'x'}); got != "x" {
		t.Fatalf("BOM strip failed: %q", got)
	}
}
