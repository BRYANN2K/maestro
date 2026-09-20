//go:build windows

// Package windowsjob runs a command in a Windows Job Object whose complete
// process tree is terminated when the job handle closes.
package windowsjob

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Controller owns one started command and its kill-on-close Job Object. It is
// suitable for streaming commands whose stdin/stdout pipes must remain live
// between Start and Wait.
type Controller struct {
	cmd        *exec.Cmd
	job        *killOnCloseJob
	rootExited <-chan struct{}
	waitOnce   sync.Once
	waitDone   chan struct{}
	waitErr    error
}

// Start starts cmd suspended, assigns it to a kill-on-close Job Object, then
// resumes it. Starting suspended closes the otherwise unavoidable race in
// which the new process could create descendants before assignment.
//
// cmd must have been created with exec.CommandContext: Start installs a Cancel
// function which closes the job and therefore terminates every descendant.
// The caller must eventually call Wait, just as it would after exec.Cmd.Start.
func Start(cmd *exec.Cmd, waitDelay time.Duration) (*Controller, error) {
	if cmd == nil {
		return nil, errors.New("windows job: nil command")
	}

	job, err := newKillOnCloseJob()
	if err != nil {
		return nil, err
	}
	started := false
	defer func() {
		if !started {
			_ = job.Close()
		}
	}()
	controller := &Controller{cmd: cmd, job: job, waitDone: make(chan struct{})}

	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED
	setupDone := make(chan struct{})
	finishSetup := sync.OnceFunc(func() { close(setupDone) })
	defer finishSetup()
	cmd.Cancel = func() error {
		// CommandContext may observe cancellation immediately after Start. Keep
		// its cancellation goroutine from killing and releasing the suspended
		// root PID while job assignment and thread discovery are in progress.
		<-setupDone
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return controller.Kill()
	}
	cmd.WaitDelay = waitDelay

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		finishSetup()
		return nil, abortSuspendedCommand(cmd, job, fmt.Errorf("open process for job assignment: %w", err))
	}

	if err := windows.AssignProcessToJobObject(job.Handle(), process); err != nil {
		_ = windows.CloseHandle(process)
		finishSetup()
		return nil, abortSuspendedCommand(cmd, job, fmt.Errorf("assign process to job: %w", err))
	}
	if err := resumeProcessThreads(uint32(cmd.Process.Pid)); err != nil {
		_ = windows.CloseHandle(process)
		finishSetup()
		return nil, abortSuspendedCommand(cmd, job, fmt.Errorf("resume process in job: %w", err))
	}
	finishSetup()

	// A direct child can exit while a background descendant still owns an
	// inherited stdout/stderr handle. Close the job as soon as the root exits,
	// so cmd.Wait does not have to wait for that descendant or WaitDelay.
	rootExited := make(chan struct{})
	controller.rootExited = rootExited
	go func() {
		_, _ = windows.WaitForSingleObject(process, windows.INFINITE)
		_ = job.Close()
		_ = windows.CloseHandle(process)
		close(rootExited)
	}()
	started = true
	return controller, nil
}

// Wait reaps the root process. It is safe to call more than once; every caller
// receives the same result after the single underlying exec.Cmd.Wait call.
func (c *Controller) Wait() error {
	if c == nil || c.cmd == nil {
		return os.ErrProcessDone
	}
	c.waitOnce.Do(func() {
		c.waitErr = c.cmd.Wait()
		_ = c.job.Close()
		if c.rootExited != nil {
			<-c.rootExited
		}
		close(c.waitDone)
	})
	<-c.waitDone
	return c.waitErr
}

// Kill closes the Job Object, terminating the root process and all descendants.
// Process.Kill is a fail-safe for the unlikely event that closing the valid job
// handle fails.
func (c *Controller) Kill() error {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return os.ErrProcessDone
	}
	if err := c.job.Close(); err != nil {
		killErr := c.cmd.Process.Kill()
		if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			return errors.Join(err, killErr)
		}
		return err
	}
	return nil
}

// Run is the synchronous convenience form of Start followed by Wait.
func Run(cmd *exec.Cmd, waitDelay time.Duration) error {
	controller, err := Start(cmd, waitDelay)
	if err != nil {
		return err
	}
	return controller.Wait()
}

type killOnCloseJob struct {
	handle windows.Handle
	once   sync.Once
	err    error
}

func newKillOnCloseJob() (*killOnCloseJob, error) {
	handle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create Windows job object: %w", err)
	}
	job := &killOnCloseJob{handle: handle}

	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		handle,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		_ = job.Close()
		return nil, fmt.Errorf("configure Windows job object: %w", err)
	}
	return job, nil
}

func (j *killOnCloseJob) Handle() windows.Handle {
	if j == nil {
		return 0
	}
	return j.handle
}

func (j *killOnCloseJob) Close() error {
	if j == nil {
		return nil
	}
	j.once.Do(func() {
		if j.handle != 0 {
			j.err = windows.CloseHandle(j.handle)
		}
	})
	return j.err
}

func abortSuspendedCommand(cmd *exec.Cmd, job *killOnCloseJob, cause error) error {
	// Closing an assigned job kills the whole tree. Process.Kill is required
	// when assignment itself failed and the suspended root is not in the job.
	_ = job.Close()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	return cause
}

func resumeProcessThreads(processID uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		return err
	}

	resumed := 0
	for {
		if entry.OwnerProcessID == processID {
			thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if openErr != nil {
				return openErr
			}
			_, resumeErr := windows.ResumeThread(thread)
			closeErr := windows.CloseHandle(thread)
			if resumeErr != nil {
				return resumeErr
			}
			if closeErr != nil {
				return closeErr
			}
			resumed++
		}

		err = windows.Thread32Next(snapshot, &entry)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			break
		}
		if err != nil {
			return err
		}
	}
	if resumed == 0 {
		return errors.New("no suspended process thread found")
	}
	return nil
}
