//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/psewar/vordr/internal/agent"
	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/source/winget"
	"github.com/psewar/vordr/internal/wire"
)

// The agent as a Windows service (design/architecture.md, 14): LocalSystem, because winget installs machine-wide
// packages only elevated and Windows Update wants it; automatic start, restarted by the SCM after a crash.

const (
	serviceName = "vordr-agent"
	// stopWait: how long a stop request waits for an install run. An installer is never cut short; Windows itself
	// only waits so long at shutdown.
	stopWait = 5 * time.Minute
)

// asService: the Service Control Manager started this process.
func asService() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

func runService(path string) int {
	s := &agentService{path: path}
	if err := svc.Run(serviceName, s); err != nil {
		return 1
	}
	return s.code
}

type agentService struct {
	path   string
	code   int
	client atomic.Pointer[agent.Client]
}

// pbtAPMSuspend: the power broadcast Windows sends before it sleeps (SERVICE_CONTROL_POWEREVENT, winuser.h).
const pbtAPMSuspend = 0x0004

func (s *agentService) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	restart := make(chan struct{}, 1)
	onClient := func(c *agent.Client) {
		s.client.Store(c)
		if c.Updater != nil {
			c.Updater.Exit = func(job string) { plannedRestart(job, restart) }
		}
	}
	go func() { done <- runAgentCtx(ctx, s.path, true, onClient) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown | svc.AcceptPowerEvent}
	// The hub hears it from the PC before it goes away, so that a sleeping PC is no "offline" problem. Sent at once:
	// Windows gives the suspend notice about two seconds.
	power := func(state string) {
		if c := s.client.Load(); c != nil {
			c.Power(state)
		}
	}
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.PowerEvent:
				if c.EventType == pbtAPMSuspend {
					power(wire.PowerSleeping)
				}
			case svc.Stop, svc.Shutdown:
				if c.Cmd == svc.Shutdown {
					power(wire.PowerShutdown)
				}
				s.stop(cancel, done, status)
				return false, 0
			}
		case <-restart:
			// A self-update's restart (ADR-0103): a clean stop, which the service manager counts as no failure; the
			// update guard starts the service again.
			s.stop(cancel, done, status)
			return false, 0
		case s.code = <-done:
			// The agent ended on its own (configuration, identity): an error, so that the SCM's recovery steps in.
			return true, uint32(max(s.code, 1))
		}
	}
}

// stop ends the agent and reports the stop while it waits; an install run still in progress gets up to stopWait.
func (s *agentService) stop(cancel context.CancelFunc, done <-chan int, status chan<- svc.Status) {
	cancel()
	deadline := time.After(stopWait)
	for check := uint32(1); ; check++ {
		status <- svc.Status{State: svc.StopPending, CheckPoint: check, WaitHint: 15000}
		select {
		case s.code = <-done:
			return
		case <-deadline:
			return
		case <-time.After(10 * time.Second):
		}
	}
}

// plannedRestart is the self-update's exit in the service (ADR-0103). With the update guard of job listening, the
// agent asks it to start the service again and stops cleanly. Without it a clean stop would leave the agent stopped:
// then it ends with an error as before, and the service's recovery restarts it, counted as a failure.
func plannedRestart(job string, restart chan<- struct{}) {
	if !agent.AskGuardToStart(job) {
		os.Exit(3)
	}
	select {
	case restart <- struct{}{}:
	default: // a restart is already under way
	}
}

// serviceLog: as a service the agent has no console; it writes to <state_dir>\agent.log, the previous one kept as
// agent.log.old once it passes 5 MiB.
func serviceLog(stateDir string) io.Writer {
	path := filepath.Join(stateDir, "agent.log")
	if fi, err := os.Stat(path); err == nil && fi.Size() > 5<<20 {
		_ = os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return os.Stderr
	}
	return f
}

