// Package unpack turns a .pack archive into a folder of files on disk.
package unpack

import (
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"

	"packunpacker/internal/format"
	"packunpacker/internal/job"
)

// SafeJoin resolves an entry name inside root and guarantees the result stays
// inside root. Entry names come from the pack file itself, so they are treated
// as untrusted: a name like "..\..\Startup\evil.exe" or an absolute
// "C:\Windows\x.dll" must not be able to write outside the output folder.
func SafeJoin(root, name string) (string, error) {
	// Names in the format are ASCII and use forward or backslashes depending on
	// the pack. Normalize to forward slashes so filepath.Clean understands
	// every separator regardless of the host OS.
	cleaned := strings.ReplaceAll(name, `\`, "/")
	cleaned = strings.TrimSpace(cleaned)

	// Strip drive letters and any leading slashes so the name is always
	// interpreted as relative to root.
	if len(cleaned) >= 2 && cleaned[1] == ':' {
		cleaned = cleaned[2:]
	}
	cleaned = strings.TrimLeft(cleaned, "/")
	if cleaned == "" {
		return "", fmt.Errorf("empty entry name")
	}

	// Drop any ".." components outright rather than relying on Clean alone.
	parts := strings.Split(cleaned, "/")
	var safe []string
	for _, p := range parts {
		switch p {
		case "", ".", "..":
			continue
		default:
			safe = append(safe, p)
		}
	}
	if len(safe) == 0 {
		return "", fmt.Errorf("entry name %q resolves to nothing", name)
	}

	target := filepath.Join(append([]string{root}, safe...)...)
	// Belt and braces: confirm the cleaned result is still under root.
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("entry name %q escapes the output folder", name)
	}
	return target, nil
}

// Extract unpacks the archive at packPath into outDir, which is created if
// needed. r must be the opened pack file and idx its parsed directory.
func Extract(r io.ReaderAt, idx *format.Index, outDir string, opts job.Options, progress func(done, total int, bytes int64)) (*job.Stats, error) {
	st := &job.Stats{}

	if !opts.DryRun {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return nil, fmt.Errorf("create output folder: %w", err)
		}
	}

	buf := make([]byte, 512*1024)

	for i, e := range idx.Entries {
		st.Files++
		target, err := SafeJoin(outDir, e.Name)
		if err != nil {
			st.Failed++
			if !opts.Quiet {
				fmt.Fprintf(os.Stderr, "  %s %s: %v\n", "SKIP", e.Name, err)
			}
			continue
		}

		if !opts.DryRun {
			// Resume support: if a file of the right size is already there, keep
			// it. Makes re-running after a cancel cheap instead of a full redo.
			if !opts.Force {
				if info, err := os.Stat(target); err == nil && info.Size() == e.Length {
					st.Skipped++
					progress(i+1, len(idx.Entries), st.Written)
					continue
				}
			}

			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				st.Failed++
				fmt.Fprintf(os.Stderr, "  FAIL %s: %v\n", e.Name, err)
				continue
			}

			if err := writeEntry(r, e, target, buf, opts.Verify); err != nil {
				st.Failed++
				fmt.Fprintf(os.Stderr, "  FAIL %s: %v\n", e.Name, err)
				// Clean up the partial file so a later re-run does not see a
				// wrong-sized file and skip it.
				os.Remove(target)
				continue
			}
			st.Written += e.Length
		}

		progress(i+1, len(idx.Entries), st.Written)
	}

	return st, nil
}

// writeEntry streams one entry to disk through a temp file, so an interrupted
// run never leaves a half-written file that a later run would treat as done.
func writeEntry(r io.ReaderAt, e format.Entry, target string, buf []byte, verify bool) error {
	tmp, err := os.CreateTemp(filepath.Dir(target), ".pu-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName) // no-op once the rename below succeeds
	}()

	remaining := e.Length
	pos := e.Offset
	var sum uint32

	for remaining > 0 {
		n := int64(len(buf))
		if remaining < n {
			n = remaining
		}
		if _, err := r.ReadAt(buf[:n], pos); err != nil {
			return err
		}
		if _, err := tmp.Write(buf[:n]); err != nil {
			return err
		}
		if verify {
			sum = crc32.Update(sum, crc32.IEEETable, buf[:n])
		}
		pos += n
		remaining -= n
	}

	if verify && sum != e.CRC32 {
		return fmt.Errorf("crc32 mismatch: got %08x want %08x", sum, e.CRC32)
	}

	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return err
	}
	return nil
}
