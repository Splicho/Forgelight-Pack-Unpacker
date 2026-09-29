package pack

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"packunpacker/internal/format"
	"packunpacker/internal/job"
)

// writeTree materializes files under root, creating parent folders as needed.
// Names use forward slashes regardless of host so the tests mean the same
// thing everywhere.
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

func TestFolderPreservesStructure(t *testing.T) {
	files := map[string][]byte{
		"manifest.json":                     []byte(`{"v":1}`),
		"characters/heroes/male/head.dds":   []byte("malehead"),
		"characters/heroes/female/head.dds": []byte("femhead"),
		"weapons/ak47/model.obj":            []byte("ak"),
		"weapons/ak47/icon.dds":             []byte("akicon"),
		"sounds/reload.wav":                 []byte("reload"),
	}
	src := t.TempDir()
	writeTree(t, src, files)

	out := filepath.Join(t.TempDir(), "tree.pack")
	res := Folder(src, out, job.Options{Quiet: true}, job.NoProgress{}, nil)
	if res.Err != nil {
		t.Fatalf("Folder: %v", res.Err)
	}
	if res.Stats.Failed != 0 {
		t.Errorf("%d files were skipped", res.Stats.Failed)
	}

	// The pack must be readable by the real reader, and every name must carry
	// its folder so the structure comes back.
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := format.Read(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("the archive we wrote is not readable: %v", err)
	}

	got := map[string]bool{}
	for _, e := range idx.Entries {
		got[e.Name] = true
		if want := files[e.Name]; !bytes.Equal(raw[e.Offset:e.Offset+e.Length], want) {
			t.Errorf("%s: payload does not match", e.Name)
		}
	}
	for name := range files {
		if !got[name] {
			t.Errorf("%s is missing from the pack", name)
		}
	}
}

func TestFolderRoundTrips(t *testing.T) {
	files := map[string][]byte{
		"a.txt":              []byte("alpha"),
		"sub/b.txt":          []byte("beta"),
		"sub/deep/c.txt":     []byte("gamma"),
		"empty.dat":          {},
		"binary.bin":         {0, 1, 2, 254, 255},
		"trailing space..":   []byte("awkward"),
		"UPPER_MiXeD.TxT":    []byte("case is preserved"),
		"spaces and (paren)": []byte("awkward, but legal"),
	}
	src := t.TempDir()
	writeTree(t, src, files)

	out := filepath.Join(t.TempDir(), "rt.pack")
	res := Folder(src, out, job.Options{Quiet: true}, job.NoProgress{}, nil)
	if res.Err != nil {
		t.Fatalf("Folder: %v", res.Err)
	}

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := format.Read(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("read our own output: %v", err)
	}

	back := t.TempDir()
	for _, e := range idx.Entries {
		p := filepath.Join(back, filepath.FromSlash(e.Name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, raw[e.Offset:e.Offset+e.Length], 0o644); err != nil {
			t.Fatal(err)
		}
	}

	assertTreesEqual(t, src, back)
}

func TestFolderDefaultName(t *testing.T) {
	root := t.TempDir()
	assets := filepath.Join(root, "MyAssets")
	writeTree(t, assets, map[string][]byte{"a.txt": []byte("a")})

	// An empty outPath means the default: MyAssets.pack beside the folder.
	res := Folder(assets, "", job.Options{Quiet: true}, job.NoProgress{}, nil)
	if res.Err != nil {
		t.Fatalf("Folder: %v", res.Err)
	}
	want := filepath.Join(root, "MyAssets.pack")
	if !SamePath(res.Output, want) {
		t.Errorf("wrote %q, want %q", res.Output, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected the archive at %s: %v", want, err)
	}
}

func TestFolderExcludesItself(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string][]byte{"a.txt": []byte("a")})

	// Output inside the source folder: it is created by the run, so the
	// collection pass must not see it as input.
	out := filepath.Join(src, "self.pack")
	res := Folder(src, out, job.Options{Quiet: true}, job.NoProgress{}, nil)
	if res.Err != nil {
		t.Fatalf("Folder: %v", res.Err)
	}
	if res.Stats.Files != 1 {
		t.Errorf("packed %d files, want 1 (the output archive must be excluded)", res.Stats.Files)
	}

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := format.Read(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Entries) != 1 || idx.Entries[0].Name != "a.txt" {
		t.Errorf("archive contains %d entries, first = %q", len(idx.Entries), idx.Entries[0].Name)
	}
}

func TestFolderIsDeterministic(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string][]byte{
		"b.txt":     []byte("bee"),
		"a.txt":     []byte("ay"),
		"c/d.txt":   []byte("dee"),
		"c/a/e.txt": []byte("eee"),
	})

	pack := func() []byte {
		out := filepath.Join(t.TempDir(), "det.pack")
		if _, err := format.Write(out, mustCollect(t, src, out), nil); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	if first, second := pack(), pack(); !bytes.Equal(first, second) {
		t.Errorf("same folder produced different archives (%d vs %d bytes)", len(first), len(second))
	}
}

// mustCollect exposes the package's collection step for a direct format.Write,
// so the determinism test checks the writer given identical input order.
func mustCollect(t *testing.T, root, exclude string) []format.Source {
	t.Helper()
	sources, _, err := collect(root, exclude)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	return sources
}

func TestFolderSkipsUnstorableNames(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string][]byte{
		"ok.txt":     []byte("fine"),
		"naïve.txt":  []byte("accented"),
		"emoji😀.txt": []byte("emoji"),
	})

	res := Folder(src, filepath.Join(t.TempDir(), "x.pack"),
		job.Options{Quiet: true}, job.NoProgress{}, nil)
	if res.Err != nil {
		t.Fatalf("Folder: %v", res.Err)
	}
	if res.Stats.Files != 1 {
		t.Errorf("packed %d files, want 1 (only the ASCII name is storable)", res.Stats.Files)
	}
	if res.Stats.Failed != 2 {
		t.Errorf("reported %d skipped names, want 2", res.Stats.Failed)
	}
}

func TestFolderNoticeFiresBeforeWriting(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string][]byte{"a.txt": []byte("a"), "b.txt": []byte("b")})
	out := filepath.Join(t.TempDir(), "n.pack")

	var gotName string
	var gotFiles int
	var existedAtNotice bool

	res := Folder(src, out, job.Options{Quiet: true}, job.NoProgress{},
		func(name string, files int, bytes int64) {
			gotName, gotFiles = name, files
			_, err := os.Stat(out)
			existedAtNotice = err == nil
		})
	if res.Err != nil {
		t.Fatalf("Folder: %v", res.Err)
	}

	if gotName == "" || gotFiles != 2 {
		t.Errorf("notice got name=%q files=%d, want the folder name and 2 files", gotName, gotFiles)
	}
	// The notice is printed so the user knows what is being worked on, which
	// means it has to come before the archive exists.
	if existedAtNotice {
		t.Error("notice fired after the archive was already written")
	}
}

func TestFolderEmptyIsAnError(t *testing.T) {
	res := Folder(t.TempDir(), filepath.Join(t.TempDir(), "x.pack"),
		job.Options{Quiet: true}, job.NoProgress{}, nil)
	if res.Err == nil {
		t.Error("packing an empty folder should fail loudly, not produce an empty archive")
	}
}

func TestSamePathIsCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "Thing.pack")
	if !SamePath(a, strings.ToUpper(a)) {
		t.Error("SamePath should treat case-only differences as the same file on Windows")
	}
	if SamePath(a, filepath.Join(dir, "other.pack")) {
		t.Error("SamePath reported two different files as the same")
	}
}
