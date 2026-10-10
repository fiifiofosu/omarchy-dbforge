package termsafe

import (
	"bytes"
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		// The reported case: OSC 52 asks the terminal to set the clipboard.
		// Base64 "pwned" is cHduZWQ=.
		{"osc52 BEL", "ERROR: x\x1b]52;c;cHduZWQ=\x07 y", "ERROR: x y"},
		{"osc52 ST", "a\x1b]52;c;cHduZWQ=\x1b\\b", "ab"},
		{"osc52 C1 ST", "a\x1b]52;c;cHduZWQ=\u009cb", "ab"},
		{"C1 OSC", "a\u009d52;c;cHduZWQ=\x07b", "ab"},
		{"title", "a\x1b]0;owned\x07b", "ab"},
		{"colour", "\x1b[31mred\x1b[0m", "red"},
		{"C1 CSI", "a\u009b2Jb", "ab"},
		{"raw 8-bit CSI byte", "a\x9b2Jb", "ab"},
		{"charset designation", "a\x1b(Bb", "ab"},
		{"DCS", "a\x1bPq#0;2;0;0;0\x1b\\b", "ab"},
		{"carriage return overwrite", "safe\rEVIL", "safeEVIL"},
		{"backspace", "ab\bc", "abc"},
		{"bell and DEL", "a\x07\x7fb", "ab"},
		// An unterminated sequence must not swallow later lines.
		{"unterminated osc", "a\x1b]52;c;xxx\nnext", "a\nnext"},
		{"unterminated csi", "a\x1b[12\nnext", "a\nnext"},
		{"keeps tabs and newlines", "a\tb\nc", "a\tb\nc"},
		{"keeps unicode", "héllo — 日本 ✓", "héllo — 日本 ✓"},
		{"plain postgres line", "2026-10-10 16:00:00 UTC [1] LOG:  database system is ready", "2026-10-10 16:00:00 UTC [1] LOG:  database system is ready"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := String(c.in); got != c.want {
				t.Errorf("String(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestLineExpandsTabs(t *testing.T) {
	if got := Line("a\tb"); got != "a    b" {
		t.Errorf("Line = %q", got)
	}
}

// A sequence can straddle two reads of the log stream; splitting the input
// at every possible byte must give the same output as filtering it whole.
func TestWriterAcrossWrites(t *testing.T) {
	in := "ok \x1b]52;c;cHduZWQ=\x1b\\ héllo \x1b[31mred\u009b2J 日本\n"
	want := String(in)
	for cut := 0; cut <= len(in); cut++ {
		var out bytes.Buffer
		w := NewWriter(&out)
		if _, err := w.Write([]byte(in[:cut])); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(in[cut:])); err != nil {
			t.Fatal(err)
		}
		if out.String() != want {
			t.Fatalf("cut at %d: got %q, want %q", cut, out.String(), want)
		}
	}
}

func TestNoEscapeSurvives(t *testing.T) {
	// Every byte value, in every position: nothing a terminal treats as the
	// start of a control sequence may come out.
	var b strings.Builder
	for i := 0; i < 256; i++ {
		b.WriteByte(byte(i))
		b.WriteString("x")
	}
	out := String(b.String())
	for _, r := range out {
		if r == 0x1b || (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			t.Fatalf("control %U survived in %q", r, out)
		}
	}
}
