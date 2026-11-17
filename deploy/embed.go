package deploy

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

// AgentFS holds install-agent.sh and every file it installs from next to itself, laid out as in this folder.
//
//go:embed install-agent.sh rollback.sh systemd/vordr-agent.service systemd/vordr-agent-rollback.service
var AgentFS embed.FS

// AgentFile is one file of AgentFS with the mode it gets on disk.
type AgentFile struct {
	Name string // slash-separated, relative to the script's folder
	Mode fs.FileMode
}

// AgentFiles: the files of AgentFS. The script is run with bash and installs the others with modes of its own; these
// modes only keep the folder as it is in the repository.
var AgentFiles = []AgentFile{
	{"install-agent.sh", 0o755},
	{"rollback.sh", 0o755},
	{"systemd/vordr-agent.service", 0o644},
	{"systemd/vordr-agent-rollback.service", 0o644},
}

// WriteAgentFiles writes the files of AgentFiles into dir, so that "bash <dir>/install-agent.sh" runs as it does from
// this folder. dir must exist; the files are new in it.
func WriteAgentFiles(dir string) error {
	for _, f := range AgentFiles {
		raw, err := AgentFS.ReadFile(f.Name)
		if err != nil {
			return err
		}
		if err := WriteNew(filepath.Join(dir, filepath.FromSlash(f.Name)), raw, f.Mode); err != nil {
			return err
		}
	}
	return nil
}

// WriteNew writes a file that must not exist yet, creating its folder (0700) if needed, and gives it mode exactly (the
// umask does not narrow it).
func WriteNew(path string, raw []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}
