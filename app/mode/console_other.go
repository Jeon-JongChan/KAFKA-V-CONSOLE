//go:build !windows

package mode

// AttachConsole 은 Windows 가 아니면 할 일이 없다 (이미 터미널에 붙어 있다).
func AttachConsole() {}
