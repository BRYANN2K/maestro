//go:build !unix

package rlm

import "os/exec"

func isolateKernel(cmd *exec.Cmd) {}
