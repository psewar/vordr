package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/hub"
	"github.com/psewar/vordr/internal/wire"
)

// printShow writes one host for people: what the register knows and, for an approved host, its view as Home
// Assistant gets it. The text is no interface; programs read show --json (ADR-0171).
func printShow(w io.Writer, n hub.NodeView, loc *time.Location) {
	at := func(t time.Time) string { return t.In(loc).Format("2006-01-02 15:04") }
	line := func(label, text string) { fmt.Fprintf(w, "  %-15s %s\n", label, text) }
	v := n.View

	state := "offline"
	if n.Online {
		state = "online"
		if n.LastSeen != nil {
			state += ", last seen " + at(*n.LastSeen)
		}
	}
	if v != nil && v.Power != "" {
		state += ", " + v.Power
	}
	fmt.Fprintf(w, "%s  %s  %s\n", n.Node, n.State, state)
	line("fingerprint", n.Fingerprint)

	agent := n.AgentVersion
	if n.AgentSince != nil {
		agent += " since " + at(*n.AgentSince)
	}
	if v != nil && v.AgentRelease != "" {
		switch {
		case v.AgentReleaseNoBinary:
			agent += ", release " + v.AgentRelease + " has no binary for this host"
		case v.AgentRelease != n.AgentVersion:
			agent += ", release " + v.AgentRelease + " handed out"
		}
	}
	line("agent", agent)
	system := []string{n.Facts.OS}
	if n.Facts.GOOS != "" {
		system = append(system, n.Facts.GOOS+"/"+n.Facts.Arch)
	}
	if virt := n.Facts.Virtualization; virt != "" && virt != "none" {
		system = append(system, virt)
	}
	if n.Facts.Hostname != "" {
		system = append(system, "host name "+n.Facts.Hostname)
	}
	line("system", strings.Join(system, ", "))
	registered := "first seen " + at(n.FirstSeen)
	if n.DecidedAt != nil {
		registered += ", " + string(n.State) + " " + at(*n.DecidedAt)
	}
	line("registered", registered)
	verbs := make([]string, 0, len(n.Verbs))
	for _, vb := range n.Verbs {
		verbs = append(verbs, string(vb))
	}
	line("verbs", orNone(strings.Join(verbs, ", ")))
	line("policy", policyText(n.Policy))

	line("updates", updatesText(n, at))
	switch {
	case v != nil && v.Reboot.Required == nil:
		line("reboot", "unknown (no signal answered)")
	case v != nil:
		line("reboot", rebootText(v.Reboot, at))
	case n.Reboot != nil && *n.Reboot:
		line("reboot", "required")
	case n.Reboot != nil:
		line("reboot", "not needed")
	}
	if v == nil {
		return // a waiting or rejected host has no view
	}
	problem := "none"
	if p := v.Problem; p != nil {
		problem = strings.Join(p.Reasons, ", ")
		if p.CheckFailedCode != "" {
			problem += " (check failed: " + string(p.CheckFailedCode) + ")"
		}
	}
	line("problem", problem)
	if v.LastRun != nil {
		line("last run", runText(*v.LastRun, at))
		printDetail(w, *v.LastRun)
	}
	if v.FailedInstall != nil {
		line("failed install", runText(*v.FailedInstall, at))
	}
	if len(v.Queued) > 0 {
		line("queued", strings.Join(v.Queued, ", "))
	}
	if v.NextInstall != nil {
		line("next install", "not before "+at(*v.NextInstall)+" (minimum interval)")
	}
	if b := v.Backup; b != nil {
		text := b.State
		if b.Last != nil {
			text += ", last " + at(*b.Last)
		}
		if b.BeforeInstall != "" {
			text += ", a fresh backup by " + b.BeforeInstall + " before each install"
		} else if !b.ConfigBackup {
			text += fmt.Sprintf(", limit %d h", b.MaxAgeHours)
		}
		line("backup", text)
	}
	if p := v.Packages; p != nil {
		text := fmt.Sprintf("%d in %d location(s), checked %s", p.Total, len(p.Locations), at(p.CheckedAt))
		text += ", " + countText(p.Vulnerable, "vulnerable") + ", " + countText(p.Malicious, "malicious") + ", " +
			countText(p.Outdated, "outdated")
		line("packages", text)
	}
	if c := v.Containers; c != nil {
		text := fmt.Sprintf("%d image(s)", c.Images)
		if c.Code != "" {
			text = "not read (" + c.Code + ")"
		} else {
			text += ", " + countText(c.Outdated, "outdated")
		}
		line("containers", text)
	}
}

