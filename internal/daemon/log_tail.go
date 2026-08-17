package daemon

import (
	"regexp"
	"strings"
	"unicode"
)

// Managed processes are spawned under a login shell (`$SHELL -lic` on darwin),
// so their logs carry whatever that shell's startup emits: color codes, cursor
// moves, hyperlinks, and — for anyone running a themed prompt — a decorative
// MOTD banner. Both are noise in a crash diagnosis, and both were previously
// passed through verbatim: the dashboard rendered raw escape bytes as literal
// "[2m"/"[37m" text, and a ~20-line banner filled the whole 12-24 line tail
// window that is supposed to show the error.

// ansiRe matches the two escape families that actually appear in dev-server
// output: OSC strings (hyperlinks, title sets — terminated by BEL or ST) and
// CSI sequences (SGR colors, cursor movement). OSC is listed first so its
// payload is consumed as part of the sequence rather than left behind as text.
var ansiRe = regexp.MustCompile("\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|\x1b\\[[0-9;?]*[ -/]*[@-~]")

// stripANSI removes terminal escape sequences from s. Any stray ESC left over
// (a truncated sequence, say — logs get cut mid-write) is dropped too, so the
// result is always safe to render as plain text.
func stripANSI(s string) string {
	return strings.ReplaceAll(ansiRe.ReplaceAllString(s, ""), "\x1b", "")
}

// isDecorationOnly reports whether a line carries no information: no letters
// and no digits, i.e. box-drawing, rules, and ASCII art. Deliberately narrow —
// it must not eat real output. A banner line that names something ("node ·
// iMacNano") has letters and is kept; only the frame around it is dropped.
// Blank lines qualify, which is how empty-line filtering folds in here.
func isDecorationOnly(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
