# ForgeLight .pack unpack-packer

Two small Windows tools for ForgeLight `.pack` asset archives. One takes a
folder apart, the other puts it back together.

```
unpacker.exe    Assets_027.pack  ->  Assets_027\
packer.exe      Assets_027\      ->  Assets_027.pack
```

Drag and drop is the whole interface. Drop a `.pack` on `unpacker.exe` and it
becomes a folder of files beside it; drop a folder on `packer.exe` and it
becomes a `.pack` beside it. No arguments, no flags, no menu.

## Why two programs

The filename is the instruction. An earlier single binary worked out what a
drop meant from the file extension, which meant a mode flag, which meant the
two directions could be confused for one another. Splitting them removes the
guessing: a folder only ever goes to `packer.exe`, a `.pack` only ever to
`unpacker.exe`.

Each one also keeps only the flags that mean something for its direction.

## Install

Grab both `.exe` files from the releases and put them anywhere. They are
standalone binaries with no runtime to install.

To build from source you need [Go](https://go.dev) 1.23 or newer:

```powershell
git clone https://github.com/Splicho/pack-unpacker.git
cd pack-unpacker
.\build.ps1
```

That runs the tests, then writes `packer.exe` and `unpacker.exe` into the repo
root.

## Using it

Double-click either exe to see its options, or drop files straight onto it.

### unpacker.exe

```
usage: unpacker.exe [options] <pack.pack> [more.pack ...]

  -force     overwrite existing files instead of skipping matching ones
  -verify    check the CRC32 of every entry
  -o string  write into this folder instead of one named after each pack
  -dry-run   parse the packs and report, write nothing
  -quiet     only print the final summary
```

```powershell
unpacker.exe Assets_027.pack
unpacker.exe -verify *.pack
unpacker.exe -o D:\extracted Assets_027.pack
```

### packer.exe

```
usage: packer.exe [options] <folder> [more.folder ...]

  -o string  write the archives into this folder instead of beside each input
  -dry-run   report what would be packed, write nothing
  -quiet     only print the final summary
```

```powershell
packer.exe Assets_027
packer.exe -o D:\build Assets_027
```

### Exit codes

`0` when the run was clean, `1` when anything failed or was skipped, so a
script can branch on the result without reading the output.

## What it does when things go wrong

Nothing is left half-done. A file is streamed to a temporary name and renamed
into place only once it is complete and its CRC matches, and an archive is
built in a temporary file that replaces the target only on success. A run cut
short by a cancelled copy cannot leave behind a partial file that a later run
mistakes for a finished one.

Re-running is cheap. `unpacker.exe` skips any file that is already present at
the right size, so an interrupted extraction resumes rather than starting over.
Use `-force` to rewrite anyway.

## The format

`.pack` is a chain of directory chunks, big-endian throughout. Each chunk
points at the next, and a next offset of `0` ends the chain. Every entry carries
the absolute offset and length of its payload plus a CRC32:

```
chunk @ offset:
  uint32  nextChunkOffset   0 terminates the chain
  uint32  fileCount
  fileCount x entry:
    uint32  nameLength
    char[]  name            ASCII, not NUL-terminated
    uint32  dataOffset      absolute, from start of file
    uint32  dataLength
    uint32  crc32
```

Entry names are paths relative to the archive root and use forward slashes, so
subfolders survive a round trip intact. Names are stored as raw ASCII, which is
why `packer.exe` refuses to store a name containing anything else: the game
would read it back as mojibake. Such files are reported and skipped rather than
silently mangled.

Writing is harder than reading, because each entry stores an absolute offset
and its CRC is only known after the bytes have streamed past, while the
directory comes first in the file. `packer.exe` therefore plans the layout from
file sizes alone, reserves the directory's bytes, streams the payloads, and
writes the directory over the reservation. That keeps it to a single read pass
over the source, and makes the output deterministic: the same folder always
produces a byte-identical archive.

## Project layout

```
cmd/packer/       packer.exe: flags, summary, one call into internal/pack
cmd/unpacker/     unpacker.exe: flags, summary, one call into internal/unpack
internal/
  format/         the .pack container: read and write
  pack/           folder -> archive, and what to include
  unpack/         archive -> folder, and the path-traversal guard
  job/            Options, Stats and Progress shared by both directions
  cli/            the little that both front ends share
  console/        colors, progress bar, the pause before the window closes
```

Dependencies run one way. The front ends depend on the work packages, which
depend on the format, and nothing depends on a front end. `internal/format`
knows nothing about either direction: the reader and the writer sit side by
side as the two halves of one container format.

## Safety

Entry names come from the archive file itself and are treated as untrusted. A
name like `..\..\Startup\evil.dll` cannot write outside the output folder, and
an absolute path is treated as relative to it.

The progress bar only redraws itself on a real terminal, and colors turn
themselves off when output is redirected, so piping to a file or a log never
fills it with escape codes.

## Tests

```powershell
go test ./...
```

The suite builds small valid archives in memory rather than committing binary
fixtures, and covers the round trip, CRC detection, resume behavior, the path
traversal guard, determinism, and the 4 GiB format ceiling. The full test suite
runs in about a second.
