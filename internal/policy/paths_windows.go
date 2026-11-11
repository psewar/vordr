//go:build windows

package policy

import (
	"os"
	"path/filepath"
)

var (
	programData       = programDataDir()
	DefaultConfigPath = filepath.Join(programData, "vordr", "agent.yaml")
	DefaultStateDir   = filepath.Join(programData, "vordr")
)

func programDataDir() string {
	if d := os.Getenv("ProgramData"); d != "" {
		return d
	}
	return `C:\ProgramData`
}
