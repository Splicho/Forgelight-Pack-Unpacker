//go:build windows

package console

// The stdout console handle, typed as Windows wants it.

import "syscall"

func stdoutHandle() uintptr { return uintptr(syscall.Stdout) }
