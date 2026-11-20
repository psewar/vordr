package agent

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/target"
)

// stubInstaller plays an appliance whose updater the agent drives (ADR-0205). Queue records the offer's words;
// InstallStatus answers from states in turn (the last repeats), each with the result in statusRes if there is one;
// Check answers status until the device reported an end, then the answers in after in turn (the last repeats).
type stubInstaller struct {
	stubDriver
	queued    []string
	ids       []string
	queueRes  target.Result
	states    []target.InstallState
	statusRes []target.Result
	calls     int
	after     []checked
	checks    int
	ended     bool
}

type checked struct {
	st  target.Status
	res target.Result
}

func (s *stubInstaller) Queue(_ context.Context, o target.Offer, id string) target.Result {
	if s.queueRes.OK() {
		s.queued = append(s.queued, o.Install)
		s.ids = append(s.ids, id)
	}
	return s.queueRes
}

func (s *stubInstaller) InstallStatus(context.Context, string) (target.InstallState, target.Result) {
	i := min(s.calls, len(s.states)-1)
	s.calls++
	if i < len(s.statusRes) && !s.statusRes[i].OK() {
		return target.InstallState{}, s.statusRes[i]
	}
	st := s.states[i]
	if st.State == target.InstallOK || st.State == target.InstallRebooted {
		s.ended = true
	}
	return st, target.Result{}
}

func (s *stubInstaller) Check(ctx context.Context) (target.Status, target.Result) {
	if !s.ended || len(s.after) == 0 {
		return s.stubDriver.Check(ctx)
	}
	a := s.after[min(s.checks, len(s.after)-1)]
	s.checks++
	return a.st, a.res
}

var (
	releaseOffer = target.Offer{Name: "pfSense", From: "1.2.3", Version: "1.2.4", Install: "release"}
	acmeOffer    = target.Offer{Name: "package pfSense-pkg-acme", From: "1.2", Version: "1.3.2",
		Install: "addon pfSense-pkg-acme"}
	nmapOffer = target.Offer{Name: "package pfSense-pkg-nmap", From: "1.4.4_11", Version: "1.4.4_12",
		Install: "addon pfSense-pkg-nmap"}
	otherOffer = target.Offer{Name: "pfSense (other branch)", From: "1.2.3", Version: "1.3.0", KeptBack: true}
)

// readAt: a status whose update facts were read at the given second, with the given offers.
func readAt(at int64, offers ...target.Offer) checked {
	return checked{st: target.Status{Product: "pfSense", Installed: "1.2.3-RELEASE", Offers: offers,
		Facts: map[string]string{"updates_checked_at": strconv.FormatInt(at, 10)}}}
}

func newStubInstaller(offers ...target.Offer) *stubInstaller {
	return &stubInstaller{stubDriver: stubDriver{exportLen: 2048,
		status: target.Status{Product: "pfSense", Installed: "1.2.3-RELEASE", Offers: offers}}}
}

func (s *stubInstaller) installer(t *testing.T) targetInstaller {
	return targetInstaller{d: s, store: target.Store{Dir: t.TempDir(), Keep: 5}, now: time.Now,
		poll: time.Millisecond, lost: time.Minute}
}

type phases struct{ seen []protocol.Phase }

func (p *phases) add(pr source.Progress) {
	if len(p.seen) == 0 || p.seen[len(p.seen)-1] != pr.Phase {
		p.seen = append(p.seen, pr.Phase)
	}
}

var scopeAll = source.InstallRequest{Scope: protocol.Scope{Kind: protocol.ScopeAll}}

func TestTargetInstallerOnlyForDriversThatInstall(t *testing.T) {
	store := target.Store{Dir: t.TempDir(), Keep: 5}
	if in := TargetInstaller(&stubDriver{}, store, time.Now); in != nil {
		t.Errorf("a driver that only reports got an installer: %#v", in)
	}
	if in := TargetInstaller(newStubInstaller(), store, time.Now); in == nil {
		t.Error("a driver that installs got none")
	}
}

