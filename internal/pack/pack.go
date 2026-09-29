// Package pack turns a folder of files back into a .pack archive.
//
// The archive layout itself lives in internal/format; this package is the part
// that knows about folders on disk: which files to include, what to call them,
// and where the resulting archive goes.
package pack

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"packunpacker/internal/format"
	"packunpacker/internal/job"
)

// DefaultName is where a folder's archive goes when no output path is given:
// beside the folder, named after it, so MyAssets becomes MyAssets.pack.
func DefaultName(root string) string {
	root = strings.TrimSuffix(root, string(filepath.Separator))
	return root + ".pack"
}

// SamePath reports whether two paths name the same file. Windows paths are
// case-insensitive and the drive letter is optional, so this errs toward
// "same" rather than being an exact string match.
func SamePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ca, err1 := filepath.Abs(a)
	cb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(ca), filepath.Clean(cb))
}

// collect walks root and returns everything that can be stored in a pack,
// sorted by name so two runs over the same folder produce identical output.
//
// Names that cannot be represented are returned separately rather than
// failing the whole run, mirroring how extraction treats a bad entry: one
// unstorable name should not cost you the other two hundred files.
func collect(root, exclude string) (sources []format.Source, rejected []string, err error) {
	root = filepath.Clean(root)
	dirs := 0

	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() {
			// A reparse point can point back up the tree. Following it would
			// pack the same bytes twice, or walk in a circle.
			if p != root && d.Type()&fs.ModeSymlink != 0 {
				return filepath.SkipDir
			}
			dirs++
			return nil
		}

		// Only regular files. A directory entry of some other kind, such as a
		// socket or a device node, has no size worth storing and no bytes to
		// read back.
		if !d.Type().IsRegular() {
			return nil
		}

		// Never pack the archive we are in the middle of writing, or a stale
		// one from a previous run sitting in the folder.
		if SamePath(p, exclude) {
			return nil
		}

		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		// The pack stores names as forward slashes relative to the root, so a
		// subfolder arrives as a/b/c.txt and the folder structure survives the
		// round trip intact.
		name := filepath.ToSlash(rel)
		if nameErr := format.ValidateName(name); nameErr != nil {
			rejected = append(rejected, fmt.Sprintf("%s: %v", name, nameErr))
			return nil
		}

		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		sources = append(sources, format.Source{Name: name, Path: p, Size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, rejected, err
	}

	// A walk that never descended found no files at all.
	if dirs <= 1 && len(sources) == 0 {
		return nil, rejected, fmt.Errorf("%s contains no files", filepath.Base(root))
	}

	sort.Slice(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })
	return sources, rejected, nil
}

// TotalSize is the summed payload size of a planned set of files, used for the
// "n files, x MB" line before anything is written.
func TotalSize(sources []format.Source) int64 {
	var n int64
	for _, s := range sources {
		n += s.Size
	}
	return n
}

// Folder packs root into a .pack at outPath, which may be empty to use the
// default name beside the folder. Rejected names are reported on stderr
// unless quiet, and counted in Stats.Failed.
//
// notice, if not nil, is called once the contents are known but before
// anything is written, so the caller can print what is about to be packed
// without this package having to know how output is formatted. That ordering
// matters: a run that fails partway through should still have said what it
// was working on.
func Folder(root, outPath string, opts job.Options, prog job.Progress, notice func(name string, files int, bytes int64)) job.Result {
	res := job.Result{Input: root}
	start := time.Now()

	absRoot, err := filepath.Abs(root)
	if err != nil {
		absRoot = root
	}

	if outPath == "" {
		outPath = DefaultName(absRoot)
	} else if absOut, err := filepath.Abs(outPath); err == nil {
		outPath = absOut
	}
	res.Output = outPath

	// The archive is collected around, not by reading itself.
	sources, rejected, err := collect(absRoot, outPath)
	if err != nil {
		res.Err = fmt.Errorf("%s: %w", filepath.Base(absRoot), err)
		res.Stats.Elapsed = time.Since(start)
		return res
	}
	res.Stats.Files = len(sources)
	res.Stats.Failed = len(rejected)

	if notice != nil {
		notice(filepath.Base(absRoot), len(sources), TotalSize(sources))
	}

	// A file the format cannot carry is a real problem, so it is named rather
	// than quietly dropped, but it does not stop the rest of the pack.
	if !opts.Quiet {
		for _, r := range rejected {
			fmt.Fprintf(os.Stderr, "  SKIP %s\n", r)
		}
	}

	if len(sources) == 0 {
		res.Err = fmt.Errorf("%s contains no files to pack", filepath.Base(absRoot))
		res.Stats.Elapsed = time.Since(start)
		return res
	}

	written, err := format.Write(outPath, sources, func(done, total int) {
		prog.Entry(done, total)
	})
	if err != nil {
		res.Err = fmt.Errorf("%s: %w", filepath.Base(outPath), err)
		res.Stats.Elapsed = time.Since(start)
		return res
	}

	res.Stats.Written = written
	res.Stats.Elapsed = time.Since(start)
	return res
}