// installService sets the agent up as a service in one step. Everything that can refuse is checked before anything
// changes (exit 2): elevated, agent.yaml valid with a hub, the hub reachable, an installed agent.yaml the same or
// replaced with a reason (--replace-config), the service manager and an existing service readable.
// Then, while an existing agent keeps running, PowerShell 7 and the WinGet PowerShell module are installed for all
// users if missing, %ProgramData%\vordr and state_dir become readable for SYSTEM and administrators only (identity key,
// policy) and a replaced agent.yaml is kept as agent.yaml.bak-<time>. Only then the service stops, the binary goes to
// %ProgramFiles%\Vordr (a service binary in a folder users can write would hand them LocalSystem), agent.yaml to
// %ProgramData%\vordr, and the service is created or pointed at the new binary and started.
// A step that fails undoes what changed after the stop, last first: the service is as before, with its previous binary,
// agent.yaml and configuration (exit 3). Exit 1 only if something could not be put back; the last line then states
// the service as the service manager reports it.
func installService(path, reason string) int {
	if !windows.GetCurrentProcessToken().IsElevated() {
		fmt.Fprintln(os.Stderr, "install-service: run it as administrator (it creates a LocalSystem service)")
		return 2
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	cfg, issues, found, err := policy.Load(abs)
	switch {
	case err != nil || !found || len(issues) > 0:
		fmt.Fprintf(os.Stderr, "install-service: %s is not usable - run check-config\n", abs)
		return 2
	case cfg.Hub.URL == "" || cfg.HubKey() == nil:
		fmt.Fprintf(os.Stderr, "install-service: %s needs hub.url and hub.public_key\n", abs)
		return 2
	}
	if code := hubCheck(abs); code != 0 {
		fmt.Fprintln(os.Stderr, "install-service: the hub is not reachable from here - nothing changed")
		return 2
	}
	// The service reads agent.yaml from its own folder: a file in a folder users can write could be swapped for one
	// with another hub.
	target := policy.DefaultConfigPath
	plan, err := planConfig(target, abs, reason, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "install-service: %v\n", err)
		return 2
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	dir := filepath.Join(os.Getenv("ProgramFiles"), "Vordr")
	exe := filepath.Join(dir, "vordr-agent.exe")
	m, err := mgr.Connect()
	if err != nil {
		fmt.Fprintf(os.Stderr, "install-service: service manager: %v - nothing changed\n", err)
		return 2
	}
	defer m.Disconnect()
	before, err := serviceState(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "install-service: service %s: %v - nothing changed\n", serviceName, err)
		return 2
	}
	var s *mgr.Service
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	var oldConf mgr.Config
	if before.installed {
		if s, err = m.OpenService(serviceName); err == nil {
			oldConf, err = s.Config()
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "install-service: service %s: %v - nothing changed\n", serviceName, err)
			return 2
		}
	}

	switch {
	case plan.action == configReplace:
		for _, l := range plan.diff {
			fmt.Println(l)
		}
		fmt.Printf("install-service: agent.yaml will be replaced, old one kept as %s - reason: %s\n", plan.backup, reason)
	case reason != "":
		fmt.Printf("note   %s is new or the same as %s: --replace-config has nothing to replace\n", target, abs)
	}
	// First what a running agent does not notice: it keeps working while PowerShell 7 installs.
	steps := []step{
		{what: "PowerShell 7 for all users", do: installPowerShell7},
		{what: "PowerShell module Microsoft.WinGet.Client for all users", do: installWinGetClient},
		{what: filepath.Dir(target) + " for SYSTEM and administrators only", do: func() error { return restrict(filepath.Dir(target)) }},
		{what: "state_dir " + cfg.StateDir + " for SYSTEM and administrators only", do: func() error { return restrict(cfg.StateDir) }},
	}
	if plan.action == configReplace {
		steps = append(steps, step{what: "old agent.yaml kept as " + plan.backup,
			do: func() error { return copyFile(target, plan.backup, filepath.Dir(target)) }})
	}
	// From the stop on, every change can be undone. A running agent holds its binary open: it stops first.
	if before.installed {
		steps = append(steps, step{what: "service " + serviceName + " stopped",
			do: func() error { return stopService(s) },
			undo: func() error {
				if !before.running {
					return nil
				}
				return startService(s)
			}})
	}
	if !strings.EqualFold(self, exe) {
		steps = append(steps, binaryStep(self, exe, dir))
	}
	rollback := filepath.Join(dir, "rollback.cmd")
	marker := filepath.Join(cfg.StateDir, "update.json")
	steps = append(steps, rollbackStep(rollback, exe, marker))
	if _, err := os.Stat(marker); err == nil {
		steps = append(steps, markerStep(marker))
	}
	recoveryCommand := `cmd.exe /c "` + rollback + `"`
	if plan.action != configSame {
		steps = append(steps, configStep(abs, target, plan, reason, cfg.StateDir))
	}
	args := []string{"run", "--config", target}
	if before.installed {
		steps = append(steps, step{what: "service " + serviceName + ": " + exe + " as LocalSystem, automatic start",
			do: func() error {
				conf := oldConf
				conf.BinaryPathName = windows.EscapeArg(exe) + " " + strings.Join(escape(args), " ")
				conf.StartType, conf.DelayedAutoStart, conf.ServiceStartName = mgr.StartAutomatic, true, "LocalSystem"
				if err := s.UpdateConfig(conf); err != nil {
					return err
				}
				return setRecovery(s, recoveryCommand)
			},
			undo: func() error { return s.UpdateConfig(oldConf) }})
	} else {
		steps = append(steps, step{what: "service " + serviceName + " created",
			do: func() error {
				created, err := m.CreateService(serviceName, exe, mgr.Config{DisplayName: "Vördr agent",
					Description: "Keeps this PC's winget packages up to date as the Vördr hub asks, within agent.yaml.",
					StartType:   mgr.StartAutomatic, DelayedAutoStart: true}, args...)
				if err != nil {
					return err
				}
				s = created
				return setRecovery(s, recoveryCommand)
			},
			undo: func() error {
				if s == nil {
					return nil
				}
				err := s.Delete()
				s.Close()
				s = nil
				return err
			}})
	}
	steps = append(steps, step{what: "service " + serviceName + " started",
		do: func() error {
			if err := startService(s); err != nil {
				return fmt.Errorf("%v - see %s", err, filepath.Join(cfg.StateDir, "agent.log"))
			}
			return nil
		},
		undo: func() error { return stopService(s) }})

	if res := apply(steps, os.Stdout, os.Stderr); res.failed != "" {
		// The state as the service manager reports it now, not guessed from the errors above.
		now, err := serviceState(m)
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "state  service %s: the service manager does not say (%v)\n", serviceName, err)
		case len(res.stuck) == 0 && now.installed == before.installed && now.running == before.running:
			fmt.Printf("state  service %s %s, as before install-service\n", serviceName, now)
			return 3
		default:
			fmt.Fprintf(os.Stderr, "state  service %s %s, before install-service %s\n", serviceName, now, before)
		}
		return 1
	}
	// A manual install is a fresh start, as on Linux: the previous binary is not kept.
	if _, err := os.Stat(exe + ".old"); err == nil {
		if err := os.Remove(exe + ".old"); err != nil {
			fmt.Fprintf(os.Stderr, "note   %s.old stays: %v\n", exe, err)
		}
	}
	fmt.Printf("ok     service %s runs: %s run --config %s\n", serviceName, exe, target)
	fmt.Println("At the hub, after comparing the fingerprint (vordr-agent identity):")
	fmt.Printf("  vordr-hub approve %s --fingerprint <fp>\n", cfg.Node)
	return 0
}

