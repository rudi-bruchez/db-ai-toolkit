package sqlq

import "testing"

func TestLexTracksLineOffsetAndDepth(t *testing.T) {
	src := "SELECT IIF(@a = 1, 2, 3);\r\nSET @b += 1;"
	toks := Lex(Sanitize(src))
	want := []struct {
		text  string
		line  int
		depth int
	}{
		{"SELECT", 1, 0}, {"IIF", 1, 0}, {"(", 1, 0}, {"@a", 1, 1}, {"=", 1, 1},
		{"1", 1, 1}, {",", 1, 1}, {"2", 1, 1}, {",", 1, 1}, {"3", 1, 1}, {")", 1, 0},
		{";", 1, 0}, {"SET", 2, 0}, {"@b", 2, 0}, {"+=", 2, 0}, {"1", 2, 0}, {";", 2, 0},
	}
	if len(toks) != len(want) {
		t.Fatalf("got %d tokens %v, want %d", len(toks), toks, len(want))
	}
	for i, w := range want {
		if toks[i].Text != w.text || toks[i].Line != w.line || toks[i].Depth != w.depth {
			t.Errorf("token %d = %+v, want %+v", i, toks[i], w)
		}
	}
	// Offsets are rune offsets into the text, so they survive multi-byte runes.
	rs := []rune("-- é\nSELECT @x;")
	toks = Lex(Sanitize(string(rs)))
	if got := string(rs[toks[1].Start:toks[1].End]); got != "@x" {
		t.Errorf("rune offsets point at %q, want @x", got)
	}
}