// The release of the configured branch goes alone, after a fresh configuration backup; the run waits for the device's
// end, then for facts read after it, and the release is no longer offered. Kept-back offers are never queued.
func TestTargetInstallerInstallsTheRelease(t *testing.T) {
	s := newStubInstaller(releaseOffer, acmeOffer, otherOffer)
	s.states = []target.InstallState{{State: target.InstallQueued}, {State: target.InstallRunning},
		{State: target.InstallOK, RC: "0", Finished: 1000}}
	s.after = []checked{readAt(999, releaseOffer), readAt(1001, acmeOffer, otherOffer)}
	journal := 0
	req := scopeAll
	req.BeforeChange = func() error { journal++; return nil }
	var p phases
	res := s.installer(t).Install(context.Background(), req, time.Minute, p.add)
	if res.Code != protocol.CodeOK || res.Planned != 1 || !slices.Equal(res.Packages, []string{"pfSense"}) {
		t.Fatalf("result: %+v", res)
	}
	if !slices.Equal(s.queued, []string{"release"}) || len(s.ids[0]) != 24 || s.exports != 1 || journal != 1 {
		t.Errorf("queued %q (ids %q), %d exports, journal %d", s.queued, s.ids, s.exports, journal)
	}
	want := []protocol.Phase{protocol.PhasePrechecking, protocol.PhaseInstalling, protocol.PhaseVerifying}
	if !slices.Equal(p.seen, want) {
		t.Errorf("phases %v, want %v", p.seen, want)
	}
}

// Without a release, each add-on in turn.
func TestTargetInstallerInstallsEachAddOn(t *testing.T) {
	s := newStubInstaller(acmeOffer, otherOffer, nmapOffer)
	s.states = []target.InstallState{{State: target.InstallOK, Finished: 1000}}
	s.after = []checked{readAt(1001, otherOffer)}
	res := s.installer(t).Install(context.Background(), scopeAll, time.Minute, func(source.Progress) {})
	if res.Code != protocol.CodeOK || res.Planned != 2 ||
		!slices.Equal(s.queued, []string{"addon pfSense-pkg-acme", "addon pfSense-pkg-nmap"}) {
		t.Fatalf("result %+v, queued %q", res, s.queued)
	}
}

// Nothing it may install - only a release in another branch - is ok with nothing planned, and nothing is queued.
func TestTargetInstallerWithNothingToInstall(t *testing.T) {
	s := newStubInstaller(otherOffer)
	res := s.installer(t).Install(context.Background(), scopeAll, time.Minute, func(source.Progress) {})
	if res.Code != protocol.CodeOK || res.Planned != 0 || len(s.queued) != 0 {
		t.Fatalf("result %+v, queued %q", res, s.queued)
	}
}

// Refused before anything is queued: another scope than all, no configuration backup, a device that does not answer.
func TestTargetInstallerRefusesBeforeQueueing(t *testing.T) {
	s := newStubInstaller(releaseOffer)
	req := source.InstallRequest{Scope: protocol.Scope{Kind: protocol.ScopeSecurity}}
	if res := s.installer(t).Install(context.Background(), req, time.Minute, func(source.Progress) {}); res.Code !=
		protocol.CodeRejectedScopeNotAllowed {
		t.Errorf("scope security: %+v", res)
	}
	s.backup = target.Result{Code: protocol.CodeFailedConfigBackup, Detail: "not XML"}
	if res := s.installer(t).Install(context.Background(), scopeAll, time.Minute, func(source.Progress) {}); res.Code !=
		protocol.CodeFailedConfigBackup {
		t.Errorf("no backup: %+v", res)
	}
	s.backup = target.Result{}
	s.check = target.Result{Code: protocol.CodePrecheckRepoUnreachable}
	if res := s.installer(t).Install(context.Background(), scopeAll, time.Minute, func(source.Progress) {}); res.Code !=
		protocol.CodePrecheckRepoUnreachable {
		t.Errorf("device not answering: %+v", res)
	}
	journalFails := scopeAll
	journalFails.BeforeChange = func() error { return errors.New("disk full") }
	s.check = target.Result{}
	if res := s.installer(t).Install(context.Background(), journalFails, time.Minute, func(source.Progress) {}); res.Code !=
		protocol.CodeFailedPackageManager {
		t.Errorf("no journal: %+v", res)
	}
	if len(s.queued) != 0 {
		t.Errorf("queued after a refusal: %q", s.queued)
	}
}