// serviceFact: the service as the service manager reports it.
type serviceFact struct {
	installed bool
	running   bool   // running or starting
	state     string // the service manager's state, for the report
}

func (f serviceFact) String() string {
	if !f.installed {
		return "not installed"
	}
	return f.state
}

// serviceState asks the service manager whether the service exists and in which state it is.
func serviceState(m *mgr.Mgr) (serviceFact, error) {
	names, err := m.ListServices()
	if err != nil {
		return serviceFact{}, err
	}
	if !slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, serviceName) }) {
		return serviceFact{}, nil
	}
	s, err := m.OpenService(serviceName)
	if err != nil {
		return serviceFact{}, err
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return serviceFact{}, err
	}
	return serviceFact{installed: true, running: st.State != svc.Stopped && st.State != svc.StopPending,
		state: stateName(st.State)}, nil
}

func stateName(st svc.State) string {
	switch st {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "starting"
	case svc.StopPending:
		return "stopping"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "continuing"
	case svc.PausePending:
		return "pausing"
	case svc.Paused:
		return "paused"
	}
	return fmt.Sprintf("in state %d", st)
}

// stopService stops the service and waits as long as an install run may take. The state decides what to do: a
// stopped service needs nothing, a stopping one only the wait.
func stopService(s *mgr.Service) error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	switch st.State {
	case svc.Stopped:
		return nil
	case svc.StopPending:
	default:
		if _, err := s.Control(svc.Stop); err != nil {
			return err
		}
	}
	if !waitState(s, svc.Stopped, stopWait) {
		return fmt.Errorf("not stopped after %s", stopWait)
	}
	return nil
}

