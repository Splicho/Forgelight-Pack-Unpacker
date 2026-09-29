// Package format reads and writes the ForgeLight .pack directory format.
//
// The layout is big-endian throughout, and the file is a chain of directory
// chunks. Each chunk points at the next one; a next offset of 0 ends the
// chain. Within a chunk, a file count is followed by that many entries.
//
//	chunk @ offset:
//	  uint32  nextChunkOffset   0 terminates the chain
//	  uint32  fileCount
//	  fileCount x entry:
//	    uint32  nameLength
//	    char[]  name            ASCII, not NUL-terminated
//	    uint32  dataOffset      absolute, from start of file
//	    uint32  dataLength
//	    uint32  crc32
//
// This is a direct port of the reader the Node tools use, bounds checks and
// loud failures included. A format inferred rather than read from a spec is
// much better served by a hard error than by a listing that quietly drops or
// invents entries.
//
// Both directions live here. Reading a directory is a matter of following the
// chunk chain; writing one is harder, because every entry stores the absolute
// offset of its payload and its CRC is only known after the bytes have
// streamed past, while the directory itself comes first in the file. A writer
// therefore plans offsets from file sizes alone, reserves the directory's
// bytes, streams the payloads, and writes the directory over the reservation.
package format

import (
	"encoding/binary"
	"fmt"
	"io"
)

// MaxNameLength is the longest name we are willing to believe or to write.
// Real ones are far under this.
const MaxNameLength = 1024

// Entry is one file inside a pack.
type Entry struct {
	Name   string // as stored, forward slashes, relative to the pack root
	Offset int64  // absolute, from the start of the file
	Length int64
	CRC32  uint32
}

// Index is the parsed directory of a pack, plus a little bookkeeping the
// progress display wants.
type Index struct {
	Entries    []Entry
	ChunkCount int
	TotalBytes int64
}

// Read walks the chunk chain of r and returns every entry. size is the total
// length of r, used for bounds checking.
func Read(r io.ReaderAt, size int64) (*Index, error) {
	idx := &Index{}

	hdr := make([]byte, 8)
	meta := make([]byte, 12)
	name := make([]byte, MaxNameLength)
	visited := make(map[int64]bool)

	chunkOffset := int64(0)
	for {
		if visited[chunkOffset] {
			return nil, fmt.Errorf("chunk chain loops back to offset %d", chunkOffset)
		}
		visited[chunkOffset] = true

		if chunkOffset < 0 || chunkOffset+8 > size {
			return nil, fmt.Errorf("chunk header at %d runs past EOF (%d)", chunkOffset, size)
		}
		if _, err := r.ReadAt(hdr, chunkOffset); err != nil {
			return nil, fmt.Errorf("chunk header at %d: %w", chunkOffset, err)
		}

		next := binary.BigEndian.Uint32(hdr[0:4])
		fileCount := binary.BigEndian.Uint32(hdr[4:8])
		idx.ChunkCount++

		p := chunkOffset + 8

		for i := uint32(0); i < fileCount; i++ {
			if p+4 > size {
				return nil, fmt.Errorf("entry %d of chunk %d: name length past EOF", i, idx.ChunkCount)
			}
			if _, err := r.ReadAt(hdr[:4], p); err != nil {
				return nil, fmt.Errorf("entry %d of chunk %d: %w", i, idx.ChunkCount, err)
			}
			nameLen := int64(binary.BigEndian.Uint32(hdr[:4]))
			p += 4

			if nameLen == 0 || nameLen > MaxNameLength || p+nameLen+12 > size {
				return nil, fmt.Errorf("entry %d of chunk %d: implausible name length %d at %d",
					i, idx.ChunkCount, nameLen, p-4)
			}

			if _, err := r.ReadAt(name[:nameLen], p); err != nil {
				return nil, fmt.Errorf("entry %d of chunk %d: %w", i, idx.ChunkCount, err)
			}
			p += nameLen

			if _, err := r.ReadAt(meta, p); err != nil {
				return nil, fmt.Errorf("entry %d of chunk %d: %w", i, idx.ChunkCount, err)
			}
			dataOffset := int64(binary.BigEndian.Uint32(meta[0:4]))
			dataLength := int64(binary.BigEndian.Uint32(meta[4:8]))
			checksum := binary.BigEndian.Uint32(meta[8:12])
			p += 12

			if dataOffset < 0 || dataLength < 0 || dataOffset+dataLength > size {
				return nil, fmt.Errorf("'%s': data %d+%d runs past EOF (%d)",
					name[:nameLen], dataOffset, dataLength, size)
			}

			idx.Entries = append(idx.Entries, Entry{
				Name:   string(name[:nameLen]),
				Offset: dataOffset,
				Length: dataLength,
				CRC32:  checksum,
			})
			idx.TotalBytes += dataLength
		}

		if next == 0 {
			break
		}
		if int64(next) >= size {
			return nil, fmt.Errorf("nextChunkOffset %d past EOF (%d)", next, size)
		}
		chunkOffset = int64(next)
	}

	return idx, nil
}
