// Package console renders the tool's terminal output: colors, the progress
// bar, and the pause that keeps a drag-and-drop window from vanishing.
//
// Colors are plain ANSI escape codes, which every modern Windows console
// understands, but only once the terminal has been told to interpret them.
// That is what Enable does: it turns on virtual terminal processing for the
// attached console. On a terminal that does not support it, or when output is
// redirected to a file, colors turn themselves off so a log never ends up
// littered with escape codes.
package console

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// ANSI escape sequences. Bright variants are used for the things that should
// catch the eye; the dim ones carry supporting detail.
const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiDim    = "\x1b[2m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiBlue   = "\x1b[34m"
	ansiCyan   = "\x1b[36m"
	ansiWhite  = "\x1b[97m"
)

// enabled reports whether escape codes will be interpreted rather than
// printed. Set once at startup by Enable.
var enabled bool

// Enable turns on ANSI interpretation for this console, when the console
// supports it and output is actually going to a console.
func Enable() {
	enabled = false

	// No point coloring a file or a pipe. IsTerminal already checks stdout,
	// and enabled stays false, so escape codes are never emitted.
	if !IsTerminal() {
		return
	}

	// ANSI interpretation is a Windows console setting. Everywhere else the
	// platform file reports that there is nothing to switch on, and colors
	// stay off, which is the safe default.
	//
	// The handle is the raw stdout value. The platform file does the rest, so
	// nothing above here has to know that the type is Windows-only.
	if enableAnsi(stdoutHandle()) {
		enabled = true
	}
}

// paint wraps text in a color when colors are on, and returns it untouched
// when they are not, so call sites never have to check.
func paint(color, text string) string {
	if !enabled {
		return text
	}
	return color + text + ansiReset
}

// The readable shorthands used throughout the output.
func Dim(s string) string    { return paint(ansiDim, s) }
func Bold(s string) string   { return paint(ansiBold, s) }
func Green(s string) string  { return paint(ansiGreen, s) }
func Red(s string) string    { return paint(ansiRed, s) }
func Yellow(s string) string { return paint(ansiYellow, s) }
func Blue(s string) string   { return paint(ansiBlue, s) }
func Cyan(s string) string   { return paint(ansiCyan, s) }
func White(s string) string  { return paint(ansiWhite, s) }

// Reset ends a styled run. Only needed where styling is applied by hand
// rather than through paint, for instance when a bold color wraps another
// color.
func Reset() string {
	if !enabled {
		return ""
	}
	return ansiReset
}

// IsTerminal reports whether stdout is attached to a console we can draw on.
// When output is piped to a file or another program we stay quiet instead of
// spraying escape codes into the log.
func IsTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// CountColor reddens a failure count so it stands out, and leaves a zero count
// in the ordinary text color so the eye is not pulled to it.
func CountColor(n int) string {
	if n == 0 {
		return "0"
	}
	return Bold(Red(fmt.Sprint(n)))
}

// HumanBytes renders a byte count the way the console output reads best.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTP"[exp])
}

// Progress renders a single updating line, then erases it.
type Progress struct {
	packNo, packTotal     int
	entryDone, entryTotal int
	label                 string
	tty                   bool
}

// NewProgress returns a renderer that draws only when attached to a terminal.
func NewProgress() *Progress {
	return &Progress{tty: IsTerminal()}
}

// Pack announces the start of input n of total.
func (c *Progress) Pack(n, total int, name string) {
	c.packNo, c.packTotal, c.label = n, total, name
	c.entryDone, c.entryTotal = 0, 0
	c.render()
}

// Entry reports that done of total files in the current input are handled.
func (c *Progress) Entry(done, total int) {
	c.entryDone, c.entryTotal = done, total
	c.render()
}

func (c *Progress) render() {
	if !c.tty {
		return
	}

	const width = 28
	filled := 0
	if c.entryTotal > 0 {
		filled = c.entryDone * width / c.entryTotal
	}
	if filled > width {
		filled = width
	}

	bar := strings.Repeat("=", filled) + strings.Repeat(" ", width-filled)

	// The bar is colored and the counters dimmed, so the eye lands on the bar
	// and the pack name rather than on the numbers. Each piece is painted
	// whole, so no escape sequence is left stranded in the middle of the line
	// where a later redraw could overwrite it and leave stray bytes visible.
	frame := fmt.Sprintf("  %s %s %s %s",
		Dim(fmt.Sprintf("pack %d/%d", c.packNo, c.packTotal)),
		Green("["+bar+"]"),
		Dim(fmt.Sprintf("%d/%d", c.entryDone, c.entryTotal)),
		Dim(c.label))

	// \r returns to column 0 and \x1b[K erases the rest of the line, so a long
	// pack name does not leave a tail behind when the next frame is shorter.
	fmt.Printf("\r\x1b[K%s", frame)
}

// Done ends the progress line, moving to a fresh one.
func (c *Progress) Done() {
	if c.tty {
		fmt.Println()
	}
}

// Pause waits for a keypress when a person is watching, and returns immediately
// otherwise.
//
// A console window disappears the instant its process exits, so a run started
// by dropping a pack on the exe would print its result and vanish before
// anyone could read it. Pausing until a key is pressed is what makes the
// output usable.
//
// It only pauses for a real interactive console. When output is redirected, or
// when the process was not started by a person, it returns at once, so nothing
// ever hangs waiting for a keypress that will not come.
func Pause() {
	if !interactive() {
		return
	}

	fmt.Println()
	fmt.Print(Dim("Press Enter to close this window..."))

	// Read a line so the trailing newline is consumed too, otherwise a
	// leftover Enter from the drop operation can close the next thing typed.
	// A scan error means stdin is gone, so there is nothing to wait for.
	bufio.NewReader(os.Stdin).ReadString('\n')
}

// interactive reports whether this process is talking to a real console that a
// person is sitting in front of.
//
// The two things that disqualify it are output that is not a console (a pipe,
// a file, a test runner) and a test binary, which can be handed a
// console-attached handle by the harness and would then hang on the read.
func interactive() bool {
	if !IsTerminal() {
		return false
	}
	if isTestBinary() {
		return false
	}
	return true
}

// isTestBinary reports whether this executable is a `go test` binary, which is
// always named <something>.test.exe.
func isTestBinary() bool {
	return strings.HasSuffix(strings.ToLower(os.Args[0]), ".test.exe")
}
