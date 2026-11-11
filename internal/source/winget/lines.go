package winget

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"

	"github.com/psewar/vordr/internal/runner"
)

// execLines runs a real command and reads its stdout line by line while it runs. runner.Exec's Stream reads a third
// file descriptor, which Windows does not hand to child processes.
type execLines struct{}

func (execLines) Lines(ctx context.Context, onLine func(line string), name string, args ...string) runner.Result {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errOut bytes.Buffer
	cmd.Stderr = &errOut
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return runner.Result{Err: err}
	}
	if err := cmd.Start(); err != nil {
		return runner.Result{Err: err}
	}
	sc := bufio.NewScanner(io.TeeReader(pipe, &out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		onLine(sc.Text())
	}
	err = cmd.Wait()
	res := runner.Result{Stdout: out.Bytes(), Stderr: errOut.Bytes()}
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
