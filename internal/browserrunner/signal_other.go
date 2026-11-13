//go:build !windows

package browserrunner

import (
	"os"
	"os/exec"
	"syscall"
)

func platformSetup(cmd *exec.Cmd) {}

func platformGracefulSignal(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGTERM)
}
