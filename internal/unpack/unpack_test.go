package unpack

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"packunpacker/internal/format"
	"packunpacker/internal/job"
)

func TestSafeJoin(t *testing.T) {
	root := filepath.Join("C:", "out")

	ok := []struct {
		name string
		want string
	}{
		{"model.obj", filepath.Join(root, "model.obj")},
		{"textures/a.png", filepath.Join(root, "textures", "a.png")},
		{`textures\b.png`, filepath.Join(root, "textures", "b.png")},
		{"./model.obj", filepath.Join(root, "model.obj")},
		{"a//b//c.obj", filepath.Join(root, "a", "b", "c.obj")},
	}
	for _, tc := range ok {
		got, err := SafeJoin(root, tc.name)
		if err != nil {
			t.Errorf("SafeJoin(%q) unexpected error: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("SafeJoin(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}

	// Names are data from the pack file. None of these may escape root, and
	// some of them must be rejected outright.
	bad := []string{
		`..\..\..\Windows\System32\evil.dll`,
		"../../../etc/passwd",
		`C:\Windows\System32\evil.dll`,
		"/etc/passwd",
		`\absolute\evil.dll`,
		"",
		"..",
		".",
		"/",
	}
	for _, name := range bad {
		got, err := SafeJoin(root, name)
		if err == nil {
			// It may be clamped into root rather than rejected, but it must
			// never land outside.
			rel, relErr := filepath.Rel(root, got)
			if relErr != nil || rel == ".." || len(rel) > 2 && rel[:3] == ".."+string(filepath.Separator) {
				t.Errorf("SafeJoin(%q) = %q which escapes root", name, got)
			}
			continue
		}
		if got != "" {
			t.Errorf("SafeJoin(%q) returned both an error and %q", name, got)
		}
	}
}

// buildPack assembles a small valid pack so extraction can be tested without
// shipping a binary fixture in the repo.
func buildPack(entries map[string][]byte) []byte {
	// Names are sorted so the fixture is deterministic despite map ordering.
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)

	// Payloads sit after the directory chunk, the layout real packs use, so
	// compute the header size first.
	headerSize := 8
	for _, name := range names {
		headerSize += 4 + len(name) + 12
	}

	dataOffset := headerSize
	dir := make([]byte, 0, headerSize)
	dir = append(dir, 0, 0, 0, 0) // nextChunkOffset: 0 terminates
	var count [4]byte
	binary.BigEndian.PutUint32(count[:], uint32(len(names)))
	dir = append(dir, count[:]...)

	for _, name := range names {
		data := entries[name]

		var nl [4]byte
		binary.BigEndian.PutUint32(nl[:], uint32(len(name)))
		dir = append(dir, nl[:]...)
		dir = append(dir, name...)

		var meta [12]byte
		binary.BigEndian.PutUint32(meta[0:4], uint32(dataOffset))
		binary.BigEndian.PutUint32(meta[4:8], uint32(len(data)))
		binary.BigEndian.PutUint32(meta[8:12], crc32.ChecksumIEEE(data))
		dir = append(dir, meta[:]...)

		dataOffset += len(data)
	}

	buf := &bytes.Buffer{}
	buf.Write(dir)
	for _, name := range names {
		buf.Write(entries[name])
	}
	return buf.Bytes()
}

func TestExtractRoundTrip(t *testing.T) {
	files := map[string][]byte{
		"a.txt":              []byte("hello"),
		"sub/dir/b.bin":      {0x00, 0x01, 0x02, 0xFF, 0xFE},
		"sub/empty.dat":      {},
		"weird name with.pb": []byte("spaces and . in name"),
	}
	raw := buildPack(files)

	idx, err := format.Read(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("format.Read: %v", err)
	}
	if len(idx.Entries) != len(files) {
		t.Fatalf("got %d entries, want %d", len(idx.Entries), len(files))
	}

	dir := t.TempDir()
	opts := job.Options{Force: true, Verify: true}
	st, err := Extract(bytes.NewReader(raw), idx, dir, opts, func(int, int, int64) {})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if st.Failed != 0 {
		t.Errorf("%d entries failed", st.Failed)
	}

	// Subfolder structure must survive, since a name carries its own path.
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestExtractDetectsCorruption(t *testing.T) {
	raw := buildPack(map[string][]byte{"a.txt": []byte("hello")})
	idx, err := format.Read(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}

	// Flip a byte of the payload. The directory is still readable, so only
	// the CRC check can catch this.
	corrupt := append([]byte(nil), raw...)
	corrupt[idx.Entries[0].Offset] ^= 0xFF

	dir := t.TempDir()
	st, err := Extract(bytes.NewReader(corrupt), idx, dir,
		job.Options{Force: true, Verify: true}, func(int, int, int64) {})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if st.Failed != 1 {
		t.Errorf("a corrupted payload passed verification: Failed = %d, want 1", st.Failed)
	}
	// The bad file must not be left behind looking like a good one.
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); err == nil {
		t.Error("a file that failed verification was left on disk")
	}
}

func TestExtractSkipsExistingOfSameSize(t *testing.T) {
	raw := buildPack(map[string][]byte{"a.txt": []byte("hello")})
	dir := t.TempDir()

	opts := job.Options{}
	run := func() *job.Stats {
		idx, err := format.Read(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			t.Fatalf("format.Read: %v", err)
		}
		st, err := Extract(bytes.NewReader(raw), idx, dir, opts, func(int, int, int64) {})
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		return st
	}

	first := run()
	if first.Written != 5 {
		t.Errorf("first run wrote %d bytes, want 5", first.Written)
	}
	second := run()
	if second.Written != 0 {
		t.Errorf("second run wrote %d bytes, want 0 (should skip)", second.Written)
	}
	if second.Skipped != 1 {
		t.Errorf("second run skipped %d, want 1", second.Skipped)
	}
}

func TestExtractDryRunWritesNothing(t *testing.T) {
	raw := buildPack(map[string][]byte{"a.txt": []byte("hello")})
	idx, err := format.Read(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "not-created")
	st, err := Extract(bytes.NewReader(raw), idx, dir,
		job.Options{DryRun: true}, func(int, int, int64) {})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if st.Files != 1 {
		t.Errorf("Files = %d, want 1 (the run should still report)", st.Files)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("a dry run created the output folder")
	}
}