// startService starts the service and waits until it runs; a running one needs nothing, a starting one only the wait.
func startService(s *mgr.Service) error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	switch st.State {
	case svc.Running:
		return nil
	case svc.StartPending:
	default:
		if err := s.Start(); err != nil {
			return err
		}
	}
	if !waitState(s, svc.Running, time.Minute) {
		return fmt.Errorf("not running after %s", time.Minute)
	}
	return nil
}

// setRecovery: the SCM restarts the agent after 10 s and 1 min, also when it ends with an error. The third time it runs
// command (rollback.cmd) after 5 min: that puts the previous binary back while a self-update is pending, and starts the
// agent again in any case. The count holds for a day, so a self-update stops cleanly instead of ending with an error
// (ADR-0103); only without its update guard does it still end that way.
func setRecovery(s *mgr.Service, command string) error {
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: time.Minute}, {Type: mgr.RunCommand, Delay: 5 * time.Minute}}, 86400); err != nil {
		return err
	}
	if err := s.SetRecoveryCommand(command); err != nil {
		return err
	}
	return s.SetRecoveryActionsOnNonCrashFailures(true)
}

// restartAgent, for the update guard: the service manager starts the binary now in place. A hung agent may not answer
// a stop, so its process is ended first, as a crash would end it. The service's recovery starts it again as well,
// after 10 s to 5 min; whoever comes first starts it.
func restartAgent(context.Context) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return err
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.ProcessId != 0 {
		h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, st.ProcessId)
		if err != nil {
			return fmt.Errorf("agent process %d: %w", st.ProcessId, err)
		}
		defer windows.CloseHandle(h)
		if err := windows.TerminateProcess(h, 1); err != nil {
			return fmt.Errorf("ending agent process %d: %w", st.ProcessId, err)
		}
		if ev, err := windows.WaitForSingleObject(h, uint32(time.Minute/time.Millisecond)); err != nil || ev != windows.WAIT_OBJECT_0 {
			return fmt.Errorf("agent process %d did not end within a minute", st.ProcessId)
		}
	}
	if !waitState(s, svc.Stopped, time.Minute) {
		return fmt.Errorf("service %s not stopped after a minute", serviceName)
	}
	// ERROR_SERVICE_ALREADY_RUNNING: the recovery started it between the query and this start - a race with the
	// service manager that no query can rule out.
	if err := s.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return err
	}
	return nil
}

// plannedRestarts, for the update guard: the agent's requests for its planned restart, and the start that answers
// them (ADR-0103). The guard listens before the agent swaps the binary (armGuard waits for that).
func plannedRestarts(ctx context.Context, job string) (<-chan struct{}, func(context.Context) error, error) {
	requests, err := agent.GuardRequests(ctx, job)
	if err != nil {
		return nil, nil, err
	}
	return requests, startAfterStop, nil
}

