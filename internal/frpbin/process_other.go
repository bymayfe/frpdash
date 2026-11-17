//go:build !linux

package frpbin

import "os/exec"

func killWithParent(*exec.Cmd) {}
