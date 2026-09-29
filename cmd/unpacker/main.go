// Unpacker — extracts ForgeLight .pack archives.
//
// Drag one or more .pack files onto unpacker.exe and each is unpacked into a
// folder of the same name next to it, so Assets_027.pack becomes Assets_027/.
// The console stays open the whole time and reports what it is doing, which is
// the whole point of doing this in a console: you can see what happened, and a
// failure tells you why instead of vanishing.
//
// Built with -H windowsgui the app has no console of its own and creates one on
// launch; without that flag it behaves as an ordinary command line tool, which
// is what the tests and any scripted use go through.
//
// This file is the command line layer and nothing else: flags, the summary,
// and one call into internal/unpack. The archive format lives in
// internal/format. Its sibling packer.exe does the other direction and shares
// only internal/cli.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"packunpacker/internal/cli"
	"packunpacker/internal/console"
	"packunpacker/internal/format"
	"packunpacker/internal/job"
	"packunpacker/internal/unpack"
)

// progName is what shows up in error output.
const progName = "Unpacker"

func main() {
	// Must happen before anything is printed, or the first lines go out
	// uncolored while the mode change is still taking effect.
	console.Enable()

	force := flag.Bool("force", false, "overwrite existing files instead of skipping matching ones")
	verify := flag.Bool("verify", false, "check the CRC32 of every entry")
	dryRun := flag.Bool("dry-run", false, "parse the packs and report, write nothing")
	outDir := flag.String("o", "", "write into this folder instead of one named after each pack")
	quiet := flag.Bool("quiet", false, "only print the final summary")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "%s%s - extracts ForgeLight .pack archives\n\n",
			console.Bold(console.Cyan(progName)), console.Reset())
		fmt.Fprintf(os.Stderr, "usage: %s [options] <pack.pack> [more.pack ...]\n", filepath.Base(os.Args[0]))
		fmt.Fprintln(os.Stderr, "\nOr just drag one or more .pack files onto the .exe.")
		fmt.Fprintln(os.Stderr, "Each becomes a folder of the same name next to it.")
		fmt.Fprintln(os.Stderr, "\nTo go the other way, use packer.exe on a folder.")
		flag.PrintDefaults()
	}
	flag.Parse()

	inputs := flag.Args()
	if len(inputs) == 0 {
		flag.Usage()
		return
	}

	opts := job.Options{Force: *force, Verify: *verify, DryRun: *dryRun, Quiet: *quiet}

	bar := console.NewProgress()
	overall, err := runJobs(inputs, *outDir, opts, bar)
	bar.Done()
	if err != nil {
		cli.Fatal(progName, err)
	}

	// The summary carries the outcome, so it gets the strongest color that
	// matches: green for a clean run, red if anything failed.
	fmt.Println()
	fmt.Printf("%s %s=%d  %s=%s  %s=%d  %s=%s  %s=%.1fs\n",
		cli.Summary(overall),
		console.Dim("files"), overall.Files,
		console.Dim("written"), console.HumanBytes(overall.Written),
		console.Dim("skipped"), overall.Skipped,
		console.Dim("failed"), console.CountColor(overall.Failed),
		console.Dim("elapsed"), overall.Elapsed.Seconds())

	// A console window closes the moment the process exits, so on a
	// drag-and-drop run everything printed above would flash by unread. Hold
	// the window open until a key is pressed.
	console.Pause()

	os.Exit(cli.ExitCode(overall))
}

// runJobs unpacks every input in order. Jobs run one at a time on purpose: the
// packs are large and disk-bound, so overlapping them only thrashes the disk
// and makes the progress bar lie.
func runJobs(inputs []string, outRoot string, opts job.Options, prog job.Progress) (*job.Stats, error) {
	overall := &job.Stats{}
	begin := time.Now()
	seen := 0

	for _, in := range inputs {
		abs, err := filepath.Abs(in)
		if err != nil {
			abs = in
		}
		if !strings.EqualFold(filepath.Ext(abs), ".pack") {
			// Dropping something that is not a pack should not be an error,
			// just something quietly passed over, and it should not claim a
			// slot in the pack n/total counter either. A folder is the other
			// tool's job, and that is the likely mistake, so name the exe.
			if !opts.Quiet {
				hint := "not a .pack file"
				if info, err := os.Stat(abs); err == nil && info.IsDir() {
					hint = "that is a folder, use packer.exe"
				}
				fmt.Fprintf(os.Stderr, "  %s %s: %s\n",
					console.Yellow("SKIP"), console.Yellow(filepath.Base(abs)), hint)
			}
			continue
		}

		seen++
		prog.Pack(seen, len(inputs), filepath.Base(abs))
		res := unpackOne(abs, outRoot, opts, prog)
		mergeStats(overall, &res.Stats)
		if res.Err != nil {
			overall.Elapsed = time.Since(begin)
			return overall, res.Err
		}
	}

	if seen == 0 {
		// A drop of the wrong kind is the likely cause here, so say which exe
		// handles it rather than leaving the user to work it out.
		msg := "no .pack files found: drag a .pack onto this exe, or use packer.exe on a folder"
		if !opts.Quiet {
			fmt.Fprintln(os.Stderr, console.Yellow(msg))
		}
		overall.Elapsed = time.Since(begin)
		return overall, errors.New(msg)
	}

	overall.Elapsed = time.Since(begin)
	return overall, nil
}

// unpackOne is the whole single-file job.
func unpackOne(packPath, outRoot string, opts job.Options, prog job.Progress) job.Result {
	res := job.Result{Input: packPath}

	base := strings.TrimSuffix(filepath.Base(packPath), filepath.Ext(packPath))
	outDir := filepath.Join(filepath.Dir(packPath), base)
	if outRoot != "" {
		outDir = filepath.Join(outRoot, base)
	}
	res.Output = outDir

	f, err := os.Open(packPath)
	if err != nil {
		res.Err = fmt.Errorf("open %s: %w", filepath.Base(packPath), err)
		return res
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		res.Err = fmt.Errorf("stat %s: %w", filepath.Base(packPath), err)
		return res
	}

	idx, err := format.Read(f, info.Size())
	if err != nil {
		res.Err = fmt.Errorf("%s: %w", filepath.Base(packPath), err)
		return res
	}

	// The notice line, once the pack is known to be readable and we know how
	// much is in it. Printed before any writing starts, so a failure part way
	// through still shows what was being worked on.
	if !opts.Quiet {
		fmt.Printf("\n%s %s %s\n",
			console.Bold(console.Blue("Exporting assets of")),
			console.Bold(console.White(filepath.Base(packPath))),
			console.Dim(fmt.Sprintf("(%d files, %s)", len(idx.Entries), console.HumanBytes(idx.TotalBytes))))
	}

	st, err := unpack.Extract(f, idx, outDir, opts, func(done, total int, _ int64) {
		prog.Entry(done, total)
	})
	res.Stats = *st
	res.Err = err
	return res
}

// mergeStats folds a job's numbers into the run total.
func mergeStats(overall, add *job.Stats) {
	if add == nil {
		return
	}
	overall.Files += add.Files
	overall.Written += add.Written
	overall.Skipped += add.Skipped
	overall.Failed += add.Failed
}
