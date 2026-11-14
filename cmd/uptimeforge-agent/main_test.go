package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// launch refuses at once what the agent would refuse on every start, instead
// of restarting a child that exits with a usage error forever.
func TestParseLaunch(t *testing.T) {
	for _, k := range []string{"UF_HUB", "UF_TOKEN", "UF_JOIN", "UF_STATE_DIR"} {
		t.Setenv(k, "")
	}
	dir := t.TempDir()
	saved := t.TempDir()
	if err := os.WriteFile(filepath.Join(saved, "token"), []byte("tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args    []string
		ok      bool
		wantDir string
	}{
		{args: nil},
		{args: []string{"--token", "t"}},
		{args: []string{"--hub", "https://hub.example.com", "--state-dir", dir}},
		{args: []string{"--bogus"}},
		{args: []string{"--hub", "https://hub.example.com", "--token", "t", "--state-dir", dir}, ok: true, wantDir: dir},
		{args: []string{"-hub=https://hub.example.com", "--join", "CODE", "-state-dir=" + dir}, ok: true, wantDir: dir},
		{args: []string{"--hub", "https://hub.example.com", "--insecure", "--state-dir", saved}, ok: true, wantDir: saved},
	} {
		stateDir, err := parseLaunch(tc.args)
		if (err == nil) != tc.ok {
			t.Errorf("%q: err %v, want ok=%v", tc.args, err, tc.ok)
		}
		if tc.ok && stateDir != tc.wantDir {
			t.Errorf("%q: state dir %q, want %q", tc.args, stateDir, tc.wantDir)
		}
	}
}

// The agent image's entrypoint is "uptimeforge-agent launch": a one-off
// `docker run <image> check …` or `… version` must still reach the subcommand.
func TestOneOffThroughLaunch(t *testing.T) {
	for _, c := range []struct{ in, want []string }{
		{[]string{"launch", "check", "https://x"}, []string{"check", "https://x"}},
		{[]string{"launch", "version"}, []string{"version"}},
		{[]string{"launch", "--hub", "https://h"}, []string{"launch", "--hub", "https://h"}},
		{[]string{"check", "https://x"}, []string{"check", "https://x"}},
		{nil, nil},
	} {
		if got := oneOffArgs(c.in); strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("oneOffArgs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
