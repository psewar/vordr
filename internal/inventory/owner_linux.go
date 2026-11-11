//go:build linux

package inventory

import (
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"
)

// handTo gives dir to the owner of like, so that a command running as that owner (asOwner) can write into it.
func handTo(dir, like string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	fi, err := os.Stat(like)
	if err != nil {
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid != 0 {
		return os.Chown(dir, int(st.Uid), int(st.Gid))
	}
	return nil
}

// asOwner makes cmd run as the owner of dir when the agent runs as root and dir belongs to someone else, with that
// user's HOME (pip and npm keep caches there). It returns the environment for cmd.
func asOwner(cmd *exec.Cmd, dir string) ([]string, error) {
	env := os.Environ()
	if dir == "" || os.Geteuid() != 0 {
		return env, nil
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid == 0 {
		return env, nil
	}
	u, err := user.LookupId(strconv.FormatUint(uint64(st.Uid), 10))
	if err != nil {
		return nil, err // an owner without an account: do not guess one
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: st.Uid, Gid: st.Gid}}
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "USER=") && !strings.HasPrefix(kv, "LOGNAME=") {
			out = append(out, kv)
		}
	}
	return append(out, "HOME="+u.HomeDir, "USER="+u.Username, "LOGNAME="+u.Username), nil
}
