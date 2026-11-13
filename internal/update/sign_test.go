package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestSignVerify(t *testing.T) {
	pub, priv := newKey(t)
	other, _ := newKey(t)
	data := []byte("0123abcd  uptimeforge-agent_linux_amd64\n")
	sig := Sign(priv, data)

	if bytes.ContainsAny(sig, "\r\n") {
		t.Fatalf("signature is not one line: %q", sig)
	}
	raw, err := base64.StdEncoding.DecodeString(string(sig))
	if err != nil || len(raw) != ed25519.SignatureSize {
		t.Fatalf("signature is not base64 of %d bytes: %q (%v)", ed25519.SignatureSize, sig, err)
	}

	if err := Verify([]ed25519.PublicKey{pub}, data, sig); err != nil {
		t.Errorf("own key: %v", err)
	}
	if err := Verify([]ed25519.PublicKey{other}, data, sig); err == nil {
		t.Error("foreign key accepted")
	}
	changed := append([]byte(nil), data...)
	changed[0] = '9'
	if err := Verify([]ed25519.PublicKey{pub}, changed, sig); err == nil {
		t.Error("changed data accepted")
	}
	if err := Verify([]ed25519.PublicKey{other, pub}, data, sig); err != nil {
		t.Errorf("matching key second in the list: %v", err)
	}
	// A .sig file ends with a newline; CRLF if someone edits it on Windows.
	if err := Verify([]ed25519.PublicKey{pub}, data, append(append([]byte(nil), sig...), "\r\n"...)); err != nil {
		t.Errorf("trailing newline: %v", err)
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	pub, priv := newKey(t)
	data := []byte("checksums")
	sig := Sign(priv, data)

	if err := Verify(nil, data, sig); err == nil {
		t.Error("no keys: accepted")
	}
	if err := Verify([]ed25519.PublicKey{pub[:10]}, data, sig); err == nil {
		t.Error("short public key: accepted")
	}
	for name, bad := range map[string][]byte{
		"empty":     nil,
		"not b64":   []byte("!!!"),
		"short sig": []byte(base64.StdEncoding.EncodeToString(make([]byte, 10))),
		"two lines": append(append(append([]byte(nil), sig...), '\n'), sig...),
	} {
		if err := Verify([]ed25519.PublicKey{pub}, data, bad); err == nil {
			t.Errorf("%s signature: accepted", name)
		}
	}
}

func TestReleaseKeys(t *testing.T) {
	for i, k := range ReleaseKeys {
		if len(k) != ed25519.PublicKeySize {
			t.Errorf("ReleaseKeys[%d]: %d bytes", i, len(k))
		}
	}
	pub, _ := newKey(t)
	if got := mustPublicKey(base64.StdEncoding.EncodeToString(pub)); !pub.Equal(got) {
		t.Errorf("mustPublicKey = %x, want %x", got, pub)
	}
	defer func() {
		if recover() == nil {
			t.Error("mustPublicKey accepted a truncated key")
		}
	}()
	mustPublicKey(base64.StdEncoding.EncodeToString(pub[:31]))
}

func TestParsePublicKeys(t *testing.T) {
	a, _ := newKey(t)
	b, _ := newKey(t)
	s := base64.StdEncoding.EncodeToString(a) + ", " + base64.StdEncoding.EncodeToString(b)
	keys, err := ParsePublicKeys(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || !a.Equal(keys[0]) || !b.Equal(keys[1]) {
		t.Fatalf("keys = %v", keys)
	}
	for _, bad := range []string{"", "!!!", base64.StdEncoding.EncodeToString(make([]byte, 31))} {
		if _, err := ParsePublicKeys(bad); err == nil {
			t.Errorf("ParsePublicKeys(%q) accepted", bad)
		}
	}
}
