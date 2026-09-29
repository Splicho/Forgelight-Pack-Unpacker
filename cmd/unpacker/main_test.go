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

// TestUnpackerRoundTrip mirrors the packer's own end-to-end test: build an
// archive with internal/pack, then extract it through the real command line
// path and check the tree comes back intact.
func TestUnpackerRoundTrip(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "Assets_027")
	writeTree(t, src, map[string][]byte{
		"manifest.json":                     []byte(`{"v":1}`),
		"characters/heroes/male/head.dds":   []byte("malehead"),
		"characters/heroes/male/torso.obj":  []byte("maletorso"),
		"characters/heroes/female/head.dds": []byte("femhead"),
		"weapons/ak47/model.obj":            []byte("akmodel"),
		"weapons/ak47/icon.dds":             []byte("akicon"),
		"sounds/foley/reload.wav":           []byte("reload"),
		"empty.dat":                         {},
	})

	// Build the archive with the same writer the packer binary uses, so this
	// test stays on the extraction side.
	var sources []format.Source
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		sources = append(sources, format.Source{
			Name: filepath.ToSlash(rel), Path: p, Size: info.Size(),
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(root, "Assets_027.pack")
	if _, err := format.Write(archive, sources, nil); err != nil {
		t.Fatalf("format.Write: %v", err)
	}

	// Extract to a different root so the output does not land on top of the
	// source tree the comparison reads from.
	outRoot := filepath.Join(root, "extracted")

	t.Run("extract into the chosen folder", func(t *testing.T) {
		overall, err := runJobs([]string{archive}, outRoot,
			job.Options{Force: true, Verify: true, Quiet: true}, job.NoProgress{})
		if err != nil {
			t.Fatalf("runJobs: %v", err)
		}
		if overall.Failed != 0 {
			t.Errorf("%d entries failed", overall.Failed)
		}
		if overall.Files != len(sources) {
			t.Errorf("unpacked %d files, want %d", overall.Files, len(sources))
		}
	})

	t.Run("the tree came back intact", func(t *testing.T) {
		// The folder inside outRoot is named after the pack, and its contents
		// must match the source byte for byte, structure included.
		assertTreesEqual(t, src, filepath.Join(outRoot, "Assets_027"))
	})
}

func TestUnpackerRejectsCorruptPack(t *testing.T) {
	// A file that is not a pack at all should fail loudly, not produce an
	// empty folder and a cheerful summary.
	dir := t.TempDir()
	bad := filepath.Join(dir, "Broken.pack")
	if err := os.WriteFile(bad, []byte("this is not a directory chain"), 0o644); err != nil {
		t.Fatal(err)
	}

	overall, err := runJobs([]string{bad}, "", job.Options{Quiet: true}, job.NoProgress{})
	if err == nil {
		t.Error("expected an error from a corrupt pack")
	}
	if overall == nil {
		t.Fatal("runJobs returned nil stats")
	}
}

func TestUnpackerSkipsNonPackInputs(t *testing.T) {
	// A stray file dropped on the exe should be passed over, and should not
	// stop a real pack in the same drop from being handled.
	dir := t.TempDir()
	stray := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(stray, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	good := filepath.Join(dir, "Good.pack")
	if _, err := format.Write(good, []format.Source{
		{Name: "a.txt", Path: writeFile(t, dir, "a.txt", []byte("hello")), Size: 5},
	}, nil); err != nil {
		t.Fatal(err)
	}

	overall, err := runJobs([]string{stray, good}, filepath.Join(dir, "out"),
		job.Options{Force: true, Verify: true, Quiet: true}, job.NoProgress{})
	if err != nil {
		t.Fatalf("a stray file should not be fatal: %v", err)
	}
	if overall.Files != 1 {
		t.Errorf("extracted %d files, want 1 (the stray must be passed over)", overall.Files)
	}

	got, err := os.ReadFile(filepath.Join(dir, "out", "Good", "a.txt"))
	if err != nil {
		t.Fatalf("the good pack was not extracted: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("extracted %q, want %q", got, "hello")
	}
}

func TestUnpackerResumesBySkippingExistingFiles(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "Repeat.pack")
	if _, err := format.Write(archive, []format.Source{
		{Name: "a.txt", Path: writeFile(t, dir, "a.txt", []byte("hello")), Size: 5},
	}, nil); err != nil {
		t.Fatal(err)
	}

	outRoot := filepath.Join(dir, "out")
	first, err := runJobs([]string{archive}, outRoot,
		job.Options{Quiet: true}, job.NoProgress{})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.Written != 5 {
		t.Errorf("first run wrote %d bytes, want 5", first.Written)
	}

	// Re-running should be cheap: the file is already the right size, so it is
	// skipped rather than rewritten.
	second, err := runJobs([]string{archive}, outRoot,
		job.Options{Quiet: true}, job.NoProgress{})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.Written != 0 {
		t.Errorf("second run wrote %d bytes, want 0 (it should skip)", second.Written)
	}
	if second.Skipped != 1 {
		t.Errorf("second run skipped %d, want 1", second.Skipped)
	}
}

func TestUnpackerNoInputs(t *testing.T) {
	overall, err := runJobs(nil, "", job.Options{Quiet: true}, job.NoProgress{})
	if err == nil {
		t.Error("expected an error when there is nothing to unpack")
	}
	if overall == nil {
		t.Fatal("runJobs returned nil stats")
	}
}

// writeFile writes data under dir and returns the path, for archive sources.
func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}
