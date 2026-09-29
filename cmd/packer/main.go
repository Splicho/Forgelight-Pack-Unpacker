// Packer — turns ForgeLight asset folders back into .pack archives.
//
// Drag one or more folders onto packer.exe and each becomes a .pack beside it,
// so MyAssets becomes MyAssets.pack. Subfolder structure is preserved, so a
// drop and an unpack later give back the same tree. The console stays open the
// whole time and reports what it is doing, which is the whole point of doing
// this in a console: you can see what happened, and a failure tells you why
// instead of vanishing.
//
// Built with -H windowsgui the app has no console of its own and creates one on
// launch; without that flag it behaves as an ordinary command line tool, which
// is what the tests and any scripted use go through.
//
// This file is the command line layer and nothing else: flags, the summary,
// and one call into internal/pack. The archive format lives in
// internal/format. Its sibling unpacker.exe does the other direction and
// shares only internal/cli.
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
	"packunpacker/internal/job"
	"packunpacker/internal/pack"
)

// progName is what shows up in error output.
const progName = "Packer"

func main() {
	// Must happen before anything is printed, or the first lines go out
	// uncolored while the mode change is still taking effect.
	console.Enable()

	outDir := flag.String("o", "", "write the archives into this folder instead of beside each input")
	dryRun := flag.Bool("dry-run", false, "report what would be packed, write nothing")
	quiet := flag.Bool("quiet", false, "only print the final summary")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "%s%s - builds ForgeLight .pack archives from folders\n\n",
			console.Bold(console.Cyan(progName)), console.Reset())
		fmt.Fprintf(os.Stderr, "usage: %s [options] <folder> [more.folder ...]\n", filepath.Base(os.Args[0]))
		fmt.Fprintln(os.Stderr, "\nOr just drag one or more folders onto the .exe.")
		fmt.Fprintln(os.Stderr, "Each becomes <folder>.pack beside it, subfolders and all.")
		fmt.Fprintln(os.Stderr, "\nTo go the other way, use unpacker.exe on a .pack file.")
		flag.PrintDefaults()
	}
	flag.Parse()

	inputs := flag.Args()
	if len(inputs) == 0 {
		flag.Usage()
		return
	}

	opts := job.Options{DryRun: *dryRun, Quiet: *quiet}

	bar := console.NewProgress()
	overall, err := runJobs(inputs, *outDir, opts, bar)
	bar.Done()
	if err != nil {
		cli.Fatal(progName, err)
	}

	// The summary carries the outcome, so it gets the strongest color that
	// matches: green for a clean run, red if anything failed.
	fmt.Println()
	fmt.Printf("%s %s=%d  %s=%s  %s=%s  %s=%.1fs\n",
		cli.Summary(overall),
		console.Dim("files"), overall.Files,
		console.Dim("packed"), console.HumanBytes(overall.Written),
		console.Dim("failed"), console.CountColor(overall.Failed),
		console.Dim("elapsed"), overall.Elapsed.Seconds())

	// A console window closes the moment the process exits, so on a
	// drag-and-drop run everything printed above would flash by unread. Hold
	// the window open until a key is pressed.
	console.Pause()

	os.Exit(cli.ExitCode(overall))
}

// runJobs packs every input folder in order, one at a time on purpose: the
// work is disk-bound and reading two large asset trees at once only thrashes
// the drive and makes the progress bar lie.
func runJobs(inputs []string, outRoot string, opts job.Options, prog job.Progress) (*job.Stats, error) {
	overall := &job.Stats{}
	begin := time.Now()
	seen := 0

	for _, in := range inputs {
		abs, err := filepath.Abs(in)
		if err != nil {
			abs = in
		}

		info, statErr := os.Stat(abs)
		if statErr != nil {
			// A path that does not exist cannot be packed, but it is also not
			// worth stopping for when several folders were dropped: say so and
			// carry on with the rest.
			overall.Failed++
			fmt.Fprintf(os.Stderr, "  %s %s: %v\n",
				console.Red("SKIP"), console.Yellow(filepath.Base(abs)), statErr)
			continue
		}
		if !info.IsDir() {
			// A .pack is the other tool's job, and that is the single most
			// likely mistake, so name the exe that handles it.
			if !opts.Quiet {
				hint := "not a folder"
				if strings.EqualFold(filepath.Ext(abs), ".pack") {
					hint = "that is a .pack, use unpacker.exe"
				}
				fmt.Fprintf(os.Stderr, "  %s %s: %s\n",
					console.Yellow("SKIP"), console.Yellow(filepath.Base(abs)), hint)
			}
			continue
		}

		seen++
		prog.Pack(seen, len(inputs), filepath.Base(abs))

		outPath := ""
		if outRoot != "" {
			outPath = filepath.Join(outRoot, filepath.Base(abs)+".pack")
		}

		// The notice line, once the folder is known to be worth packing and we
		// know how much is in it. Printed before any writing starts, so a
		// failure part way through still shows what was being worked on.
		res := pack.Folder(abs, outPath, opts, prog, func(name string, files int, bytes int64) {
			if opts.Quiet {
				return
			}
			fmt.Printf("\n%s %s %s\n",
				console.Bold(console.Blue("Packing assets of")),
				console.Bold(console.White(name)),
				console.Dim(fmt.Sprintf("(%d files, %s)", files, console.HumanBytes(bytes))))
		})

		mergeStats(overall, &res.Stats)
		if res.Err != nil {
			overall.Elapsed = time.Since(begin)
			return overall, res.Err
		}
	}

	if seen == 0 {
		// A missing path was already reported and counted above. Saying "no
		// folders to pack" on top of that would describe a different problem,
		// so let the counted failure speak for itself.
		msg := "no folders to pack: drag a folder onto this exe, or use unpacker.exe on a .pack"
		if overall.Failed > 0 {
			msg = "nothing could be packed"
		}
		if !opts.Quiet {
			fmt.Fprintln(os.Stderr, console.Yellow(msg))
		}
		overall.Elapsed = time.Since(begin)
		return overall, errors.New(msg)
	}

	overall.Elapsed = time.Since(begin)
	return overall, nil
}

// mergeStats folds a job's numbers into the run total.
func mergeStats(overall, add *job.Stats) {
	if add == nil {
		return
	}
	overall.Files += add.Files
	overall.Written += add.Written
	overall.Failed += add.Failed
}
