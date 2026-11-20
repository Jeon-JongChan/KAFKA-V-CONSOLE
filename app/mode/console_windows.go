//go:build windows

// Windows GUI(-H windowsgui) 바이너리에서 -console 출력을 보이게 한다.
package mode

import (
	"os"
	"syscall"
)

// AttachConsole 은 GUI 서브시스템에서도 표준입출력이 터미널에 붙게 한다.
func AttachConsole() {
	k32 := syscall.NewLazyDLL("kernel32.dll")
	attach := k32.NewProc("AttachConsole")
	alloc := k32.NewProc("AllocConsole")
	const attachParent = ^uintptr(0) // ATTACH_PARENT_PROCESS (-1)
	r, _, _ := attach.Call(attachParent)
	if r == 0 {
		_, _, _ = alloc.Call()
	}
	out, err := syscall.Open("CONOUT$", syscall.O_RDWR, 0)
	if err == nil {
		os.Stdout = os.NewFile(uintptr(out), "stdout")
		os.Stderr = os.NewFile(uintptr(out), "stderr")
	}
	in, err := syscall.Open("CONIN$", syscall.O_RDWR, 0)
	if err == nil {
		os.Stdin = os.NewFile(uintptr(in), "stdin")
	}
}
