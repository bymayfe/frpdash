//go:build !linux

package frp

import "os/exec"

func killWithParent(*exec.Cmd) {}
