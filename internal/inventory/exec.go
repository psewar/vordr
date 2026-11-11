package inventory

import (
	"bytes"
	"context"
	"errors"
	"os/exec"

	"github.com/psewar/vordr/internal/runner"
)

// Command is one run of pip or npm for an update (11.4). Owner: the folder whose owner runs it (Linux); a package
// manager running as root in a user's folder would leave files the user cannot change any more.
type Command struct {
	Dir   string
	Env   []string
	Owner string
	Name  string
	Args  []string
}

// CommandRunner runs commands; tests stand in for pip and npm.
type CommandRunner interface {
	Run(ctx context.Context, c Command) runner.Result
}

// Exec runs real commands, as the owner of c.Owner where the platform allows it (asOwner).
type Exec struct{}

func (Exec) Run(ctx context.Context, c Command) runner.Result {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	env, err := asOwner(cmd, c.Owner)
	if err != nil {
		return runner.Result{Err: err}
	}
	cmd.Env = append(env, c.Env...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	res := runner.Result{Stdout: out.Bytes(), Stderr: errOut.Bytes()}
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		res.ExitCode = exit.ExitCode()
	default:
		res.Err = err
	}
	return res
}
