// vordr-release signs agent releases on the operator's machine, away from the hub (ADR-0032, ADR-0139). The hub
// only passes a release on; agents install it only when the operator key in their own agent.yaml signed it.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/protocol"
)

var version = "dev"

const usageText = `vordr-release %s - sign agent releases away from the hub (ADR-0032, ADR-0139)

  vordr-release keygen <key-file>
        create the operator key (base64 Ed25519 seed, file mode 0600) and print its public key.
        Refuses when <key-file> exists: every agent trusts exactly this key, so it is never replaced.
  vordr-release public-key <key-file>
        print the public key for agent_update.public_key in agent.yaml and hub.yaml
  vordr-release sign <key-file> <X.Y.Z> <dist-dir> <out-dir> [--skip-version-check "<reason>"]
        sign every vordr-agent-<os>-<arch>[.exe] in <dist-dir> as release X.Y.Z and write the binaries,
        manifest.json and manifest.sig to <out-dir> (new or empty). Copy its content to <state_dir>/agent
        on the hub, the manifest last. Before signing, the binary for this machine's platform must answer
        "version" with X.Y.Z; without one, only --skip-version-check with a reason signs.
  vordr-release version

Exit 0 done, 1 failed, 2 refused (nothing written).
`

func usage() { fmt.Fprintf(os.Stderr, usageText, version) }

// refused is a precondition that does not hold; nothing was written.
type refused struct{ msg string }

func (r refused) Error() string { return r.msg }

func refuse(format string, a ...any) error { return refused{fmt.Sprintf(format, a...)} }

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = needArgs(os.Args[2:], 1, func(a []string) error { return keygen(a[0], os.Stdout) })
	case "public-key":
		err = needArgs(os.Args[2:], 1, func(a []string) error { return printPublic(a[0], os.Stdout) })
	case "sign":
		err = signCommand(os.Args[2:], os.Stdout)
	case "version":
		fmt.Println(version)
		return
	default:
		usage()
		os.Exit(2)
	}
	os.Exit(exitCode(err))
}

func exitCode(err error) int {
	var r refused
	switch {
	case err == nil:
		return 0
	case errors.As(err, &r):
		fmt.Fprintln(os.Stderr, "refused:", r.msg)
		return 2
	default:
		fmt.Fprintln(os.Stderr, "failed:", err)
		return 1
	}
}

func needArgs(args []string, n int, f func([]string) error) error {
	if len(args) != n {
		usage()
		return refuse("expected %d argument(s), got %d", n, len(args))
	}
	return f(args)
}

func signCommand(args []string, out io.Writer) error {
	var pos []string
	skipReason := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--skip-version-check" {
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return refuse("--skip-version-check needs a reason")
			}
			skipReason = args[i+1]
			i++
			continue
		}
		pos = append(pos, args[i])
	}
	if len(pos) != 4 {
		usage()
		return refuse("sign needs <key-file> <X.Y.Z> <dist-dir> <out-dir>")
	}
	return sign(pos[0], pos[1], pos[2], pos[3], skipReason, out)
}

// keygen creates a new operator key. The file is created exclusively: an existing key is never replaced.
func keygen(path string, out io.Writer) error {
	if _, err := os.Lstat(path); err == nil {
		return refuse("%s exists; the operator key is never replaced (every agent trusts exactly this key)", path)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	// O_EXCL as well: the check above names the state, this closes the race with another process creating the file.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(f, base64.StdEncoding.EncodeToString(priv.Seed())); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(out, "operator key written to %s; keep a backup apart from the hub (ADR-0035)\n", path)
	fmt.Fprintf(out, "public key: %s\n", base64.StdEncoding.EncodeToString(pub))
	return nil
}

func printPublic(path string, out io.Writer) error {
	priv, err := readKey(path)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)))
	return nil
}

