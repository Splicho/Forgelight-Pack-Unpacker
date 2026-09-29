package format

// Writing a .pack.
//
// Writing is harder than reading, and the difficulty is ordering. Every entry
// stores the absolute offset of its payload, and the payload's CRC is only
// known after its bytes have streamed past. Both facts have to be settled
// before the directory can be written, and the directory comes first in the
// file. So the run is: lay out every entry from file sizes alone, reserve the
// directory's bytes, stream the payloads, and only then go back and write the
// directory over the reservation. That keeps it to a single read pass over the
// source files, which matters when the folder is a few gigabytes of assets.
//
// A real ForgeLight pack splits its directory across two chunks and leaves
// unreferenced bytes between payloads from earlier revisions. Neither is
// required to read a pack, and reproducing them would only make the output
// larger, so this writes one clean chunk and no dead space.

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"
)

const (
	// dataAlign is the boundary each payload starts on. Real packs put their
	// first payload byte at 8192 and leave gaps between blocks, so matching
	// that costs a little space and keeps the output shaped like something the
	// game has already read. Nothing in the format requires it: the reader
	// only ever follows the offsets it is given.
	dataAlign = 8192

	// MaxSize is the ceiling the format imposes on us. Offsets and lengths are
	// uint32, so a pack at or beyond 4 GiB would wrap and point entries at the
	// wrong bytes. Refusing loudly beats emitting a file that looks valid and
	// is not.
	MaxSize = int64(math.MaxUint32)

	// copyBufSize is the staging buffer for streaming payloads. Large enough
	// that the disk is the bottleneck rather than the syscalls.
	copyBufSize = 1 << 20
)

// Source is one file to be packed, and the layout slot assigned to it.
type Source struct {
	Name string // as it will be stored, forward slashes, relative to the root
	Path string // where the bytes are read from
	Size int64  // from stat, up front

	offset int64
	crc    uint32
}

// alignUp rounds n up to the next multiple of a.
func alignUp(n, a int64) int64 {
	return (n + a - 1) / a * a
}

// dirOf is filepath.Dir, wrapped so a path with no directory part still yields
// something os.CreateTemp can work with.
func dirOf(p string) string {
	d := filepath.Dir(p)
	if d == "" {
		return "."
	}
	return d
}

// crcSum returns a running IEEE CRC32 writer and the function to read its
// final value. Kept as a pair so the payload loop below stays readable.
func crcSum() (hash.Hash, func() uint32) {
	h := crc32.NewIEEE()
	return h, func() uint32 { return h.Sum32() }
}

// plan assigns every source its offset in the finished file and returns the
// size of the directory and of the whole pack. It reads sizes only; no file
// content is touched, so the expensive part of packing does not happen until
// the layout is already known to be sound.
func plan(sources []Source) (dirSize, totalSize int64, err error) {
	// One chunk: 8 bytes of header, then 4 + name + 12 per entry.
	dirSize = 8
	for _, s := range sources {
		dirSize += 4 + int64(len(s.Name)) + 12
	}

	// Payloads start past the directory, on a boundary, and never overlap it.
	cursor := alignUp(dirSize, dataAlign)
	if cursor < dataAlign {
		cursor = dataAlign
	}

	for i := range sources {
		if sources[i].Size > MaxSize {
			return 0, 0, fmt.Errorf("%s: %d bytes, a single file cannot exceed 4 GiB",
				sources[i].Name, sources[i].Size)
		}
		off := alignUp(cursor, dataAlign)
		if off+sources[i].Size > MaxSize {
			return 0, 0, fmt.Errorf(
				"packing this folder needs more than 4 GiB, which the format cannot address (%s ends at %d)",
				sources[i].Name, off+sources[i].Size)
		}
		sources[i].offset = off
		cursor = off + sources[i].Size
	}

	return dirSize, cursor, nil
}

