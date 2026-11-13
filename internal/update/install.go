package update

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// The state dir holds plain files (no symlinks, so it works on Windows and
// wherever links are awkward):
//
//	bin/<version>/uptimeforge-agent  installed releases
//	current, previous                a version each: the newest install and the one before
//	bad                              versions rolled back, one per line
//	connected-<version>              written by that version once the hub has assigned it work

// binName is the agent executable's file name on this OS.
func binName() string {
	if runtime.GOOS == "windows" {
		return "uptimeforge-agent.exe"
	}
	return "uptimeforge-agent"
}

// binPath is where version v is installed.
func binPath(stateDir, v string) string {
	return filepath.Join(stateDir, "bin", v, binName())
}

// ConnectedMarker is the file, relative to the state dir, an agent of
// version v writes once it is connected and assigned (see MarkConnected).
func ConnectedMarker(v string) string { return "connected-" + v }

// MarkConnected records that version v reached the hub, which ends the
// launcher's health watch over it.
func MarkConnected(stateDir, v string) error {
	if strings.ContainsAny(v, `/\`) || v == "" || v == "." || v == ".." {
		return fmt.Errorf("version %q cannot name a marker file", v)
	}
	return writeAtomic(filepath.Join(stateDir, ConnectedMarker(v)), nil, 0o644)
}

// readVersion reads a version file (current, previous); "" if absent or not
// a release version.
func readVersion(stateDir, name string) string {
	b, err := os.ReadFile(filepath.Join(stateDir, name))
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(b))
	if !IsRelease(v) {
		return ""
	}
	return v
}

// readBad is the set of versions rolled back.
func readBad(stateDir string) map[string]bool {
	bad := map[string]bool{}
	b, err := os.ReadFile(filepath.Join(stateDir, "bad"))
	if err != nil {
		return bad
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v := strings.TrimSpace(line); v != "" {
			bad[v] = true
		}
	}
	return bad
}

// setBad adds v to or removes it from the bad list.
func setBad(stateDir, v string, isBad bool) error {
	b, err := os.ReadFile(filepath.Join(stateDir, "bad"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var out bytes.Buffer
	found := false
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == v {
			found = true
			if !isBad {
				continue
			}
		}
		out.WriteString(line + "\n")
	}
	if isBad == found {
		return nil // already so
	}
	if isBad {
		out.WriteString(v + "\n")
	}
	return writeAtomic(filepath.Join(stateDir, "bad"), out.Bytes(), 0o644)
}

// writeAtomic replaces path with data via a temp file in the same directory
// and a rename, so a crash leaves the old file or the new one, never half.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after the rename
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Install downloads release version for goos/arch with fetch (a file name
// under the hub's /download/), checks it against checksums.txt signed by
// one of keys, installs it under bin/<version>/ and makes it current, the
// old current becoming previous. On any error current and previous are as
// they were.
func Install(ctx context.Context, stateDir, version, goos, arch string, fetch func(name string) ([]byte, error), keys []ed25519.PublicKey) error {
	if !IsRelease(version) {
		return fmt.Errorf("%q is not a release version", version)
	}
	if len(keys) == 0 {
		return ErrNoKeys
	}
	sums, err := fetch("checksums.txt")
	if err != nil {
		return fmt.Errorf("download checksums.txt: %w", err)
	}
	sig, err := fetch("checksums.txt.sig")
	if err != nil {
		return fmt.Errorf("download checksums.txt.sig: %w", err)
	}
	if err := Verify(keys, sums, sig); err != nil {
		return fmt.Errorf("checksums.txt: %w", err)
	}
	name := AssetName(goos, arch)
	want, err := checksumOf(sums, name)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	bin, err := fetch(name)
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	if got := sha256.Sum256(bin); !bytes.Equal(got[:], want) {
		return fmt.Errorf("%s: sha256 does not match checksums.txt", name)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	path := binPath(stateDir, version)
	if err := writeAtomic(path, bin, 0o755); err != nil {
		os.Remove(filepath.Dir(path)) // only if empty
		return fmt.Errorf("install %s: %w", version, err)
	}

	// A connected marker left from an earlier try of this version would let
	// it skip the health watch: it goes before the version becomes current.
	if err := os.Remove(filepath.Join(stateDir, ConnectedMarker(version))); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("install %s: stale connected marker: %w", version, err)
	}

	// The old current falls back to previous, unless it was rolled back or
	// its binary is gone: then the old previous stays the way back.
	oldCur, oldPrev := readVersion(stateDir, "current"), readVersion(stateDir, "previous")
	prev := oldPrev
	if oldCur != "" && !readBad(stateDir)[oldCur] && isFile(binPath(stateDir, oldCur)) {
		prev = oldCur
	}
	if prev == version {
		prev = ""
	}
	if prev != oldPrev {
		if err := writeVersion(stateDir, "previous", prev); err != nil {
			return fmt.Errorf("install %s: %w", version, err)
		}
	}
	if err := writeAtomic(filepath.Join(stateDir, "current"), []byte(version+"\n"), 0o644); err != nil {
		if prev != oldPrev {
			writeVersion(stateDir, "previous", oldPrev) // best effort: put it back
		}
		return fmt.Errorf("install %s: %w", version, err)
	}

	// A re-install is a fresh try: no verdict carries over (nor, above, a
	// connection).
	setBad(stateDir, version, false)
	prune(stateDir, version, prev)
	return nil
}

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// writeVersion writes a version file, or removes it for "".
func writeVersion(stateDir, name, v string) error {
	path := filepath.Join(stateDir, name)
	if v == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeAtomic(path, []byte(v+"\n"), 0o644)
}

// prune removes installed versions other than keep: routers have little
// flash. Errors are ignored (Windows cannot remove a running binary; the
// next install tries again).
func prune(stateDir string, keep ...string) {
	entries, err := os.ReadDir(filepath.Join(stateDir, "bin"))
	if err != nil {
		return
	}
	for _, e := range entries {
		kept := false
		for _, k := range keep {
			kept = kept || (k != "" && e.Name() == k)
		}
		if !kept {
			os.RemoveAll(filepath.Join(stateDir, "bin", e.Name()))
		}
	}
}

// checksumOf finds name in a sha256sum listing ("<hex>  <name>", or
// "<hex> *<name>" for binary mode).
func checksumOf(sums []byte, name string) ([]byte, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 || strings.TrimPrefix(f[1], "*") != name {
			continue
		}
		sum, err := hex.DecodeString(f[0])
		if err != nil || len(sum) != sha256.Size {
			return nil, fmt.Errorf("checksums.txt: bad sha256 for %s", name)
		}
		return sum, nil
	}
	return nil, fmt.Errorf("checksums.txt does not list %s", name)
}
