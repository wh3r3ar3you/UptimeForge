// Package update verifies release files before an agent installs them.
//
// A release signs checksums.txt with Ed25519 (tools/sign, run by GoReleaser)
// into checksums.txt.sig: one line of standard base64 holding the raw
// 64-byte signature over the exact bytes of checksums.txt. An agent accepts
// the checksums only if one of the keys built into it (ReleaseKeys) verifies
// that signature, and a binary only if its sha256 is listed there.
package update

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrNoKeys means the binary carries no release key, so nothing it
	// downloads can be trusted (a dev build).
	ErrNoKeys = errors.New("no release keys")
	// ErrBadSignature means no key verifies the signature.
	ErrBadSignature = errors.New("release signature does not verify")
)

// Sign signs data and returns the signature as one line of base64, without
// a trailing newline.
func Sign(priv ed25519.PrivateKey, data []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data)))
}

// Verify checks a signature made by Sign against every key in turn and
// succeeds if any of them verifies it. Surrounding whitespace in sig (the
// newline a .sig file ends with) is ignored.
func Verify(keys []ed25519.PublicKey, data, sig []byte) error {
	if len(keys) == 0 {
		return ErrNoKeys
	}
	sig = bytes.TrimSpace(sig)
	if bytes.ContainsAny(sig, "\r\n") {
		return fmt.Errorf("%w: not one line", ErrBadSignature)
	}
	raw := make([]byte, base64.StdEncoding.DecodedLen(len(sig)))
	n, err := base64.StdEncoding.Decode(raw, sig)
	if err != nil || n != ed25519.SignatureSize {
		return fmt.Errorf("%w: not base64 of %d bytes", ErrBadSignature, ed25519.SignatureSize)
	}
	raw = raw[:n]
	for _, k := range keys {
		// ed25519.Verify panics on a key of the wrong size.
		if len(k) == ed25519.PublicKeySize && ed25519.Verify(k, data, raw) {
			return nil
		}
	}
	return ErrBadSignature
}

// ParsePublicKeys parses a comma-separated list of base64 Ed25519 public
// keys, the form tools/sign keygen prints.
func ParsePublicKeys(s string) ([]ed25519.PublicKey, error) {
	var keys []ed25519.PublicKey
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(part)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("public key %d: not base64 of %d bytes", len(keys)+1, ed25519.PublicKeySize)
		}
		keys = append(keys, ed25519.PublicKey(raw))
	}
	if len(keys) == 0 {
		return nil, ErrNoKeys
	}
	return keys, nil
}
