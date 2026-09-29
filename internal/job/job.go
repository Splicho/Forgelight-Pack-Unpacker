// Package job holds the vocabulary shared by the two halves of this tool.
//
// Unpacking and packing are separate concerns that speak the same language: a
// run is described by a set of Options, it reports what it did through Stats,
// and it reports progress through a Progress it does not implement. Those
// three types live here so neither half has to import the other, and so the
// command line layer can hold both kinds of result at once.
package job

import "time"

// Options controls one run.
type Options struct {
	Force  bool // overwrite existing files even when the size already matches
	Verify bool // check CRC32 of every entry against the value in the index
	DryRun bool // parse only, write nothing
	Quiet  bool // suppress the per-item reporting
}

// Stats accumulates what a run did, for the final summary line.
type Stats struct {
	Files   int
	Written int64
	Skipped int
	Failed  int
	Elapsed time.Duration
}

// Result is the outcome of one input, whether that input was unpacked or
// packed. Err is non-nil only for a failure that stopped the whole run; a
// per-file problem is counted in Stats.Failed and reported as it happens.
type Result struct {
	Input  string
	Output string
	Stats  Stats
	Err    error
}

// Progress is the whole reporting surface a run needs. The renderer lives in
// the console package; declaring the surface here keeps the run loops from
// depending on how progress is drawn.
type Progress interface {
	// Pack announces the start of input n of total, named name.
	Pack(n, total int, name string)
	// Entry reports that done of total files in the current input are handled.
	Entry(done, total int)
	// Done is called once when everything has finished.
	Done()
}

// NoProgress is a Progress that discards everything, for tests and for runs
// that want no progress output at all.
type NoProgress struct{}

func (NoProgress) Pack(int, int, string) {}
func (NoProgress) Entry(int, int)        {}
func (NoProgress) Done()                 {}
