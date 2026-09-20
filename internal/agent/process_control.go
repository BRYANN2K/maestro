package agent

import "os/exec"

type processWaiter interface {
	Wait() error
}

type commandWaiter struct{ cmd *exec.Cmd }

func (w commandWaiter) Wait() error { return w.cmd.Wait() }
