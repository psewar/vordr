//go:build linux

package runner

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestStreamHandsOverStatusLines(t *testing.T) {
	var lines []string
	res := Exec{}.Stream(context.Background(), nil, func(l string) { lines = append(lines, l) },
		"sh", "-c", "echo pmstatus:a:50:x >&3; echo out; echo dlstatus:1:100:y >&3; exit 3")
	if !res.Ran() || res.ExitCode != 3 || string(res.Stdout) != "out\n" ||
		!slices.Equal(lines, []string{"pmstatus:a:50:x", "dlstatus:1:100:y"}) {
		t.Fatalf("result %+v, lines %v", res, lines)
	}
}

// A daemon a maintainer script starts can inherit the status pipe and stdout. The run ends with the command, not
// with the daemon, and its exit code still counts.
func TestStreamDoesNotWaitForInheritingDaemons(t *testing.T) {
	start := time.Now()
	var lines []string
	res := Exec{}.Stream(context.Background(), nil, func(l string) { lines = append(lines, l) },
		"sh", "-c", "sleep 20 & echo done >&3; exit 0")
	took := time.Since(start)
	if !res.Ran() || res.ExitCode != 0 || !slices.Equal(lines, []string{"done"}) || took > 15*time.Second {
		t.Fatalf("result %+v, lines %v, took %s", res, lines, took)
	}
}