// writeDirectory emits the single chunk: the chain terminator, the file count,
// and every entry with the offset and CRC that the payload pass settled.
func writeDirectory(w io.Writer, sources []Source) error {
	var u32 [4]byte
	put := func(v uint32) error {
		binary.BigEndian.PutUint32(u32[:], v)
		_, err := w.Write(u32[:])
		return err
	}

	// nextChunkOffset 0 ends the chain: everything fits in one chunk.
	if err := put(0); err != nil {
		return err
	}
	if err := put(uint32(len(sources))); err != nil {
		return err
	}

	for _, s := range sources {
		if err := put(uint32(len(s.Name))); err != nil {
			return err
		}
		if _, err := io.WriteString(w, s.Name); err != nil {
			return err
		}
		if err := put(uint32(s.offset)); err != nil {
			return err
		}
		if err := put(uint32(s.Size)); err != nil {
			return err
		}
		if err := put(s.crc); err != nil {
			return err
		}
	}
	return nil
}

// Write produces the archive at outPath from the given sources. progress, if
// not nil, is called after each file is written.
//
// The archive is written to a temp file beside the target and renamed into
// place only on success, so an interrupted run cannot leave a half-written
// pack that later looks like a valid one.
func Write(outPath string, sources []Source, progress func(done, total int)) (int64, error) {
	dirSize, totalSize, err := plan(sources)
	if err != nil {
		return 0, err
	}

	outDir := dirOf(outPath)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return 0, fmt.Errorf("create output folder: %w", err)
	}

	tmp, err := os.CreateTemp(outDir, ".pack-*.tmp")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpName) // no-op once the rename below succeeds
		}
	}()

	bw := bufio.NewWriterSize(tmp, copyBufSize)

	// Reserve the directory's bytes. They are written for real at the end,
	// once every offset and CRC is known.
	if _, err := bw.Write(make([]byte, dirSize)); err != nil {
		return 0, err
	}
	if err := bw.Flush(); err != nil {
		return 0, err
	}

	for i := range sources {
		s := &sources[i]

		// Land exactly on the offset the plan promised. The buffer has to be
		// empty first or the seek would leave its contents stranded mid-file.
		if err := bw.Flush(); err != nil {
			return 0, err
		}
		if _, err := tmp.Seek(s.offset, io.SeekStart); err != nil {
			return 0, err
		}

		src, err := os.Open(s.Path)
		if err != nil {
			return 0, fmt.Errorf("open %s: %w", s.Name, err)
		}
		sum, checksum := crcSum()
		n, copyErr := io.Copy(io.MultiWriter(bw, sum), src)
		src.Close()
		if copyErr != nil {
			return 0, fmt.Errorf("read %s: %w", s.Name, copyErr)
		}
		if n != s.Size {
			// The file changed between the stat that planned the layout and
			// now. Writing it anyway would spill into the next entry's space,
			// so stop rather than emit a pack whose offsets no longer describe
			// its contents.
			return 0, fmt.Errorf("%s changed while packing: expected %d bytes, read %d",
				s.Name, s.Size, n)
		}

		s.crc = checksum()
		if progress != nil {
			progress(i+1, len(sources))
		}
	}

	// Back to the front for the directory, now that every CRC exists.
	if err := bw.Flush(); err != nil {
		return 0, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	if err := writeDirectory(bw, sources); err != nil {
		return 0, err
	}
	if err := bw.Flush(); err != nil {
		return 0, err
	}

	// Pin the length to the planned size, so the archive is exactly as long as
	// its directory claims rather than however far the last write happened to
	// reach.
	if err := tmp.Truncate(totalSize); err != nil {
		return 0, err
	}
	if err := tmp.Sync(); err != nil {
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmpName, outPath); err != nil {
		return 0, err
	}
	committed = true

	return totalSize, nil
}

// ValidateName rejects names the format cannot carry. Names are stored as raw
// ASCII with no encoding field, so anything outside printable ASCII would be
// read back as mojibake by the game even though this tool would happily
// reproduce it byte for byte.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("empty name")
	}
	if len(name) > MaxNameLength {
		return fmt.Errorf("name is %d bytes, the format allows %d", len(name), MaxNameLength)
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; c < 0x20 || c > 0x7E {
			return fmt.Errorf("name contains a non-ASCII or control byte (0x%02x)", c)
		}
	}
	return nil
}
