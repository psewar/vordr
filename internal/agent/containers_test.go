package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/source"
)

// dockerRunner answers docker commands by their whole argument list; each call takes the next answer, the last one
// stays. A missing key is a failing command. started records docker start calls.
type dockerRunner struct {
	answers map[string][]runner.Result
	started []string
}

func (r *dockerRunner) Run(_ context.Context, _ []string, name string, args ...string) runner.Result {
	if len(args) > 0 && args[0] == "start" {
		r.started = append(r.started, args[1])
	}
	key := name + " " + strings.Join(args, " ")
	out := r.answers[key]
	if len(out) == 0 {
		return runner.Result{ExitCode: 1, Stderr: []byte("Error: No such object\n")}
	}
	a := out[0]
	if len(out) > 1 {
		r.answers[key] = out[1:]
	}
	return a
}

func ok(s string) runner.Result { return runner.Result{Stdout: []byte(s + "\n")} }

const (
	psRunning = "docker ps -q --filter status=running"
	inspPol   = "docker inspect --format {{.Name}} {{.HostConfig.RestartPolicy.Name}} "
	dockerUp  = "docker info --format {{.ServerVersion}}"
	inspState = "docker inspect --format {{.State.Status}} "
)

func testContainers(r *dockerRunner) *Containers {
	return &Containers{Run: r, Wait: 50 * time.Millisecond, Every: time.Millisecond}
}

// Only containers with a restart policy count: one without was stopped or ends on purpose.
func TestContainersRunningWithRestartPolicy(t *testing.T) {
	r := &dockerRunner{answers: map[string][]runner.Result{
		psRunning:            {ok("a1\nb2\nc3")},
		inspPol + "a1 b2 c3": {ok("/app-db unless-stopped\n/oneshot no\n/chat always")},
	}}
	got := testContainers(r).Running(context.Background())
	if strings.Join(got, ",") != "app-db,chat" {
		t.Fatalf("running: %v", got)
	}
}

// After the run: what is down is started again (app-db), what runs or restarts is left alone,
// what is gone is named.
func TestContainersRestore(t *testing.T) {
	r := &dockerRunner{answers: map[string][]runner.Result{
		dockerUp:              {{ExitCode: 1}, ok("1.2.3")}, // the daemon is still restarting at first
		inspState + "app-db":  {ok("exited")},
		inspState + "chat":    {ok("running")},
		inspState + "cache":   {ok("restarting")},
		"docker start app-db": {ok("app-db")},
	}}
	started, failed := testContainers(r).Restore(context.Background(), []string{"app-db", "chat", "cache", "gone-one"})
	if strings.Join(started, ",") != "app-db" || strings.Join(r.started, ",") != "app-db" {
		t.Fatalf("started: %v (calls %v)", started, r.started)
	}
	if len(failed) != 1 || failed[0] != "gone-one: gone" {
		t.Fatalf("failed: %v", failed)
	}
}

// A daemon that does not answer again is reported, nothing is started.
func TestContainersRestoreWithoutDaemon(t *testing.T) {
	r := &dockerRunner{answers: map[string][]runner.Result{}}
	started, failed := testContainers(r).Restore(context.Background(), []string{"app-db"})
	if len(started) != 0 || len(failed) != 1 || !strings.Contains(failed[0], "does not answer") {
		t.Fatalf("started %v failed %v", started, failed)
	}
}

// In an install run: the container the upgrade left stopped is started before the health checks, the run ends ok and
// the summary names it.
func TestInstallStartsContainersAgain(t *testing.T) {
	inst := &fakeInstaller{change: true, result: source.InstallResult{Code: protocol.CodeOK, Planned: 1, Packages: []string{"docker-ce"}}}
	c := installClient(t, inst, allowAll)
	r := &dockerRunner{answers: map[string][]runner.Result{
		psRunning:             {ok("a1")},
		inspPol + "a1":        {ok("/app-db unless-stopped")},
		dockerUp:              {ok("1.2.3")},
		inspState + "app-db":  {ok("exited")},
		"docker start app-db": {ok("app-db")},
	}}
	c.Containers = testContainers(r)
	c.Health = testHealth([]policy.Check{{Docker: "app-db"}}, &scriptedRunner{answers: map[string][]string{
		"docker app-db": {"running "}}}, 200)
	var session outbox
	c.approvedSession(session.send)
	c.install(installJob("j1", "all"), session.send)
	fin := session.final()
	if fin.Code != protocol.CodeOK || fin.Summary == nil || strings.Join(fin.Summary.Restarted, ",") != "app-db" {
		t.Fatalf("result: %+v", fin)
	}
	// The journal kept them for the next start, should the run have been cut short (ADR-0125).
	if jr, _ := c.readJournal(); strings.Join(jr.Containers, ",") != "app-db" {
		t.Fatalf("journal containers: %v", jr.Containers)
	}
}

// A run cut short (ADR-0125): the agent's next start brings back the containers that ran before it, before the result
// goes out; the interrupted result names them, and those that could not be started. A run that ended leaves its
// containers alone at the next start: it brought them back itself.
func TestInterruptedRunStartsContainersAgain(t *testing.T) {
	c := installClient(t, &fakeInstaller{}, allowAll)
	if err := writeJournal(c.journalPath(), runJournal{JobID: "j7", Verb: protocol.VerbInstall, Scope: "all",
		Started: time.Now().UTC(), Containers: []string{"app-db", "chat", "gone-one"}}); err != nil {
		t.Fatal(err)
	}
	r := &dockerRunner{answers: map[string][]runner.Result{
		dockerUp:              {{ExitCode: 1}, ok("1.2.3")}, // the daemon comes up after the agent
		inspState + "app-db":  {ok("exited")},
		inspState + "chat":    {ok("running")},
		"docker start app-db": {ok("app-db")},
	}}
	c.Containers = testContainers(r)
	c.settleJournal()
	if strings.Join(r.started, ",") != "app-db" {
		t.Fatalf("started: %v", r.started)
	}
	var session outbox
	c.approvedSession(session.send)
	fin := session.final()
	if fin.Code != protocol.CodeInterrupted || fin.JobID != "j7" || fin.Summary == nil ||
		strings.Join(fin.Summary.Restarted, ",") != "app-db" || !strings.Contains(fin.Detail, "gone-one: gone") {
		t.Fatalf("interrupted result: %+v %+v", fin, fin.Summary)
	}

	// A run that ended: nothing is started at the next start.
	r2 := &dockerRunner{answers: map[string][]runner.Result{dockerUp: {ok("1.2.3")}, inspState + "app-db": {ok("exited")},
		"docker start app-db": {ok("app-db")}}}
	ended := time.Now().UTC()
	if err := writeJournal(c.journalPath(), runJournal{JobID: "j8", Verb: protocol.VerbInstall, Scope: "all",
		Started: ended, Code: protocol.CodeOK, Finished: &ended, Containers: []string{"app-db"}}); err != nil {
		t.Fatal(err)
	}
	c.Containers = testContainers(r2)
	c.settleJournal()
	if len(r2.started) != 0 {
		t.Fatalf("a run that ended: started %v", r2.started)
	}
}
