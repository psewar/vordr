//go:build !linux && !windows

package agent

import (
	"context"
	"errors"
	"os"

	"github.com/psewar/vordr/internal/runner"
)

func selfUpdatePlatform() string { return "self-update is built for Linux and Windows only" }

func armGuard(context.Context, runner.Runner, string, string, string) error {
	return errors.New("no update guard on this platform")
}

func restartExit(string) { os.Exit(0) }

func restartInstances(context.Context, runner.Runner) error { return nil }

func nextPath(exe string) string { return exe + ".new" }

func putBack(exe string) error { return os.Rename(exe+".old", exe) }

func removeLeftovers(string) {}

func readShared(path string) ([]byte, error) { return os.ReadFile(path) }
