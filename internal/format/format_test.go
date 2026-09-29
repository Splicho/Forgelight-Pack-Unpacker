package format

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// buildPack assembles a small valid pack so the reader can be tested without
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

func TestReadRoundTrip(t *testing.T) {
	files := map[string][]byte{
		"a.txt":              []byte("hello"),
		"sub/dir/b.bin":      {0x00, 0x01, 0x02, 0xFF, 0xFE},
		"sub/empty.dat":      {},
		"weird name with.pb": []byte("spaces and . in name"),
	}
	raw := buildPack(files)

	idx, err := Read(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(idx.Entries) != len(files) {
		t.Fatalf("got %d entries, want %d", len(idx.Entries), len(files))
	}
	if idx.ChunkCount != 1 {
		t.Errorf("ChunkCount = %d, want 1", idx.ChunkCount)
	}

	// The stored offsets must actually point at the right bytes.
	for _, e := range idx.Entries {
		got := raw[e.Offset : e.Offset+e.Length]
		if want := files[e.Name]; !bytes.Equal(got, want) {
			t.Errorf("%s: bytes at its recorded offset do not match", e.Name)
		}
	}
}

func TestReadRejectsGarbage(t *testing.T) {
	// A genuine cycle. Note 0 is the end-of-chain marker, so a cycle cannot
	// use it as a "next" target: 0 -> 8 -> 16 -> 8 revisits offset 8 and must
	// trip the loop guard.
	cycle := make([]byte, 24)
	binary.BigEndian.PutUint32(cycle[0:4], 8)   // chunk 0  -> chunk 8
	binary.BigEndian.PutUint32(cycle[4:8], 0)   // 0 files
	binary.BigEndian.PutUint32(cycle[8:12], 16) // chunk 8  -> chunk 16
	binary.BigEndian.PutUint32(cycle[12:16], 0) // 0 files
	binary.BigEndian.PutUint32(cycle[16:20], 8) // chunk 16 -> chunk 8 (cycle)
	binary.BigEndian.PutUint32(cycle[20:24], 0) // 0 files

	cases := map[string][]byte{
		"empty":                {},
		"short header":         {0, 0, 0},
		"huge file count":      {0, 0, 0, 0, 0xFF, 0xFF, 0xFF, 0xFF},
		"chunk cycle":          cycle,
		"next offset past eof": {0xFF, 0xFF, 0xFF, 0xFF, 0, 0, 0, 0},
	}
	for label, raw := range cases {
		if _, err := Read(bytes.NewReader(raw), int64(len(raw))); err == nil {
			t.Errorf("%s: Read accepted it, want an error", label)
		}
	}
}

func TestValidateName(t *testing.T) {
	good := []string{"a.txt", "sub/dir/file.dds", "UPPER.TxT", "with space.dat", "~tmp"}
	for _, n := range good {
		if err := ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", n, err)
		}
	}

	// Names travel as raw ASCII, so anything outside printable ASCII would
	// come back as mojibake no matter what this tool writes.
	bad := []string{"", "naïve.txt", "emoji😀.txt", "line\nbreak", "tab\there"}
	for _, n := range bad {
		if err := ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q) = nil, want an error", n)
		}
	}

	if err := ValidateName(string(make([]byte, MaxNameLength+1))); err == nil {
		t.Errorf("ValidateName accepted a name of %d bytes, limit is %d", MaxNameLength+1, MaxNameLength)
	}
}

func TestWriteThenRead(t *testing.T) {
	dir := t.TempDir()
	files := map[string][]byte{
		"a.txt":          []byte("hello"),
		"sub/b.bin":      {0x00, 0x01, 0xFF},
		"sub/deep/c.dat": bytes.Repeat([]byte{0xAB}, 5000),
		"empty.dat":      {},
		"trailing pad..": []byte("awkward"),
		"big.bin":        bytes.Repeat([]byte{0x5A}, 100*1024),
	}

	var sources []Source
	for name, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, Source{Name: name, Path: p, Size: int64(len(data))})
	}

	out := filepath.Join(dir, "out.pack")
	if _, err := Write(out, sources, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}

	// The reader must accept our own output, and the checksums must be the
	// ones it recomputes from the same bytes.
	idx, err := Read(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("Read on our own output: %v", err)
	}
	if len(idx.Entries) != len(sources) {
		t.Fatalf("wrote %d entries, read back %d", len(sources), len(idx.Entries))
	}

	for _, e := range idx.Entries {
		want, ok := files[e.Name]
		if !ok {
			t.Errorf("unexpected entry %q", e.Name)
			continue
		}
		got := raw[e.Offset : e.Offset+e.Length]
		if !bytes.Equal(got, want) {
			t.Errorf("%s: payload does not match", e.Name)
		}
		if e.CRC32 != crc32.ChecksumIEEE(want) {
			t.Errorf("%s: stored CRC %08x, want %08x", e.Name, e.CRC32, crc32.ChecksumIEEE(want))
		}
	}
}

func TestWriteIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	names := []string{"b.txt", "a.txt", "c/d.txt", "c/a/e.txt"}
	for _, n := range names {
		p := filepath.Join(dir, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write := func() []byte {
		var sources []Source
		for _, n := range names {
			p := filepath.Join(dir, filepath.FromSlash(n))
			sources = append(sources, Source{Name: n, Path: p, Size: int64(len(n))})
		}
		sort.Slice(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })
		out := filepath.Join(dir, "det.pack")
		if _, err := Write(out, sources, nil); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	if first, second := write(), write(); !bytes.Equal(first, second) {
		t.Errorf("same input produced different archives (%d vs %d bytes)", len(first), len(second))
	}
}

func TestWriteRejectsOversized(t *testing.T) {
	// One file that cannot fit, and two that together cannot.
	cases := map[string][]Source{
		"single file too big": {{Name: "huge.bin", Size: MaxSize}},
		"folder too big": {
			{Name: "a.bin", Size: MaxSize},
			{Name: "b.bin", Size: 1024},
		},
	}
	for label, sources := range cases {
		out := filepath.Join(t.TempDir(), "x.pack")
		if _, err := Write(out, sources, nil); err == nil {
			t.Errorf("%s: Write accepted input that cannot fit in 4 GiB, want an error", label)
		}
	}
}

func TestWriteLeavesNoFileOnFailure(t *testing.T) {
	// A source pointing at a file that does not exist fails partway through.
	// The temp file must be cleaned up and the target must not appear, so a
	// later run cannot mistake a partial archive for a real one.
	dir := t.TempDir()
	out := filepath.Join(dir, "should-not-exist.pack")

	_, err := Write(out, []Source{
		{Name: "ghost.bin", Path: filepath.Join(dir, "nope"), Size: 10},
	}, nil)
	if err == nil {
		t.Fatal("Write succeeded with a missing source, want an error")
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Error("Write left the output file behind after failing")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "nope" {
			t.Errorf("Write left a temp file behind: %s", e.Name())
		}
	}
}

func TestWriteChainTerminates(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "only.txt")
	if err := os.WriteFile(p, []byte("solo"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "one.pack")
	if _, err := Write(out, []Source{{Name: "only.txt", Path: p, Size: 4}}, nil); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if next := binary.BigEndian.Uint32(raw[0:4]); next != 0 {
		t.Errorf("nextChunkOffset = %d, want 0 to terminate the chain", next)
	}
	if count := binary.BigEndian.Uint32(raw[4:8]); count != 1 {
		t.Errorf("fileCount = %d, want 1", count)
	}
}

func TestWriteDirectoryIsSelfConsistent(t *testing.T) {
	dir := t.TempDir()
	var sources []Source
	for _, n := range []string{"a.bin", "b.bin", "c.bin", "d/e.bin"} {
		p := filepath.Join(dir, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, bytes.Repeat([]byte{1}, 100), 0o644); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, Source{Name: n, Path: p, Size: 100})
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })

	out := filepath.Join(dir, "consistent.pack")
	if _, err := Write(out, sources, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	size := int64(len(raw))

	idx, err := Read(bytes.NewReader(raw), size)
	if err != nil {
		t.Fatal(err)
	}

	// Recompute the directory extent from the entries themselves.
	var dirEnd int64 = 8
	for _, e := range idx.Entries {
		dirEnd += 4 + int64(len(e.Name)) + 12
	}

	spans := make([][2]int64, 0, len(idx.Entries))
	for _, e := range idx.Entries {
		if e.Offset < dirEnd {
			t.Errorf("%s starts at %d, inside the directory which ends at %d",
				e.Name, e.Offset, dirEnd)
		}
		if e.Offset+e.Length > size {
			t.Errorf("%s ends at %d, past the %d byte file", e.Name, e.Offset+e.Length, size)
		}
		spans = append(spans, [2]int64{e.Offset, e.Offset + e.Length})
	}

	// Pairwise overlap check. Fine at pack scale: the real packs hold a few
	// hundred entries, and this is a correctness test, not a hot path.
	for i := 0; i < len(spans); i++ {
		for j := i + 1; j < len(spans); j++ {
			if spans[i][0] < spans[j][1] && spans[j][0] < spans[i][1] {
				t.Errorf("entries %d and %d overlap: [%d,%d) and [%d,%d)",
					i, j, spans[i][0], spans[i][1], spans[j][0], spans[j][1])
			}
		}
	}
}
