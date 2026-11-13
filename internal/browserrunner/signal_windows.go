//go:build windows

package browserrunner

import (
	"fmt"
	"os/exec"
	"syscall"
)

const (
	createNewProcessGroup = 0x00000200
	ctrlBreakEvent        = 0x00000001
)

var procGenerateConsoleCtrlEvent = syscall.NewLazyDLL("kernel32.dll").NewProc("GenerateConsoleCtrlEvent")

// platformSetup 让 Obscura 成为独立进程组组长，才能定向发送 CTRL_BREAK_EVENT。
func platformSetup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// platformGracefulSignal 向指定进程组发 CTRL_BREAK，Rust 运行时据此做 clean exit
// （flush --storage-dir 并回收 worker 子进程）。
func platformGracefulSignal(pid int) error {
	r, _, e := procGenerateConsoleCtrlEvent.Call(uintptr(ctrlBreakEvent), uintptr(pid))
	if r == 0 {
		return fmt.Errorf("GenerateConsoleCtrlEvent(%d): %v", pid, e)
	}
	return nil
}