// updatesText: the counts of the last report; unknown while there is none or the check failed.
func updatesText(n hub.NodeView, at func(time.Time) string) string {
	if n.ReportAt == nil {
		return "no report yet"
	}
	v := n.View
	if v != nil && v.Pending == nil {
		return "unknown (the check failed), reported " + at(*n.ReportAt)
	}
	if n.Pending == nil {
		return "reported " + at(*n.ReportAt)
	}
	text := fmt.Sprintf("%d pending (%d security", *n.Pending, *n.Security)
	if v != nil && v.Installable != nil {
		text += fmt.Sprintf(", %d installable", *v.Installable)
	}
	if v != nil && v.KeptBack != nil {
		text += fmt.Sprintf(", %d kept back", *v.KeptBack)
	}
	text += "), reported " + at(*n.ReportAt)
	if n.PendingSince != nil && *n.Pending > 0 {
		text += ", pending since " + at(*n.PendingSince)
	}
	return text
}

func rebootText(r wire.RebootView, at func(time.Time) string) string {
	if !*r.Required {
		return "not needed"
	}
	text := "required"
	if r.Since != nil {
		text += " since " + at(*r.Since)
	}
	if r.Reason != "" {
		text += " (" + string(r.Reason)
		if len(r.Packages) > 0 {
			text += ": " + strings.Join(r.Packages, ", ")
		}
		text += ")"
	}
	if r.KernelRunning != "" && r.KernelNewest != "" && r.KernelRunning != r.KernelNewest {
		text += ", kernel " + r.KernelRunning + " runs, " + r.KernelNewest + " installed"
	}
	return text
}

func runText(r wire.RunView, at func(time.Time) string) string {
	text := string(r.Verb)
	if r.Scope != "" {
		text += " " + r.Scope
	}
	if r.By != "" {
		text += " by " + r.By
	}
	text += ": " + r.State
	if r.Reason != "" {
		text += " (" + string(r.Reason) + ")"
	}
	if r.Progress != nil {
		text += fmt.Sprintf(", %d %%", *r.Progress)
	}
	if r.Item != "" {
		text += ", " + r.Item
	}
	text += ", started " + at(r.Started)
	if r.Finished != nil {
		text += ", finished " + at(*r.Finished)
	}
	if r.OutsideWindow != "" {
		text += ", outside the window: " + r.OutsideWindow
	}
	if s := r.Summary; s != nil {
		text += fmt.Sprintf(", %d package(s) in %d s", s.PackageCount, s.Seconds)
	}
	return text
}

// printDetail: the run's log excerpt, indented under it.
func printDetail(w io.Writer, r wire.RunView) {
	for _, l := range strings.Split(strings.TrimRight(r.LogExcerpt, "\n"), "\n") {
		if l != "" {
			fmt.Fprintf(w, "%18s%s\n", "", l)
		}
	}
}

func policyText(p wire.PolicySummary) string {
	var parts []string
	if len(p.AllowedScopes) == 0 {
		parts = append(parts, "no installs")
	} else {
		scopes := "install " + strings.Join(p.AllowedScopes, ", ")
		if p.DefaultScope != "" {
			scopes += " (default " + p.DefaultScope + ")"
		}
		parts = append(parts, scopes)
	}
	if p.Auto != "" && p.Auto != "off" {
		auto := "auto " + p.Auto
		if p.AutoScope != "" {
			auto += " " + p.AutoScope
		}
		parts = append(parts, auto)
	}
	if wi := p.Window; wi != nil {
		window := "window " + strings.Join(wi.Days, ",") + " " + wi.Start + "-" + wi.End
		if p.TZ != "" {
			window += " " + p.TZ
		}
		parts = append(parts, window)
	}
	if p.RebootAllowed {
		parts = append(parts, "reboot allowed")
	}
	if p.AgentUpdate {
		parts = append(parts, "agent update allowed")
	}
	if p.OutsideWindow {
		parts = append(parts, "outside the window with a reason")
	}
	if p.MinIntervalH > 0 {
		parts = append(parts, fmt.Sprintf("at least %d h between installs", p.MinIntervalH))
	}
	return strings.Join(parts, ", ")
}

// countText: "3 vulnerable", or "vulnerable unknown" when the hub cannot tell.
func countText(n *int, what string) string {
	if n == nil {
		return what + " unknown"
	}
	return fmt.Sprintf("%d %s", *n, what)
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
