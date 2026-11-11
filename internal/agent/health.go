package agent

import (
	"context"
	"crypto/tls"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/runner"
)

// Health runs the checks of agent.yaml health: after an install run that changed packages (design 10.2). An upgrade
// restarts services, so the checks get time: they are repeated until all pass or the wait is up. A run that went
// through but leaves a check failing ends failed_health - "ok" would hide that the host no longer does its job.
type Health struct {
	Checks []policy.Check
	Run    runner.Runner
	Get    func(ctx context.Context, url string) (status int, err error) // status of a GET
	Wait   time.Duration                                                 // how long a check may take to come back
	Every  time.Duration                                                 // pause between rounds
}

const (
	healthWait  = 3 * time.Minute
	healthEvery = 15 * time.Second
	httpTimeout = 10 * time.Second
)

// NewHealth returns the checks of agent.yaml, or nil when it names none.
func NewHealth(checks []policy.Check) *Health {
	if len(checks) == 0 {
		return nil
	}
	// The services checked run on this host, mostly with certificates for their public name or self-signed ones;
	// the check only reads the status code.
	client := &http.Client{Timeout: httpTimeout, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // local health probe, status only
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Health{Checks: checks, Run: runner.Exec{}, Wait: healthWait, Every: healthEvery,
		Get: func(ctx context.Context, u string) (int, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			if err != nil {
				return 0, err
			}
			resp, err := client.Do(req)
			if err != nil {
				return 0, err
			}
			resp.Body.Close()
			return resp.StatusCode, nil
		}}
}

// Settle repeats the checks until all pass or Wait is up; "" when they pass, else what still fails.
func (h *Health) Settle(ctx context.Context) string {
	deadline := time.Now().Add(h.Wait)
	for {
		failed := h.round(ctx)
		if len(failed) == 0 {
			return ""
		}
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			return strings.Join(failed, "; ")
		}
		select {
		case <-ctx.Done():
		case <-time.After(h.Every):
		}
	}
}

func (h *Health) round(ctx context.Context) []string {
	var failed []string
	for _, c := range h.Checks {
		if why := h.one(ctx, c); why != "" {
			failed = append(failed, c.String()+": "+why)
		}
	}
	return failed
}

// one runs a single check; "" when it passes, else what was found.
func (h *Health) one(ctx context.Context, c policy.Check) string {
	ctx, cancel := context.WithTimeout(ctx, httpTimeout+5*time.Second)
	defer cancel()
	switch {
	case c.Systemd != "":
		res := h.Run.Run(ctx, nil, "systemctl", "is-active", c.Systemd)
		state := strings.TrimSpace(string(res.Stdout))
		if res.Ran() && state == "active" {
			return ""
		}
		if state == "" {
			state = "unknown"
		}
		return state
	case c.Docker != "":
		res := h.Run.Run(ctx, nil, "docker", "inspect", "--format",
			"{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}}", c.Docker)
		if !res.Ran() || res.ExitCode != 0 {
			return "not found"
		}
		status, health, _ := strings.Cut(strings.TrimSpace(string(res.Stdout)), " ")
		switch {
		case status != "running":
			return status
		case health != "" && health != "healthy":
			return health
		}
		return ""
	default:
		status, err := h.Get(ctx, c.HTTP)
		switch {
		case err != nil:
			return "no answer"
		case status < 200 || status > 399:
			return "HTTP " + strconv.Itoa(status)
		}
		return ""
	}
}
