// Package cli holds the few things both command line front ends share: how a
// finished run reports itself, and how a fatal problem ends the process.
//
// There are two binaries, packer.exe and unpacker.exe, and each is a main that
// does one direction only. What they have in common is small but real, and
// keeping it here means the color rules and exit codes for "done" and for
// "that failed" are written down once instead of twice.
package cli

import (
	"fmt"
	"os"

	"packunpacker/internal/console"
	"packunpacker/internal/job"
)

// Fatal reports an unrecoverable problem and stops with a non-zero code.
// progName names the binary in the message, since there are two of them and
// the user needs to know which one refused the work.
func Fatal(progName string, err error) {
	if err == nil {
		return
	}

	// Let the user see the output above before the window goes away.
	console.Pause()
	fmt.Fprintf(os.Stderr, "\n%s %s\n", console.Bold(console.Red(progName)), err)
	os.Exit(1)
}

// Summary colors the word that opens the summary line to match the outcome:
// green for a clean run, red if anything failed.
func Summary(st *job.Stats) string {
	if st.Failed > 0 {
		return console.Bold(console.Red("done."))
	}
	return console.Bold(console.Green("done."))
}

// ExitCode is what a finished run should return: 0 when nothing failed, 1
// otherwise. A run that wrote 200 of 200 files but skipped two is a run that
// needs looking at, and a script driving this should be able to tell.
func ExitCode(st *job.Stats) int {
	if st.Failed > 0 {
		return 1
	}
	return 0
}
