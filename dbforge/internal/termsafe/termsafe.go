// Package termsafe makes untrusted text safe to print to a terminal.
//
// Database logs are written by whoever can reach the database: a client's
// failed query, a bad password attempt and an application's RAISE all land in
// them verbatim. Printed raw, an escape sequence in one of those lines is an
// instruction to the user's terminal -- OSC 52 rewrites the clipboard in
// terminals that allow it (Ghostty does by default), others retitle the
// window, move the cursor over earlier output, or redefine colours to hide
// text. So log text is filtered before it reaches a terminal.
//
// The filter removes every escape sequence and control character outright
// rather than trying to tell harmless ones (colour) from harmful ones: the
// terminal never sees an ESC, a C1 control or a raw 8-bit control byte, so
// it has no way to start a sequence at all. Newlines and tabs are kept, and
// all other text -- including non-ASCII -- passes through unchanged.
package termsafe

import (
	"io"
	"strings"
	"unicode/utf8"
)

type state int

const (
	stNormal    state = iota
	stEsc             // after ESC
	stCSI             // ESC [ (or C1 CSI): parameters until a final byte
	stString          // OSC, DCS, SOS, PM, APC: a body until BEL or ST
	stStringEsc       // ESC inside a string body; a backslash ends it (ST)
)

// Filter strips control sequences from a stream. It keeps its parser state
// between calls, so a sequence split across two reads is still removed. The
// zero value is ready to use.
type Filter struct {
	st      state
	pending []byte // an incomplete UTF-8 encoding held over from the last call
}

// Append filters p and appends the safe text to dst.
func (f *Filter) Append(dst, p []byte) []byte {
	buf := p
	if len(f.pending) > 0 {
		buf = append(f.pending, p...)
		f.pending = nil
	}
	for len(buf) > 0 {
		r, size := utf8.DecodeRune(buf)
		if r == utf8.RuneError && size <= 1 {
			if !utf8.FullRune(buf) {
				// The rest of a character may arrive in the next call.
				f.pending = append([]byte(nil), buf...)
				break
			}
			// Invalid UTF-8, never printed. A lone byte in 0x80-0x9f is an
			// 8-bit C1 control to a terminal that honours them, so it is
			// parsed as one: its parameters go with it, as they would for
			// the same control encoded properly.
			if b := buf[0]; b >= 0x80 && b < 0xa0 {
				dst = f.step(dst, rune(b))
			}
			buf = buf[1:]
			continue
		}
		buf = buf[size:]
		dst = f.step(dst, r)
	}
	return dst
}

func (f *Filter) step(dst []byte, r rune) []byte {
	// A newline ends whatever was in progress. The introducer has already
	// been dropped, so anything after it is plain text, and an unterminated
	// sequence can swallow at most the rest of its own line.
	if r == '\n' {
		f.st = stNormal
		return append(dst, '\n')
	}

	switch f.st {
	case stEsc:
		switch {
		case r == '[':
			f.st = stCSI
		case r == ']', r == 'P', r == 'X', r == '^', r == '_':
			f.st = stString
		case r >= 0x20 && r <= 0x2f:
			// Intermediate byte, as in ESC ( B: the sequence continues.
		default:
			f.st = stNormal // the final byte of a two-character escape
		}
		return dst
	case stCSI:
		if r >= 0x40 && r <= 0x7e {
			f.st = stNormal
		}
		return dst
	case stString:
		switch r {
		case 0x07, 0x9c: // BEL, C1 ST
			f.st = stNormal
		case 0x1b:
			f.st = stStringEsc
		}
		return dst
	case stStringEsc:
		switch r {
		case '\\':
			f.st = stNormal
		case 0x1b:
		default:
			f.st = stString
		}
		return dst
	}

	switch {
	case r == 0x1b:
		f.st = stEsc
	case r == 0x9b:
		f.st = stCSI
	case r == 0x90, r == 0x98, r == 0x9d, r == 0x9e, r == 0x9f:
		f.st = stString
	case r == '\t':
		dst = append(dst, '\t')
	case r < 0x20, r == 0x7f, r >= 0x80 && r < 0xa0:
		// Other C0 and C1 controls: carriage return can overwrite the line
		// already printed, backspace can erase it. Drop them all.
	default:
		dst = utf8.AppendRune(dst, r)
	}
	return dst
}

// String filters one complete piece of text.
func String(s string) string {
	var f Filter
	return string(f.Append(nil, []byte(s)))
}

// Line filters a single line for a fixed-width display: as String, with tabs
// turned into spaces so width arithmetic on the result holds.
func Line(s string) string {
	return strings.ReplaceAll(String(s), "\t", "    ")
}

// Writer filters everything written through it to w.
type Writer struct {
	w   io.Writer
	f   Filter
	buf []byte
}

// NewWriter returns a Writer that filters into w.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// Write filters p and writes the result. It reports len(p) on success, since
// every input byte was consumed even when the filter dropped it.
func (w *Writer) Write(p []byte) (int, error) {
	w.buf = w.f.Append(w.buf[:0], p)
	if len(w.buf) == 0 {
		return len(p), nil
	}
	if _, err := w.w.Write(w.buf); err != nil {
		return 0, err
	}
	return len(p), nil
}
