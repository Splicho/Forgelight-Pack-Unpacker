//go:build windows

package console

// Console mode access on Windows, used to turn on ANSI color interpretation.

import (
	"syscall"
	"unsafe"
)

var (
	kernel32        = syscall.NewLazyDLL("kernel32.dll")
	pGetConsoleMode = kernel32.NewProc("GetConsoleMode")
	pSetConsoleMode = kernel32.NewProc("SetConsoleMode")
)

// enableAnsi turns on virtual terminal processing for a console handle, which
// is what makes the host interpret ANSI escape sequences. This is where the
// mode query lives, because only Windows has a mode to query and set.
func enableAnsi(handle uintptr) bool {
	var mode uint32
	r, _, _ := pGetConsoleMode.Call(handle, uintptr(unsafe.Pointer(&mode)))
	if r == 0 {
		return false
	}

	// ENABLE_VIRTUAL_TERMINAL_PROCESSING must be set on the output handle for
	// the console host to translate ANSI sequences into console output.
	const enableVirtualTerminalProcessing = 0x0004

	r, _, _ = pSetConsoleMode.Call(handle, uintptr(mode|enableVirtualTerminalProcessing))
	return r != 0
}
