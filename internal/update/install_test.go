package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// release is a fake set of release files for one platform, served by fetch.
type release struct {
	files map[string][]byte
}

// newRelease is release version: its checksums.txt lists the binary and,
// as GoReleaser's does, the versioned archive of the same platform.
func newRelease(t *testing.T, priv ed25519.PrivateKey, version, goos, arch string, bin []byte) *release {
	t.Helper()
	name := AssetName(goos, arch)
	sum := sha256.Sum256(bin)
	sums := fmt.Sprintf("%s  uptimeforge-agent_linux_mips\n%s  %s\n%s  %s\n", strings.Repeat("0", 64), hex.EncodeToString(sum[:]), name,
		strings.Repeat("1", 64), ArchiveName(version, goos, arch))
	return &release{files: map[string][]byte{
		"checksums.txt":     []byte(sums),
		"checksums.txt.sig": append(Sign(priv, []byte(sums)), '\n'),
		name:                bin,
	}}
}

func (r *release) fetch(name string) (io.ReadCloser, error) {
	b, ok := r.files[name]
	if !ok {
		return nil, fmt.Errorf("%s: HTTP 404", name)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
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
		return Install(ctx, dir, version, goos, arch, newRelease(t, priv, version, goos, arch, bin).fetch, keys)
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
		"foreign key": newRelease(t, stranger, "0.5.0", goos, arch, good),
		"wrong sha256": func() *release {
			r := newRelease(t, priv, "0.5.0", goos, arch, good)
			r.files[AssetName(goos, arch)] = []byte("agent 0.5.0, tampered")
			return r
		}(),
		"not in checksums": newRelease(t, priv, "0.5.0", goos, "mips64", good),
		"no signature": func() *release {
			r := newRelease(t, priv, "0.5.0", goos, arch, good)
			delete(r.files, "checksums.txt.sig")
			return r
		}(),
		"no binary": func() *release {
			r := newRelease(t, priv, "0.5.0", goos, arch, good)
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
	if err := Install(ctx, dir, "0.5.0", goos, arch, newRelease(t, priv, "0.5.0", goos, arch, good).fetch, nil); !errors.Is(err, ErrNoKeys) {
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
	sums := []byte(hex.EncodeToString(sum[:]) + " *" + AssetName(goos, arch) + "\n" + strings.Repeat("1", 64) + " *" + ArchiveName("0.3.0", goos, arch) + "\n")
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
		if err := Install(ctx, dir, v, goos, arch, newRelease(t, priv, v, goos, arch, []byte(v)).fetch, keys); err != nil {
			t.Fatal(err)
		}
	}
	writeState(t, dir, "bad", "0.4.0\n0.9.9")
	writeState(t, dir, ConnectedMarker("0.4.0"), "")

	if err := Install(ctx, dir, "0.5.0", goos, arch, newRelease(t, priv, "0.5.0", goos, arch, []byte("0.5.0")).fetch, keys); err != nil {
		t.Fatal(err)
	}
	if readState(t, dir, "current") != "0.5.0" || readState(t, dir, "previous") != "0.3.0" {
		t.Fatalf("a bad current replaced the good previous: current %q previous %q", readState(t, dir, "current"), readState(t, dir, "previous"))
	}

	if err := Install(ctx, dir, "0.4.0", goos, arch, newRelease(t, priv, "0.4.0", goos, arch, []byte("0.4.0")).fetch, keys); err != nil {
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

// previous is the way back, so it only ever names an installed binary.
func TestInstallPromotesOnlyInstalledCurrent(t *testing.T) {
	pub, priv := newKey(t)
	keys := []ed25519.PublicKey{pub}
	goos, arch := runtime.GOOS, ReleaseArch()
	dir := t.TempDir()
	ctx := context.Background()
	for _, v := range []string{"0.3.0", "0.4.0"} {
		if err := Install(ctx, dir, v, goos, arch, newRelease(t, priv, v, goos, arch, []byte(v)).fetch, keys); err != nil {
			t.Fatal(err)
		}
	}
	os.RemoveAll(filepath.Join(dir, "bin", "0.4.0")) // current lost its binary
	if err := Install(ctx, dir, "0.5.0", goos, arch, newRelease(t, priv, "0.5.0", goos, arch, []byte("0.5.0")).fetch, keys); err != nil {
		t.Fatal(err)
	}
	if readState(t, dir, "current") != "0.5.0" || readState(t, dir, "previous") != "0.3.0" {
		t.Fatalf("current %q previous %q, want 0.5.0 and 0.3.0", readState(t, dir, "current"), readState(t, dir, "previous"))
	}
}

// A stale connected marker that cannot be removed would let the new binary
// skip its health watch: the install fails before it becomes current.
func TestInstallFailsOnStaleMarker(t *testing.T) {
	pub, priv := newKey(t)
	goos, arch := runtime.GOOS, ReleaseArch()
	dir := t.TempDir()
	marker := filepath.Join(dir, ConnectedMarker("0.3.0"))
	if err := os.MkdirAll(filepath.Join(marker, "stuck"), 0o755); err != nil { // a non-empty directory: Remove fails
		t.Fatal(err)
	}
	err := Install(context.Background(), dir, "0.3.0", goos, arch, newRelease(t, priv, "0.3.0", goos, arch, []byte("0.3.0")).fetch, []ed25519.PublicKey{pub})
	if err == nil {
		t.Fatal("installed with a stale marker in place")
	}
	if readState(t, dir, "current") != "" {
		t.Fatal("current written")
	}
}

// The signed checksums.txt says which release it belongs to: it lists the
// versioned archive of this platform. An old signed checksums.txt served
// under a newer version label (a downgrade) is refused before the download.
func TestInstallBindsVersionToChecksums(t *testing.T) {
	pub, priv := newKey(t)
	keys := []ed25519.PublicKey{pub}
	goos, arch := runtime.GOOS, ReleaseArch()
	dir := t.TempDir()
	ctx := context.Background()
	if err := Install(ctx, dir, "0.4.0", goos, arch, newRelease(t, priv, "0.4.0", goos, arch, []byte("0.4.0")).fetch, keys); err != nil {
		t.Fatal(err)
	}

	old := newRelease(t, priv, "0.3.0", goos, arch, []byte("agent 0.3.0, signed long ago"))
	var fetched []string
	fetch := func(name string) (io.ReadCloser, error) {
		fetched = append(fetched, name)
		return old.fetch(name)
	}
	err := Install(ctx, dir, "0.5.0", goos, arch, fetch, keys)
	if err == nil || !strings.Contains(err.Error(), ArchiveName("0.5.0", goos, arch)) {
		t.Fatalf("0.3.0 checksums installed as 0.5.0: %v", err)
	}
	if slices.Contains(fetched, AssetName(goos, arch)) {
		t.Fatal("downloaded the binary of a release that is not the one asked for")
	}

	// A checksums.txt that lists no archive at all names no release.
	bin := []byte("agent 0.5.0")
	sum := sha256.Sum256(bin)
	sums := []byte(hex.EncodeToString(sum[:]) + "  " + AssetName(goos, arch) + "\n")
	bare := &release{files: map[string][]byte{"checksums.txt": sums, "checksums.txt.sig": Sign(priv, sums), AssetName(goos, arch): bin}}
	if err := Install(ctx, dir, "0.5.0", goos, arch, bare.fetch, keys); err == nil {
		t.Fatal("installed from a checksums.txt that names no release")
	}
	if readState(t, dir, "current") != "0.4.0" {
		t.Fatalf("current %q", readState(t, dir, "current"))
	}
	if _, err := os.Stat(binPath(dir, "0.5.0")); err == nil {
		t.Fatal("0.5.0 installed")
	}
}

// ArchiveName follows packaged-agent's name_template in .goreleaser.yaml.
func TestArchiveName(t *testing.T) {
	for _, c := range []struct{ version, goos, arch, want string }{
		{"0.2.0", "linux", "amd64", "uptimeforge-agent_0.2.0_linux_amd64.tar.gz"},
		{"0.2.0-rc1", "linux", "arm64", "uptimeforge-agent_0.2.0-rc1_linux_arm64.tar.gz"},
		{"0.2.0", "linux", "armv7", "uptimeforge-agent_0.2.0_linux_armv7.tar.gz"},
		{"0.2.0", "linux", "mips", "uptimeforge-agent_0.2.0_linux_mips.tar.gz"},
		{"0.2.0", "linux", "mipsle", "uptimeforge-agent_0.2.0_linux_mipsle.tar.gz"},
		{"0.2.0", "darwin", "arm64", "uptimeforge-agent_0.2.0_darwin_arm64.tar.gz"},
		{"0.2.0", "windows", "amd64", "uptimeforge-agent_0.2.0_windows_amd64.zip"},
	} {
		if got := ArchiveName(c.version, c.goos, c.arch); got != c.want {
			t.Errorf("ArchiveName(%s, %s, %s) = %s, want %s", c.version, c.goos, c.arch, got, c.want)
		}
	}
	// The names above hold only while the release config says so.
	b, err := os.ReadFile(filepath.Join("..", "..", ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(b)
	_, packaged, ok := strings.Cut(cfg, "- id: packaged-agent\n")
	if !ok {
		t.Fatal(".goreleaser.yaml has no packaged-agent archive")
	}
	packaged, _, _ = strings.Cut(packaged, "\n  - id:")
	packaged, _, _ = strings.Cut(packaged, "\n\n")
	for _, want := range []string{
		"ids: [agent, agent-lite]",
		"formats: [tar.gz]",
		"- {goos: windows, formats: [zip]}",
		`name_template: "{{ .Binary }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}{{ with .Arm }}v{{ . }}{{ end }}"`,
	} {
		if !strings.Contains(packaged, want) {
			t.Errorf("packaged-agent in .goreleaser.yaml lacks %q:\n%s", want, packaged)
		}
	}
	if !strings.Contains(cfg, `goarm: ["7"]`) || !strings.Contains(cfg, "name_template: checksums.txt") {
		t.Error(".goreleaser.yaml: arm is no longer armv7 only, or the checksum file is renamed")
	}
}

// A binary over the cap is refused while it streams, and nothing of it
// stays on disk; the download is never held in memory whole.
func TestInstallCapsBinary(t *testing.T) {
	pub, priv := newKey(t)
	goos, arch := runtime.GOOS, ReleaseArch()
	dir := t.TempDir()
	rel := newRelease(t, priv, "0.3.0", goos, arch, []byte("small"))
	fetch := func(name string) (io.ReadCloser, error) {
		if name == AssetName(goos, arch) {
			return io.NopCloser(io.LimitReader(zeros{}, MaxBinarySize+1)), nil
		}
		return rel.fetch(name)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	err := Install(context.Background(), dir, "0.3.0", goos, arch, fetch, []ed25519.PublicKey{pub})
	runtime.ReadMemStats(&after)
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("a binary over %d bytes: %v", MaxBinarySize, err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 32<<20 {
		t.Fatalf("the download allocated %d MiB", grew>>20)
	}
	assertNoDownloadLeft(t, dir)
	if readState(t, dir, "current") != "" {
		t.Fatal("current written")
	}
}

// A binary whose sha256 does not match leaves no temp file behind.
func TestInstallMismatchCleansUp(t *testing.T) {
	pub, priv := newKey(t)
	goos, arch := runtime.GOOS, ReleaseArch()
	dir := t.TempDir()
	rel := newRelease(t, priv, "0.3.0", goos, arch, []byte("agent 0.3.0"))
	rel.files[AssetName(goos, arch)] = []byte("agent 0.3.0, tampered")
	err := Install(context.Background(), dir, "0.3.0", goos, arch, rel.fetch, []ed25519.PublicKey{pub})
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("tampered binary: %v", err)
	}
	assertNoDownloadLeft(t, dir)
}

// assertNoDownloadLeft: bin/ holds nothing but installed versions.
func assertNoDownloadLeft(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Join(dir, "bin"))
	for _, e := range entries {
		if !e.IsDir() || !IsRelease(e.Name()) {
			t.Errorf("left in bin/: %s", e.Name())
		}
		if _, err := os.Stat(filepath.Join(dir, "bin", e.Name(), binName())); err != nil {
			t.Errorf("bin/%s has no binary", e.Name())
		}
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// Install drops the connected markers of versions it pruned from bin/.
func TestInstallPrunesMarkers(t *testing.T) {
	pub, priv := newKey(t)
	keys := []ed25519.PublicKey{pub}
	goos, arch := runtime.GOOS, ReleaseArch()
	dir := t.TempDir()
	ctx := context.Background()
	for _, v := range []string{"0.3.0", "0.4.0"} {
		if err := Install(ctx, dir, v, goos, arch, newRelease(t, priv, v, goos, arch, []byte(v)).fetch, keys); err != nil {
			t.Fatal(err)
		}
		if err := MarkConnected(dir, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := MarkConnected(dir, "0.1.0"); err != nil { // the launcher's builtin, never in bin/
		t.Fatal(err)
	}
	if err := Install(ctx, dir, "0.5.0", goos, arch, newRelease(t, priv, "0.5.0", goos, arch, []byte("0.5.0")).fetch, keys); err != nil {
		t.Fatal(err)
	}
	for v, want := range map[string]bool{"0.1.0": false, "0.3.0": false, "0.4.0": true} {
		_, err := os.Stat(filepath.Join(dir, ConnectedMarker(v)))
		if got := err == nil; got != want {
			t.Errorf("connected-%s kept %v, want %v", v, got, want)
		}
	}
}
