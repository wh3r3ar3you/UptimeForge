package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wh3r3ar3you/uptimeforge/internal/update"
)

// newPair runs the keygen subcommand and returns (private, public) base64.
func newPair(t *testing.T) (string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	if err := run([]string{"keygen"}, mapEnv(nil), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out.String()), strings.TrimSpace(errOut.String())
}

func mapEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// The shared format test: what tools/sign writes, internal/update accepts.
func TestSignThenVerify(t *testing.T) {
	priv, pub := newPair(t)
	rawPriv, err := base64.StdEncoding.DecodeString(priv)
	if err != nil || len(rawPriv) != ed25519.PrivateKeySize {
		t.Fatalf("keygen private key: %d bytes, %v", len(rawPriv), err)
	}
	keys, err := update.ParsePublicKeys(pub)
	if err != nil {
		t.Fatalf("keygen public key: %v", err)
	}
	if !keys[0].Equal(ed25519.PrivateKey(rawPriv).Public()) {
		t.Fatal("keygen printed a public key of another pair")
	}

	file := filepath.Join(t.TempDir(), "checksums.txt")
	data := []byte("abc  uptimeforge-agent_linux_amd64\n")
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := run([]string{file}, mapEnv(map[string]string{"UF_RELEASE_KEY": priv}), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile(file + ".sig")
	if err != nil {
		t.Fatal(err)
	}
	if err := update.Verify(keys, data, sig); err != nil {
		t.Fatalf("update.Verify rejects the tool's signature: %v", err)
	}
	if strings.Contains(out.String()+errOut.String(), priv) {
		t.Error("sign printed the private key")
	}

	// verify subcommand: the right key passes, another key and a changed file fail.
	_, otherPub := newPair(t)
	if err := run([]string{"verify", file}, mapEnv(map[string]string{"UF_RELEASE_PUBKEYS": otherPub + "," + pub}), &out, &errOut); err != nil {
		t.Errorf("verify with the key second in the list: %v", err)
	}
	if err := run([]string{"verify", file}, mapEnv(map[string]string{"UF_RELEASE_PUBKEYS": otherPub}), &out, &errOut); err == nil {
		t.Error("verify accepted a foreign key")
	}
	if err := os.WriteFile(file, append(data, 'x'), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", file}, mapEnv(map[string]string{"UF_RELEASE_PUBKEYS": pub}), &out, &errOut); err == nil {
		t.Error("verify accepted a changed file")
	}
}

func TestSignRefusesBadKeys(t *testing.T) {
	priv, _ := newPair(t)
	raw, _ := base64.StdEncoding.DecodeString(priv)
	// The public half no longer matches the seed: a corrupted secret.
	broken := append([]byte(nil), raw...)
	broken[40] ^= 1
	file := filepath.Join(t.TempDir(), "checksums.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string]string{
		"unset":     "",
		"not b64":   "!!!",
		"seed only": base64.StdEncoding.EncodeToString(raw[:32]),
		"mismatch":  base64.StdEncoding.EncodeToString(broken),
	} {
		var out, errOut bytes.Buffer
		err := run([]string{file}, mapEnv(map[string]string{"UF_RELEASE_KEY": key}), &out, &errOut)
		if err == nil {
			t.Errorf("%s: signed", name)
			continue
		}
		if key != "" && strings.Contains(err.Error(), key) {
			t.Errorf("%s: error shows the key: %v", name, err)
		}
		if _, statErr := os.Stat(file + ".sig"); statErr == nil {
			t.Errorf("%s: wrote a .sig", name)
		}
	}
}

func TestUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	for _, args := range [][]string{nil, {"verify"}, {"a", "b"}} {
		if err := run(args, mapEnv(nil), &out, &errOut); err == nil {
			t.Errorf("run(%q) succeeded", args)
		}
	}
}

// Without UF_RELEASE_PUBKEYS verify uses the keys built into the agent
// (update.ReleaseKeys), so the image build does not repeat the key.
func TestVerifyDefaultsToReleaseKeys(t *testing.T) {
	priv, pub := newPair(t)
	file := filepath.Join(t.TempDir(), "checksums.txt")
	if err := os.WriteFile(file, []byte("abc  x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := run([]string{file}, mapEnv(map[string]string{"UF_RELEASE_KEY": priv}), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	// Signed by a throwaway key: the built-in release key must reject it.
	if err := run([]string{"verify", file}, mapEnv(nil), &out, &errOut); err == nil {
		t.Fatal("verify without UF_RELEASE_PUBKEYS accepted a foreign signature")
	}
	// The built-in keys are in play: with them swapped for the throwaway
	// public key the same file passes.
	keys, err := update.ParsePublicKeys(pub)
	if err != nil {
		t.Fatal(err)
	}
	saved := update.ReleaseKeys
	update.ReleaseKeys = keys
	defer func() { update.ReleaseKeys = saved }()
	if err := run([]string{"verify", file}, mapEnv(nil), &out, &errOut); err != nil {
		t.Fatalf("verify did not fall back to update.ReleaseKeys: %v", err)
	}
}
