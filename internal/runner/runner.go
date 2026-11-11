// Package runner starts external commands. A non-zero exit code is data, not an error: dpkg-query,
// dpkg --compare-versions and systemd-detect-virt answer through it. Err is set only when a command could not run.
package runner

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Err      error // the command could not be started or was killed; ExitCode is meaningless then
}

// Ran reports whether the command ran to an exit code.
func (r Result) Ran() bool { return r.Err == nil }

// Runner runs a command with additional environment variables.
type Runner interface {
	Run(ctx context.Context, env []string, name string, args ...string) Result
}

// Exec runs real commands.
type Exec struct{}

func (Exec) Run(ctx context.Context, env []string, name string, args ...string) Result {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	return result(cmd.Run(), &out, &errOut)
}

// Streamer runs a command and hands every line it writes to file descriptor 3 to onStatus while it runs (apt's
// APT::Status-Fd). Stdout and stderr are collected as with Run.
type Streamer interface {
	Stream(ctx context.Context, env []string, onStatus func(line string), name string, args ...string) Result
}

// pipeGrace: how long a status pipe may stay open after the command ended. A daemon that a maintainer script started
// can inherit it; the run must not wait for that daemon.
const pipeGrace = 5 * time.Second

func (Exec) Stream(ctx context.Context, env []string, onStatus func(line string), name string, args ...string) Result {
	r, w, err := os.Pipe()
	if err != nil {
		return Result{Err: err}
	}
	defer r.Close()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	cmd.ExtraFiles = []*os.File{w} // fd 3 in the child
	cmd.WaitDelay = pipeGrace      // the same for stdout and stderr
	if err := cmd.Start(); err != nil {
		w.Close()
		return Result{Err: err}
	}
	w.Close() // the child has its own copy; the reader ends when every copy is closed
	lines := make(chan struct{})
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			onStatus(sc.Text())
		}
	}()
	err = cmd.Wait()
	select {
	case <-lines:
	case <-time.After(pipeGrace):
		r.Close() // an inheriting daemon keeps the pipe open; stop reading
		<-lines
	}
	res := result(err, &out, &errOut)
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.ExitCode() >= 0 {
		// The command did end; only an output pipe that a daemon inherited stayed open.
		res.Err, res.ExitCode = nil, cmd.ProcessState.ExitCode()
	}
	return res
}

func result(err error, out, errOut *bytes.Buffer) Result {
	res := Result{Stdout: out.Bytes(), Stderr: errOut.Bytes()}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr) && exitErr.ExitCode() >= 0:
		res.ExitCode = exitErr.ExitCode()
	default:
		res.Err = err
	}
	return res
}
