//go:build windows

package agent

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A running .exe can be renamed but not overwritten (ADR-0084). This test does it to its own running binary: the swap
// moves it aside as .old and puts the new file in place, putBack moves the new file aside and the running binary back.
func TestSwapAndPutBackWithTheRunningBinary(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	running, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { // whatever happened: the running binary back under its name
		if _, err := os.Stat(exe + ".old"); err == nil {
			_ = os.Remove(exe)
			_ = os.Rename(exe+".old", exe)
		}
		_ = os.Remove(exe + ".failed")
		_ = os.Remove(nextPath(exe))
	})
	next := nextPath(exe)
	if next != exe[:len(exe)-len(".exe")]+".new.exe" {
		t.Fatalf("nextPath %s keeps no .exe", next)
	}
	if err := os.WriteFile(next, []byte("the new agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := swap(exe, next); err != nil {
		t.Fatalf("swap with the binary running: %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "the new agent" {
		t.Fatalf("after the swap exe is %q", b)
	}
	if err := putBack(exe); err != nil {
		t.Fatalf("putBack with the binary running: %v", err)
	}
	back, err := os.Stat(exe)
	if err != nil || !os.SameFile(back, running) {
		t.Fatalf("after putBack exe is not the running binary (%v)", err)
	}
	if b, _ := os.ReadFile(exe + ".failed"); string(b) != "the new agent" {
		t.Fatalf(".failed is %q", b)
	}
	removeLeftovers(exe)
	if _, err := os.Stat(exe + ".failed"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf(".failed still there: %v", err)
	}
}

// The planned restart's handshake (ADR-0103): an agent finds the guard of its own job only while that guard listens,
// and each request reaches the guard once. Run unelevated, hence Local\.
func TestPlannedRestartReachesOnlyItsGuard(t *testing.T) {
	defer func(ns string) { eventNamespace = ns }(eventNamespace)
	eventNamespace = `Local\`
	job := "test-" + time.Now().Format("150405.000000000")
	if AskGuardToStart(job) || guardListens(job, 0) {
		t.Fatal("a guard answered before any listens")
	}
	ctx, cancel := context.WithCancel(context.Background())
	requests, err := GuardRequests(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	if !guardListens(job, time.Second) {
		t.Fatal("the guard listens, but armGuard would not see it")
	}
	if AskGuardToStart(job + "-other") {
		t.Fatal("another job's agent reached this guard")
	}
	if !AskGuardToStart(job) {
		t.Fatal("the agent did not reach its guard")
	}
	select {
	case <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("the request did not arrive")
	}
	select {
	case <-requests:
		t.Fatal("one request arrived twice")
	case <-time.After(1200 * time.Millisecond):
	}
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for guardListens(job, 0) { // the guard ended: its event goes with it
		if time.Now().After(deadline) {
			t.Fatal("the event outlives the guard")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if AskGuardToStart(job) {
		t.Fatal("an agent reached a guard that has ended")
	}
}

func TestPutBackWithoutOldChangesNothing(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "vordr-agent.exe")
	if err := os.WriteFile(exe, []byte("agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := putBack(exe); err == nil {
		t.Fatal("putBack without .old reported success")
	}
	if b, _ := os.ReadFile(exe); string(b) != "agent" {
		t.Fatalf("exe is %q after a failed putBack", b)
	}
}
