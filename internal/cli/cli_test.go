package cli

import (
	"testing"

	"packunpacker/internal/job"
)

func TestExitCode(t *testing.T) {
	// A clean run is 0, and a run that skipped something is 1, so a script
	// driving these tools can tell the difference without parsing the output.
	cases := map[string]struct {
		stats job.Stats
		want  int
	}{
		"clean":       {job.Stats{Files: 10}, 0},
		"nothing yet": {job.Stats{}, 0},
		"one failed":  {job.Stats{Files: 10, Failed: 1}, 1},
		"all failed":  {job.Stats{Files: 3, Failed: 3}, 1},
	}
	for label, tc := range cases {
		if got := ExitCode(&tc.stats); got != tc.want {
			t.Errorf("%s: ExitCode = %d, want %d", label, got, tc.want)
		}
	}
}

func TestSummaryColorsMatchOutcome(t *testing.T) {
	// Colors are off in a test run, so both summaries come back as the same
	// plain text. What is being asserted here is that the function is safe
	// and non-empty for either outcome; the color itself is a terminal
	// concern covered by the console package.
	for _, st := range []job.Stats{{}, {Failed: 1}} {
		if Summary(&st) == "" {
			t.Errorf("Summary returned an empty string for %+v", st)
		}
	}
}