// startAfterStop waits until the agent has stopped cleanly for its restart, then has the service manager start the
// binary now in place. The agent may first wait for an install run (stopWait).
func startAfterStop(context.Context) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return err
	}
	defer s.Close()
	if !waitState(s, svc.Stopped, stopWait+time.Minute) {
		return fmt.Errorf("service %s not stopped after %s", serviceName, stopWait+time.Minute)
	}
	return startService(s)
}

// rollbackScript is rollback.cmd, the counterpart of rollback.sh on Linux (ADR-0084): only while a self-update is
// pending (its marker exists) and the previous binary is kept, it puts that one back. Then it starts the agent, the
// third restart it replaces; an agent back on the old binary reports the failed update.
func rollbackScript(exe, marker string) string {
	return strings.Join([]string{
		"@echo off",
		"rem Vordr: third recovery action of the service " + serviceName + ", written by install-service.",
		"rem While a self-update is pending, put the previous binary back; then start the agent again.",
		fmt.Sprintf(`if exist "%s" if exist "%s.old" move /y "%s.old" "%s" >nul`, marker, exe, exe, exe),
		"sc start " + serviceName + " >nul",
	}, "\r\n") + "\r\n"
}

// rollbackStep writes rollback.cmd next to the binary; the undo puts back what was there before.
func rollbackStep(path, exe, marker string) step {
	var before []byte
	existed := false
	return step{what: "rollback " + path + " for a self-update that does not start",
		do: func() error {
			_, err := os.Stat(path)
			switch {
			case err == nil:
				if before, err = os.ReadFile(path); err != nil {
					return err
				}
				existed = true
			case !errors.Is(err, fs.ErrNotExist):
				return err
			}
			return os.WriteFile(path, []byte(rollbackScript(exe, marker)), 0o644)
		},
		undo: func() error {
			if existed {
				return os.WriteFile(path, before, 0o644)
			}
			return os.Remove(path)
		}}
}

// markerStep: an install by hand starts afresh, as install-agent.sh does on Linux. A self-update marker left behind
// would make the new agent report a failed update; the undo puts it back.
func markerStep(marker string) step {
	var content []byte
	return step{what: "pending self-update marker " + marker + " removed",
		do: func() error {
			var err error
			if content, err = os.ReadFile(marker); err != nil {
				return err
			}
			return os.Remove(marker)
		},
		undo: func() error { return os.WriteFile(marker, content, 0o600) }}
}

// binaryStep puts the binary in place. The previous one is kept as vordr-agent.exe.old, so that the undo can put it
// back; it is removed once the install has succeeded.
func binaryStep(self, exe, dir string) step {
	old := exe + ".old"
	kept := false
	return step{what: "binary " + exe,
		do: func() error {
			_, err := os.Stat(exe)
			switch {
			case err == nil:
				if err := copyFile(exe, old, dir); err != nil {
					return err
				}
				kept = true
			case !errors.Is(err, fs.ErrNotExist):
				return err
			}
			return copyFile(self, exe, dir)
		},
		undo: func() error {
			if !kept {
				return nil
			}
			return copyFile(old, exe, dir)
		}}
}

// configStep puts agent.yaml in place. A replacement and its reason go to agent.log first, as install-agent.sh puts
// them into the system log on Linux; the undo puts the previous file back, or removes a new one.
func configStep(src, target string, plan configPlan, reason, stateDir string) step {
	dir := filepath.Dir(target)
	return step{what: "agent.yaml " + target,
		do: func() error {
			if plan.action == configReplace {
				if err := logLine(stateDir, fmt.Sprintf("install-service: agent.yaml replaced (old one in %s): %s",
					plan.backup, reason)); err != nil {
					return fmt.Errorf("reason not logged, so not replaced: %v", err)
				}
			}
			return copyFile(src, target, dir)
		},
		undo: func() error {
			if plan.action == configNew {
				if _, err := os.Stat(target); errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return os.Remove(target)
			}
			if err := copyFile(plan.backup, target, dir); err != nil {
				return err
			}
			// The file is back; a missing log line does not change that.
			if err := logLine(stateDir, "install-service: agent.yaml put back from "+plan.backup+", the install failed"); err != nil {
				fmt.Fprintf(os.Stderr, "note   agent.log: %v\n", err)
			}
			return nil
		}}
}

