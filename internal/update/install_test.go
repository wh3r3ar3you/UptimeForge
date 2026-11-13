package update

import (
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
	"testing"
)

// release is a fake set of release files for one platform, served by fetch.
type release struct {
	files map[string][]byte
}

func newRelease(t *testing.T, priv ed25519.PrivateKey, goos, arch string, bin []byte) *release {
	t.Helper()
	name := AssetName(goos, arch)
	sum := sha256.Sum256(bin)
	sums := fmt.Sprintf("%s  uptimeforge-agent_linux_mips\n%s  %s\n", strings.Repeat("0", 64), hex.EncodeToString(sum[:]), name)
	return &release{files: map[string][]byte{
		"checksums.txt":     []byte(sums),
		"checksums.txt.sig": append(Sign(priv, []byte(sums)), '\n'),
		name:                bin,
	}}
}

func (r *release) fetch(name string) ([]byte, error) {
	b, ok := r.files[name]
	if !ok {
		return nil, fmt.Errorf("%s: HTTP 404", name)
	}
	return b, nil
}

func readState(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func writeState(t *testing.T, dir, name, val string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(val+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInstallVerifies(t *testing.T) {
	ctx := context.Background()
	pub, priv := newKey(t)
	keys := []ed25519.PublicKey{pub}
	goos, arch := runtime.GOOS, ReleaseArch()
	dir := t.TempDir()

	install := func(version string, bin []byte) error {
		return Install(ctx, dir, version, goos, arch, newRelease(t, priv, goos, arch, bin).fetch, keys)
	}
	if err := install("0.3.0", []byte("agent 0.3.0")); err != nil {
		t.Fatal(err)
	}
	path := binPath(dir, "0.3.0")
	if filepath.Base(path) != binName() || filepath.Base(filepath.Dir(path)) != "0.3.0" {
		t.Fatalf("installed at %s", path)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "agent 0.3.0" {
		t.Fatalf("installed binary: %q %v", got, err)
	}
	if st, _ := os.Stat(path); runtime.GOOS != "windows" && st.Mode().Perm()&0o111 == 0 {
		t.Fatalf("not executable: %v", st.Mode())
	}
	if readState(t, dir, "current") != "0.3.0" || readState(t, dir, "previous") != "" {
		t.Fatalf("current %q previous %q", readState(t, dir, "current"), readState(t, dir, "previous"))
	}

	if err := install("0.4.0", []byte("agent 0.4.0")); err != nil {
		t.Fatal(err)
	}
	if readState(t, dir, "current") != "0.4.0" || readState(t, dir, "previous") != "0.3.0" {
		t.Fatal("the old current must become previous")
	}

	// Every way the files can fail to be trusted leaves current alone.
	_, stranger := newKey(t)
	good := []byte("agent 0.5.0")
	for name, rel := range map[string]*release{
		"foreign key": newRelease(t, stranger, goos, arch, good),
		"wrong sha256": func() *release {
			r := newRelease(t, priv, goos, arch, good)
			r.files[AssetName(goos, arch)] = []byte("agent 0.5.0, tampered")
			return r
		}(),
		"not in checksums": newRelease(t, priv, goos, "mips64", good),
		"no signature": func() *release {
			r := newRelease(t, priv, goos, arch, good)
			delete(r.files, "checksums.txt.sig")
			return r
		}(),
		"no binary": func() *release {
			r := newRelease(t, priv, goos, arch, good)
			delete(r.files, AssetName(goos, arch))
			return r
		}(),
	} {
		if err := Install(ctx, dir, "0.5.0", goos, arch, rel.fetch, keys); err == nil {
			t.Errorf("%s: installed", name)
		}
		if readState(t, dir, "current") != "0.4.0" || readState(t, dir, "previous") != "0.3.0" {
			t.Fatalf("%s: current %q previous %q", name, readState(t, dir, "current"), readState(t, dir, "previous"))
		}
		if _, err := os.Stat(binPath(dir, "0.5.0")); err == nil {
			t.Fatalf("%s: an untrusted binary was left in bin/", name)
		}
	}
	for _, v := range []string{"dev", "../0.5.0", "v0.5.0"} {
		if err := install(v, good); err == nil {
			t.Errorf("version %q installed", v)
		}
	}
	if err := Install(ctx, dir, "0.5.0", goos, arch, newRelease(t, priv, goos, arch, good).fetch, nil); !errors.Is(err, ErrNoKeys) {
		t.Fatalf("no keys: %v", err)
	}
	if readState(t, dir, "current") != "0.4.0" {
		t.Fatal("current changed")
	}

	// A third version prunes bin/ to current and previous.
	if err := install("0.5.0", good); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bin", "0.3.0")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("0.3.0 kept: %v", err)
	}
	for _, v := range []string{"0.4.0", "0.5.0"} {
		if _, err := os.Stat(binPath(dir, v)); err != nil {
			t.Fatalf("%s pruned: %v", v, err)
		}
	}
}

// A sha256sum line may mark the file binary with '*'.
func TestInstallBinaryMarker(t *testing.T) {
	pub, priv := newKey(t)
	goos, arch := "linux", "mipsle"
	bin := []byte("router agent")
	sum := sha256.Sum256(bin)
	sums := []byte(hex.EncodeToString(sum[:]) + " *" + AssetName(goos, arch) + "\n")
	rel := &release{files: map[string][]byte{"checksums.txt": sums, "checksums.txt.sig": Sign(priv, sums), AssetName(goos, arch): bin}}
	dir := t.TempDir()
	if err := Install(context.Background(), dir, "0.3.0", goos, arch, rel.fetch, []ed25519.PublicKey{pub}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(binPath(dir, "0.3.0")); !bytes.Equal(got, bin) {
		t.Fatal("not installed")
	}
}

// An explicit re-install of a version that was rolled back is a fresh try:
// it leaves bad and loses its old connected marker. A bad current does not
// push a good previous out.
func TestInstallClearsBadAndKeepsGoodPrevious(t *testing.T) {
	pub, priv := newKey(t)
	keys := []ed25519.PublicKey{pub}
	goos, arch := runtime.GOOS, ReleaseArch()
	dir := t.TempDir()
	ctx := context.Background()
	for _, v := range []string{"0.3.0", "0.4.0"} {
		if err := Install(ctx, dir, v, goos, arch, newRelease(t, priv, goos, arch, []byte(v)).fetch, keys); err != nil {
			t.Fatal(err)
		}
	}
	writeState(t, dir, "bad", "0.4.0\n0.9.9")
	writeState(t, dir, ConnectedMarker("0.4.0"), "")

	if err := Install(ctx, dir, "0.5.0", goos, arch, newRelease(t, priv, goos, arch, []byte("0.5.0")).fetch, keys); err != nil {
		t.Fatal(err)
	}
	if readState(t, dir, "current") != "0.5.0" || readState(t, dir, "previous") != "0.3.0" {
		t.Fatalf("a bad current replaced the good previous: current %q previous %q", readState(t, dir, "current"), readState(t, dir, "previous"))
	}

	if err := Install(ctx, dir, "0.4.0", goos, arch, newRelease(t, priv, goos, arch, []byte("0.4.0")).fetch, keys); err != nil {
		t.Fatal(err)
	}
	if bad := readBad(dir); bad["0.4.0"] || !bad["0.9.9"] {
		t.Fatalf("bad after re-install: %v", bad)
	}
	if _, err := os.Stat(filepath.Join(dir, ConnectedMarker("0.4.0"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale connected marker kept")
	}
	if readState(t, dir, "current") != "0.4.0" || readState(t, dir, "previous") != "0.5.0" {
		t.Fatal("re-install did not become current")
	}
}
