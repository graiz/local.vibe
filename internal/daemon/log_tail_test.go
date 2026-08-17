package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLog(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "route.log")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Managed processes run under a login shell, so their logs are full of the
// prompt's color codes. Handing those to the dashboard verbatim rendered as
// literal "[2m"/"[37m" garbage on the start page.
func TestTailLogFileStripsANSI(t *testing.T) {
	p := writeLog(t, "\x1b[31mError:\x1b[0m listen EADDRINUSE \x1b[1mport 3003\x1b[0m")

	got := tailLogFile(p, 12)

	if strings.Contains(got, "\x1b") {
		t.Errorf("tail still carries escape bytes: %q", got)
	}
	if want := "Error: listen EADDRINUSE port 3003"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// OSC-8 hyperlinks (\x1b]8;;URL\x07text\x1b]8;;\x07) show up in modern tool
// output and are not CSI sequences, so a CSI-only stripper leaves the URL
// payload behind as noise.
func TestTailLogFileStripsHyperlinkSequences(t *testing.T) {
	p := writeLog(t, "see \x1b]8;;https://example.com/x\x07the docs\x1b]8;;\x07 for details")

	got := tailLogFile(p, 12)

	if strings.Contains(got, "example.com") || strings.Contains(got, "\x1b") {
		t.Errorf("hyperlink escape not stripped: %q", got)
	}
	if want := "see the docs for details"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The window is only 12-24 lines, and a decorated zsh prompt prints ~20 lines
// of box-drawing per spawn. Those lines carry no information, so letting them
// occupy the window pushes the actual error out of view.
func TestTailLogFileDropsDecorationOnlyLines(t *testing.T) {
	p := writeLog(t,
		"Error: something broke",
		"  .==================.",
		"  | .--------------. |",
		"  ▀▀▀▀▀▀▀▀▀▀▀▀",
		"  ╰──────────╯",
	)

	got := tailLogFile(p, 3)

	if !strings.Contains(got, "Error: something broke") {
		t.Errorf("decoration crowded out the real error; got %q", got)
	}
}

// Decoration filtering must not eat real output that happens to contain
// punctuation — only lines with no letters or digits at all are noise.
func TestTailLogFileKeepsLinesWithContent(t *testing.T) {
	p := writeLog(t,
		"  |   iMacNano   |",
		"---> build finished in 1.2s",
	)

	got := tailLogFile(p, 12)

	for _, want := range []string{"iMacNano", "build finished in 1.2s"} {
		if !strings.Contains(got, want) {
			t.Errorf("dropped a line with real content (%q); got %q", want, got)
		}
	}
}