// uninstallService stops and removes the service; binary, state_dir and agent.yaml stay (identity, run journal).
func uninstallService() int {
	m, err := mgr.Connect()
	if err != nil {
		fmt.Fprintf(os.Stderr, "uninstall-service: service manager: %v (administrator?)\n", err)
		return 2
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		fmt.Printf("service %s is not installed\n", serviceName)
		return 0
	}
	defer s.Close()
	_, _ = s.Control(svc.Stop)
	waitState(s, svc.Stopped, stopWait)
	if err := s.Delete(); err != nil {
		fmt.Fprintf(os.Stderr, "uninstall-service: %v\n", err)
		return 1
	}
	fmt.Printf("service %s removed; binary, state_dir and agent.yaml are kept\n", serviceName)
	return 0
}

func waitState(s *mgr.Service, want svc.State, d time.Duration) bool {
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		if st, err := s.Query(); err == nil && st.State == want {
			return true
		}
	}
	return false
}

func copyFile(src, dst, dir string) error {
	if same, _ := filepath.Abs(src); strings.EqualFold(same, dst) {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// restrict: SYSTEM (S-1-5-18) and Administrators (S-1-5-32-544) only, inheritance cut; by SID, which every Windows
// language spells the same.
func restrict(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return icacls(dir, "(OI)(CI)F")
}

func icacls(path, rights string) error {
	out, err := exec.Command("icacls", path, "/inheritance:r", "/grant:r", "*S-1-5-18:"+rights,
		"*S-1-5-32-544:"+rights).CombinedOutput()
	if err != nil {
		return fmt.Errorf("icacls: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// installPowerShell7: as LocalSystem, Microsoft.WinGet.Client works only in PowerShell 7, and the Store edition lives in
// a user's profile. Missing: winget installs it for all users, as the MSI (wix) - the default msix package can fail
// machine-wide with 0x80070005 and take the user's Store edition with it.
func installPowerShell7() error {
	p := winget.PowerShell7()
	if p == "" {
		return fmt.Errorf("%%ProgramFiles%% is not set")
	}
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	wg, err := exec.LookPath("winget")
	if err != nil {
		return fmt.Errorf("winget is not installed for this user - install PowerShell 7 for all users (MSI) by hand")
	}
	out, runErr := exec.Command(wg, "install", "--id", "Microsoft.PowerShell", "--exact", "--scope", "machine",
		"--installer-type", "wix", "--silent", "--accept-package-agreements", "--accept-source-agreements",
		"--disable-interactivity").CombinedOutput()
	if _, err := os.Stat(p); err != nil {
		tail := strings.TrimSpace(string(out))
		if len(tail) > 400 {
			tail = tail[len(tail)-400:]
		}
		return fmt.Errorf("winget did not install it (%v): %s", runErr, tail)
	}
	return nil
}

// installWinGetClient installs Microsoft's WinGet PowerShell module for all users, which LocalSystem then finds - in
// Windows PowerShell's folder, which PowerShell 7 reads as well.
// Already there: nothing to do.
func installWinGetClient() error {
	if _, err := os.Stat(filepath.Join(os.Getenv("ProgramFiles"), "WindowsPowerShell", "Modules", "Microsoft.WinGet.Client")); err == nil {
		return nil
	}
	ps := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	script := "$ProgressPreference='SilentlyContinue'; $ErrorActionPreference='Stop'; " +
		"Install-PackageProvider -Name NuGet -MinimumVersion 2.8.5.201 -Force -Scope AllUsers | Out-Null; " +
		"Install-Module -Name Microsoft.WinGet.Client -Scope AllUsers -Force -Repository PSGallery"
	out, err := exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func escape(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = windows.EscapeArg(a)
	}
	return out
}
