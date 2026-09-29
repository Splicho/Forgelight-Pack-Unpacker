//go:build !windows

package console

// ANSI enablement on platforms that do not have Windows console modes.
//
// Windows is the target for this tool and the only place where a console has
// to be told to interpret ANSI escapes. Everywhere else the terminal either
// already does or never will, so there is nothing to switch on. Reporting
// false leaves colors off, which is the safe default: a log never ends up
// littered with escape codes.
//
// The parameter is the platform's own console handle type on Windows, so it
// cannot be named here. It is unused, and that is the point: off Windows the
// enable call has nothing to do.

func enableAnsi(uintptr) bool { return false }
