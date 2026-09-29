//go:build !windows

package console

// The stdout console handle off Windows. Nothing here has a Windows console
// handle, and enableAnsi ignores it anyway, so the OS's conventional stdout
// file descriptor is as good a value as any.

func stdoutHandle() uintptr { return 1 }
