package agent

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/runner"
)

// Containers keeps the host's containers running through an install run. A Docker upgrade restarts the daemon, and a
// container with a restart policy is supposed to come back on its own, but one can stay down (e.g. a database that
// exited 0 under unless-stopped while the others came back), and the apps that use it have no database until
// someone starts it. So before a run the agent notes which containers run with a restart policy, and after it
// starts again those that are not running, once the daemon answers. The health checks of agent.yaml judge the result.
type Containers struct {
	Run   runner.Runner
	Wait  time.Duration // how long the daemon may take to answer again after the run
	Every time.Duration // pause between asking it
}

const (
	containersWait  = 2 * time.Minute
	containersEvery = 5 * time.Second
)

// NewContainers returns the guard for a host with Docker, nil without the docker command.
func NewContainers() *Containers {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil
	}
	return &Containers{Run: runner.Exec{}, Wait: containersWait, Every: containersEvery}
}

// Running names the running containers whose restart policy is always or unless-stopped. Containers without one
// (no, on-failure) are left alone: someone stopped them, or they end by design. Nil when Docker does not answer.
func (c *Containers) Running(ctx context.Context) []string {
	ids := c.Run.Run(ctx, nil, "docker", "ps", "-q", "--filter", "status=running")
	if !ids.Ran() || ids.ExitCode != 0 {
		return nil
	}
	list := strings.Fields(string(ids.Stdout))
	if len(list) == 0 {
		return nil
	}
	res := c.Run.Run(ctx, nil, "docker", append([]string{"inspect", "--format",
		"{{.Name}} {{.HostConfig.RestartPolicy.Name}}"}, list...)...)
	if !res.Ran() || res.ExitCode != 0 {
		return nil
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(res.Stdout)), "\n") {
		name, policy, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok && (policy == "always" || policy == "unless-stopped") {
			names = append(names, strings.TrimPrefix(name, "/"))
		}
	}
	return names
}

// Restore starts the containers of before that do not run now. started: started again by the agent; failed: what
// could not be started, "name: why". A container that is restarting is the daemon's business and left alone; one
// that no longer exists was removed on purpose (or by the run) and is named in failed.
func (c *Containers) Restore(ctx context.Context, before []string) (started, failed []string) {
	if len(before) == 0 {
		return nil, nil
	}
	if !c.answers(ctx) {
		return nil, []string{"docker: the daemon does not answer"}
	}
	for _, name := range before {
		st := c.Run.Run(ctx, nil, "docker", "inspect", "--format", "{{.State.Status}}", name)
		if !st.Ran() || st.ExitCode != 0 {
			failed = append(failed, name+": gone")
			continue
		}
		switch strings.TrimSpace(string(st.Stdout)) {
		case "running", "restarting":
			continue
		}
		if res := c.Run.Run(ctx, nil, "docker", "start", name); res.Ran() && res.ExitCode == 0 {
			started = append(started, name)
		} else {
			why := strings.TrimSpace(string(res.Stderr))
			if i := strings.IndexByte(why, '\n'); i >= 0 {
				why = why[:i]
			}
			if why == "" {
				why = "docker start failed"
			}
			failed = append(failed, name+": "+why)
		}
	}
	return started, failed
}

// answers waits until the daemon answers again (it restarts while Docker itself is upgraded), at most Wait.
func (c *Containers) answers(ctx context.Context) bool {
	deadline := time.Now().Add(c.Wait)
	for {
		if res := c.Run.Run(ctx, nil, "docker", "info", "--format", "{{.ServerVersion}}"); res.Ran() && res.ExitCode == 0 {
			return true
		}
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			return false
		}
		select {
		case <-ctx.Done():
		case <-time.After(c.Every):
		}
	}
}
