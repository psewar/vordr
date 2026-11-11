package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// What install-service decides and how it changes things, apart from the Windows service manager, so that it is tested
// on every platform (service_windows.go uses it).

type configAction int

const (
	configNew     configAction = iota // no agent.yaml installed yet: the given one is copied
	configSame                        // the installed file is the given one, or identical: nothing to copy
	configReplace                     // it differs and --replace-config names the reason: kept as backup, then replaced
)

// configPlan: what install-service does with the installed agent.yaml.
type configPlan struct {
	action configAction
	backup string   // configReplace: where the installed file is kept
	diff   []string // configReplace: the lines that change
}

// planConfig compares the installed agent.yaml with the given one. Changing a host's local policy is a decision, not a
// side effect of an update (ADR-0006): a different file is replaced only with a reason, as install-agent.sh does on
// Linux. Without one, the error names the state and the next step; nothing has changed then.
func planConfig(installed, given, reason string, now time.Time) (configPlan, error) {
	if strings.EqualFold(filepath.Clean(installed), filepath.Clean(given)) {
		return configPlan{action: configSame}, nil
	}
	_, err := os.Stat(installed)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return configPlan{action: configNew}, nil
	case err != nil:
		return configPlan{}, fmt.Errorf("cannot read %s: %v - nothing changed", installed, err)
	}
	old, err := os.ReadFile(installed)
	if err != nil {
		return configPlan{}, fmt.Errorf("cannot read %s: %v - nothing changed", installed, err)
	}
	want, err := os.ReadFile(given)
	if err != nil {
		return configPlan{}, fmt.Errorf("cannot read %s: %v - nothing changed", given, err)
	}
	if string(old) == string(want) {
		return configPlan{action: configSame}, nil
	}
	if reason == "" {
		return configPlan{}, fmt.Errorf("%s exists and differs from %s - compare them and decide yourself, "+
			"or replace it with --replace-config \"<reason>\"; nothing changed", installed, given)
	}
	return configPlan{action: configReplace, backup: installed + ".bak-" + now.Format("20060102-150405"),
		diff: lineDiff(old, want)}, nil
}

// lineDiff lists the lines that differ, "-" with the line number in a, "+" with the one in b, in file order (longest
// common subsequence; agent.yaml is short).
func lineDiff(a, b []byte) []string {
	x, y := lines(a), lines(b)
	// common[i][j]: length of the longest common subsequence of x[i:] and y[j:]
	common := make([][]int, len(x)+1)
	for i := range common {
		common[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				common[i][j] = common[i+1][j+1] + 1
			} else {
				common[i][j] = max(common[i+1][j], common[i][j+1])
			}
		}
	}
	var out []string
	for i, j := 0, 0; i < len(x) || j < len(y); {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			i, j = i+1, j+1
		case i < len(x) && (j == len(y) || common[i+1][j] >= common[i][j+1]):
			out = append(out, fmt.Sprintf("-%4d  %s", i+1, x[i]))
			i++
		default:
			out = append(out, fmt.Sprintf("+%4d  %s", j+1, y[j]))
			j++
		}
	}
	if len(out) == 0 && string(a) != string(b) {
		out = append(out, "      (only line endings or the final newline differ)")
	}
	return out
}

func lines(b []byte) []string {
	s := strings.TrimSuffix(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// step: one change install-service makes. undo puts back what do changed; nil when there is nothing to put back
// (an additive step such as installing PowerShell 7, or one before the service was touched).
type step struct {
	what string
	do   func() error
	undo func() error
}

// applied: how apply ended. failed is "" when every step ran.
type applied struct {
	failed string
	err    error
	stuck  []string // undos that failed, with the reason: these changes are still in place
}

// apply runs the steps in order. At the first failure it undoes the steps that ran, the failed one included (it may
// have changed something half-way), last first, and goes on past an undo that fails; the caller then states the
// resulting state as the service manager reports it.
func apply(steps []step, out, errOut io.Writer) applied {
	for n, st := range steps {
		err := st.do()
		if err == nil {
			fmt.Fprintln(out, "ok     "+st.what)
			continue
		}
		fmt.Fprintf(errOut, "failed %s: %v\n", st.what, err)
		res := applied{failed: st.what, err: err}
		for k := n; k >= 0; k-- {
			u := steps[k]
			if u.undo == nil {
				continue
			}
			if err := u.undo(); err != nil {
				fmt.Fprintf(errOut, "STUCK  %s: %v\n", u.what, err)
				res.stuck = append(res.stuck, fmt.Sprintf("%s: %v", u.what, err))
				continue
			}
			fmt.Fprintln(out, "undone "+u.what)
		}
		return res
	}
	return applied{}
}

// logLine appends one line to <state_dir>\agent.log, the agent's log on Windows, and says whether it got there.
func logLine(stateDir, msg string) error {
	f, err := os.OpenFile(filepath.Join(stateDir, "agent.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := log.New(f, "", log.LstdFlags).Output(1, msg); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