// The device's end decides: busy is rejected_busy, a refusal or a failure failed_package_manager, a refused queue its
// code; a request the device never takes is lost; no end within the run's time is a timeout.
func TestTargetInstallerTakesTheDevicesEnd(t *testing.T) {
	for name, tc := range map[string]struct {
		states   []target.InstallState
		queueRes target.Result
		want     protocol.Code
	}{
		"busy":        {[]target.InstallState{{State: target.InstallRefused, Code: target.InstallBusy}}, target.Result{}, protocol.CodeRejectedBusy},
		"not offered": {[]target.InstallState{{State: target.InstallRefused, Code: "not_offered"}}, target.Result{}, protocol.CodeFailedPackageManager},
		"failed":      {[]target.InstallState{{State: target.InstallFailed, Code: "failed", RC: "1"}}, target.Result{}, protocol.CodeFailedPackageManager},
		"interrupted": {[]target.InstallState{{State: target.InstallFailed, Code: "interrupted"}}, target.Result{}, protocol.CodeFailedPackageManager},
		"queue refused": {[]target.InstallState{{State: target.InstallOK}},
			target.Result{Code: protocol.CodeRejectedUnsupported}, protocol.CodeRejectedUnsupported},
	} {
		t.Run(name, func(t *testing.T) {
			s := newStubInstaller(releaseOffer)
			s.states, s.queueRes = tc.states, tc.queueRes
			if res := s.installer(t).Install(context.Background(), scopeAll, time.Minute,
				func(source.Progress) {}); res.Code != tc.want {
				t.Fatalf("%+v, want %s", res, tc.want)
			}
		})
	}

	s := newStubInstaller(releaseOffer)
	s.states = []target.InstallState{{State: target.InstallUnknown}}
	in := s.installer(t)
	clock := time.Unix(1700000000, 0)
	in.now = func() time.Time { clock = clock.Add(time.Minute); return clock }
	if res := in.Install(context.Background(), scopeAll, time.Minute, func(source.Progress) {}); res.Code !=
		protocol.CodeFailedPackageManager {
		t.Errorf("request never taken: %+v", res)
	}

	s = newStubInstaller(releaseOffer)
	s.states = []target.InstallState{{State: target.InstallRunning}}
	if res := s.installer(t).Install(context.Background(), scopeAll, 50*time.Millisecond,
		func(source.Progress) {}); res.Code != protocol.CodeTimeout {
		t.Errorf("no end: %+v", res)
	}
}

// A release install reboots the device: while it does not answer the run is rebooting, after the reboot it waits for
// facts read after the end and for the release to be gone, also through stale and unanswered checks.
func TestTargetInstallerThroughTheReboot(t *testing.T) {
	s := newStubInstaller(releaseOffer)
	unreachable := target.Result{Code: protocol.CodePrecheckRepoUnreachable}
	s.statusRes = []target.Result{{}, unreachable, unreachable, {}}
	s.states = []target.InstallState{{State: target.InstallRunning}, {}, {}, {State: target.InstallRebooted,
		Finished: 1000}}
	s.after = []checked{{res: unreachable}, readAt(999, releaseOffer), readAt(1000, releaseOffer),
		readAt(1060, target.Offer{Name: "pfSense", From: "1.2.4", Version: "1.2.5", Install: "release"})}
	var p phases
	res := s.installer(t).Install(context.Background(), scopeAll, time.Minute, p.add)
	if res.Code != protocol.CodeOK {
		t.Fatalf("result: %+v", res)
	}
	if !slices.Contains(p.seen, protocol.PhaseRebooting) {
		t.Errorf("phases %v without rebooting", p.seen)
	}
}

// After an install that ended ok, fresh facts that still offer it are failed_verification at once; after a reboot
// only when the run's time ends.
func TestTargetInstallerVerifies(t *testing.T) {
	s := newStubInstaller(releaseOffer)
	s.states = []target.InstallState{{State: target.InstallOK, Finished: 1000}}
	s.after = []checked{readAt(1001, releaseOffer)}
	start := time.Now()
	if res := s.installer(t).Install(context.Background(), scopeAll, time.Minute,
		func(source.Progress) {}); res.Code != protocol.CodeFailedVerification || time.Since(start) > 5*time.Second {
		t.Errorf("still offered after ok: %+v after %v, want at once", res, time.Since(start))
	}
	s = newStubInstaller(releaseOffer)
	s.states = []target.InstallState{{State: target.InstallRebooted, Finished: 1000}}
	s.after = []checked{readAt(1001, releaseOffer)}
	start = time.Now()
	if res := s.installer(t).Install(context.Background(), scopeAll, 80*time.Millisecond,
		func(source.Progress) {}); res.Code != protocol.CodeFailedVerification || time.Since(start) < 70*time.Millisecond {
		t.Errorf("still offered after the reboot: %+v after %v", res, time.Since(start))
	}
	s = newStubInstaller(releaseOffer)
	s.states = []target.InstallState{{State: target.InstallOK, Finished: 1000}}
	s.after = []checked{{res: target.Result{Code: protocol.CodeFailedTargetHostKey}}}
	if res := s.installer(t).Install(context.Background(), scopeAll, time.Minute,
		func(source.Progress) {}); res.Code != protocol.CodeFailedTargetHostKey {
		t.Errorf("another host key after the install: %+v", res)
	}
}
