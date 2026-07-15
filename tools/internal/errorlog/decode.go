package errorlog

import (
	"encoding/binary"
	"strings"
	"unicode/utf16"
)

// Decode converts raw ERRORLOG bytes to UTF-8 text with LF line endings,
// detecting the encoding from a leading BOM. SQL Server writes ERRORLOG in
// UTF-16LE with a BOM on Windows; UTF-8 (with or without BOM) is also handled.
func Decode(raw []byte) string {
	switch {
	case len(raw) >= 2 && raw[0] == 0xFF && raw[1] == 0xFE:
		return normalizeNewlines(decodeUTF16(raw[2:], binary.LittleEndian))
	case len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF:
		return normalizeNewlines(decodeUTF16(raw[2:], binary.BigEndian))
	case len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF:
		return normalizeNewlines(string(raw[3:]))
	default:
		return normalizeNewlines(string(raw))
	}
}

func decodeUTF16(b []byte, order binary.ByteOrder) string {
	if len(b)%2 != 0 {
		b = b[:len(b)-1] // drop a dangling odd byte defensively
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = order.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u))
}

func normalizeNewlines(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}