// readKey reads a base64 Ed25519 seed (32 bytes). On Unix the file must not be readable by group or others.
func readKey(path string) (ed25519.PrivateKey, error) {
	st, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, refuse("%s does not exist (create it once with: vordr-release keygen %s)", path, path)
	}
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		return nil, refuse("%s is readable by others (mode %o); chmod 600 it first", path, st.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, refuse("%s holds no base64 Ed25519 seed of %d bytes", path, ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

var agentFile = regexp.MustCompile(`^vordr-agent-(linux|windows)-(amd64|arm64|arm)(\.exe)?$`)

func sign(keyPath, ver, distDir, outDir, skipReason string, out io.Writer) error {
	if !protocol.IsRelease(ver) {
		return refuse("%q is no release version X.Y.Z; agents take only releases (ADR-0034)", ver)
	}
	priv, err := readKey(keyPath)
	if err != nil {
		return err
	}
	bins, err := agentBinaries(distDir)
	if err != nil {
		return err
	}
	if err := emptyOrMissing(outDir); err != nil {
		return err
	}
	if err := checkVersion(distDir, bins, ver, skipReason, out); err != nil {
		return err
	}

	m := protocol.Manifest{Version: ver, Issued: time.Now().UTC().Truncate(time.Second)}
	for _, b := range bins {
		data, err := os.ReadFile(filepath.Join(distDir, b.File))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		b.SHA256, b.Size = hex.EncodeToString(sum[:]), int64(len(data))
		m.Binaries = append(m.Binaries, b)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	sig := protocol.SignManifest(raw, func(b []byte) []byte { return ed25519.Sign(priv, b) })
	// The agent's own check, before anything is written.
	if _, ok := protocol.VerifyManifest(raw, sig, priv.Public().(ed25519.PublicKey)); !ok {
		return fmt.Errorf("the signed manifest does not verify; nothing written")
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	// Binaries first, manifest last: a hub that reads the directory meanwhile sees the new release only when it is
	// complete (the same order the operator keeps when copying it to the hub).
	for _, b := range m.Binaries {
		if err := copyFile(filepath.Join(distDir, b.File), filepath.Join(outDir, b.File)); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(outDir, "manifest.sig"), sig, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "manifest.json"), raw, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "release %s signed into %s (issued %s)\n", ver, outDir, m.Issued.Format(time.RFC3339))
	for _, b := range m.Binaries {
		fmt.Fprintf(out, "  %-34s %10d  %s\n", b.File, b.Size, b.SHA256)
	}
	fmt.Fprintf(out, "public key: %s\n", base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)))
	return nil
}

// agentBinaries lists the agent builds in dir, one per platform, sorted by file name.
func agentBinaries(dir string) ([]protocol.ManifestBinary, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, refuse("%s does not exist (./build.sh <X.Y.Z> writes the binaries to dist/)", dir)
	}
	if err != nil {
		return nil, err
	}
	var bins []protocol.ManifestBinary
	for _, e := range entries {
		m := agentFile.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		if (m[1] == "windows") != (m[3] == ".exe") {
			return nil, refuse("%s: Windows builds end in .exe, others do not", e.Name())
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		if info.Size() == 0 || info.Size() > protocol.MaxBinarySize {
			return nil, refuse("%s: size %d is outside 1..%d bytes", e.Name(), info.Size(), protocol.MaxBinarySize)
		}
		bins = append(bins, protocol.ManifestBinary{OS: m[1], Arch: m[2], File: e.Name()})
	}
	if len(bins) == 0 {
		return nil, refuse("no vordr-agent-<os>-<arch> binaries in %s", dir)
	}
	sort.Slice(bins, func(i, j int) bool { return bins[i].File < bins[j].File })
	return bins, nil
}

func emptyOrMissing(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return refuse("%s is not empty; sign into a new directory", dir)
	}
	return nil
}

// checkVersion lets the build for this machine's platform say its version; the agent checks the same before it
// swaps itself in. Without such a build only an explicit reason skips the check.
func checkVersion(distDir string, bins []protocol.ManifestBinary, ver, skipReason string, out io.Writer) error {
	for _, b := range bins {
		if b.OS != runtime.GOOS || b.Arch != runtime.GOARCH {
			continue
		}
		got, err := exec.Command(filepath.Join(distDir, b.File), "version").Output()
		if err != nil {
			return fmt.Errorf("%s version: %w", b.File, err)
		}
		if v := strings.TrimSpace(string(got)); v != ver {
			return refuse("%s reports version %q, not %q (build with ./build.sh %s)", b.File, v, ver, ver)
		}
		return nil
	}
	if skipReason == "" {
		return refuse("no build for %s/%s in %s to confirm the version; build it too, or pass "+
			"--skip-version-check \"<reason>\"", runtime.GOOS, runtime.GOARCH, distDir)
	}
	fmt.Fprintf(out, "version check skipped: %s\n", skipReason)
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	o, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(o, in); err != nil {
		o.Close()
		return err
	}
	return o.Close()
}
