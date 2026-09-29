package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"packunpacker/internal/format"
	"packunpacker/internal/job"
)

// writeTree materializes files under root, creating parent folders as needed.
func writeTree(t *testing.T, root string, files map[string][]byte) {
	t.Helper()
	for name, data := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

// assertTreesEqual compares two directory trees byte for byte.
func assertTreesEqual(t *testing.T, want, got string) {
	t.Helper()

	var walk func(rel string)
	walk = func(rel string) {
		entries, err := os.ReadDir(filepath.Join(want, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		for _, e := range entries {
			child := e.Name()
			if rel != "" {
				child = rel + "/" + child
			}
			if e.IsDir() {
				walk(child)
				continue
			}
			a, err := os.ReadFile(filepath.Join(want, filepath.FromSlash(child)))
			if err != nil {
				t.Fatalf("read want %s: %v", child, err)
			}
			b, err := os.ReadFile(filepath.Join(got, filepath.FromSlash(child)))
			if err != nil {
				t.Errorf("missing in repacked tree: %s (%v)", child, err)
				continue
			}
			if !bytes.Equal(a, b) {
				t.Errorf("%s differs: %d bytes vs %d", child, len(a), len(b))
			}
		}
	}
	walk("")
}

// TestRunJobsPacksPreservesStructure checks the packer's half of the round
// trip: the archive it writes has to be readable and has to carry every name
// with its folder intact, so unpacker.exe can put the tree back.
func TestRunJobsPacksPreservesStructure(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "Dropped")
	writeTree(t, src, map[string][]byte{
		"a.txt":                         []byte("alpha"),
		"sub/b.txt":                     []byte("beta"),
		"sub/deep/c.txt":                []byte("gamma"),
		"empty.dat":                     {},
		"binary.bin":                    {0, 1, 2, 254, 255},
		"trailing space..":              []byte("awkward"),
		"characters/hero/head.dds":      []byte("head"),
		"characters/hero/hand/hand.dds": []byte("hand"),
	})

	overall, err := runJobs([]string{src}, "", job.Options{Quiet: true}, job.NoProgress{})
	if err != nil {
		t.Fatalf("packer runJobs: %v", err)
	}
	if overall.Failed != 0 {
		t.Errorf("%d files were skipped", overall.Failed)
	}
	if overall.Files != 8 {
		t.Errorf("packed %d files, want 8", overall.Files)
	}

	// The archive lands beside the folder, named after it.
	archive := filepath.Join(root, "Dropped.pack")
	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatalf("expected the archive beside the folder: %v", err)
	}

	idx, err := format.Read(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("the archive we produced is not readable: %v", err)
	}
	if len(idx.Entries) != 8 {
		t.Fatalf("archive holds %d entries, want 8", len(idx.Entries))
	}

	// Every name must still be a path, not just a filename, and every payload
	// must sit where its entry claims.
	got := map[string]bool{}
	for _, e := range idx.Entries {
		got[e.Name] = true
		if want := readFile(t, filepath.Join(src, filepath.FromSlash(e.Name))); !bytes.Equal(
			raw[e.Offset:e.Offset+e.Length], want) {
			t.Errorf("%s: payload does not match the source", e.Name)
		}
	}
	for _, name := range []string{
		"sub/b.txt", "sub/deep/c.txt", "characters/hero/head.dds",
		"characters/hero/hand/hand.dds", "empty.dat",
	} {
		if !got[name] {
			t.Errorf("%s is missing from the archive, or lost its folder", name)
		}
	}
}

// readFile reads a file for comparison, failing the test if it is missing.
func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return b
}

func TestRunJobsReportsMissingFolder(t *testing.T) {
	// A folder that is not there is a failure the user needs told about, not a
	// silent no-op. It has to be counted so the exit code is non-zero, and the
	// message must describe that rather than claiming there was simply nothing
	// to pack.
	overall, err := runJobs([]string{filepath.Join(t.TempDir(), "Nope")}, "",
		job.Options{Quiet: true}, job.NoProgress{})
	if overall == nil {
		t.Fatal("runJobs returned nil stats")
	}
	if err == nil {
		t.Error("packing a folder that does not exist should be reported as an error")
	}
	if overall.Failed == 0 {
		t.Error("a missing folder was not counted as a failure, so the exit code would lie")
	}
	if overall.Files != 0 {
		t.Errorf("reported %d files packed, want 0", overall.Files)
	}
}

func TestRunJobsSkipsFilesItCannotUse(t *testing.T) {
	// A .pack is the other tool's job, and a text file is nothing at all.
	// Neither should be fatal, and neither should produce a misleading success.
	dir := t.TempDir()
	stray := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(stray, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	overall, err := runJobs([]string{stray}, "", job.Options{Quiet: true}, job.NoProgress{})
	if err == nil {
		t.Error("packing a plain file should report that there was nothing to do")
	}
	if overall.Files != 0 {
		t.Errorf("reported %d files packed, want 0", overall.Files)
	}
}

func TestRunJobsWithNoInputs(t *testing.T) {
	overall, err := runJobs(nil, "", job.Options{Quiet: true}, job.NoProgress{})
	if err == nil {
		t.Error("expected an error when there is nothing to pack")
	}
	if overall == nil {
		t.Fatal("runJobs returned nil stats")
	}
}
