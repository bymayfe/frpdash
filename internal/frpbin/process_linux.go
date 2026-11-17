package frpbin

import (
	"os/exec"
	"syscall"
)

// killWithParent makes the kernel stop the child if frpdash dies without
// cleaning up (crash, SIGKILL), so an orphaned frps/frpc never keeps holding
// its ports.
func killWithParent(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
