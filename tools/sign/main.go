// Command sign signs release checksums with Ed25519 in the format
// internal/update verifies. GoReleaser runs it on checksums.txt (signs in
// .goreleaser.yaml); the private key comes from the UF_RELEASE_KEY secret.
//
//	go run ./tools/sign keygen 2>pub.txt >priv.txt   # private base64 -> stdout, public -> stderr
//	UF_RELEASE_KEY=<private base64> go run ./tools/sign dist/checksums.txt   # writes dist/checksums.txt.sig
//	UF_RELEASE_PUBKEYS=<public base64>[,...] go run ./tools/sign verify dist/checksums.txt   # keys default to the built-in update.ReleaseKeys
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/wh3r3ar3you/uptimeforge/internal/update"
)

const usage = "usage: sign keygen | sign <file> | sign verify <file>"

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "sign:", err)
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	switch {
	case len(args) == 1 && args[0] == "keygen":
		return keygen(stdout, stderr)
	case len(args) == 2 && args[0] == "verify":
		return verify(args[1], getenv("UF_RELEASE_PUBKEYS"), stdout)
	case len(args) == 1 && args[0] != "verify":
		return sign(args[0], getenv("UF_RELEASE_KEY"))
	}
	return errors.New(usage)
}

// keygen keeps the two halves on separate streams so the private key can be
// piped straight into a secret store without reaching the screen.
func keygen(stdout, stderr io.Writer) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(stdout, base64.StdEncoding.EncodeToString(priv)); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stderr, base64.StdEncoding.EncodeToString(pub))
	return err
}

func sign(file, key string) error {
	priv, err := parsePrivateKey(key)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	return os.WriteFile(file+".sig", append(update.Sign(priv, data), '\n'), 0o644)
}

// parsePrivateKey never puts the key into its errors: they reach CI logs.
func parsePrivateKey(s string) (ed25519.PrivateKey, error) {
	if s == "" {
		return nil, errors.New("UF_RELEASE_KEY is not set")
	}
	raw, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace([]byte(s))))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("UF_RELEASE_KEY is not base64 of a %d-byte Ed25519 private key", ed25519.PrivateKeySize)
	}
	priv := ed25519.PrivateKey(raw)
	// The key is seed+public: a corrupted public half would sign with a key
	// no agent knows.
	if !ed25519.NewKeyFromSeed(priv.Seed()).Equal(priv) {
		return nil, errors.New("UF_RELEASE_KEY: public half does not match the seed")
	}
	return priv, nil
}

func verify(file, pubkeys string, stdout io.Writer) error {
	keys := update.ReleaseKeys
	if strings.TrimSpace(pubkeys) != "" {
		var err error
		if keys, err = update.ParsePublicKeys(pubkeys); err != nil {
			return fmt.Errorf("UF_RELEASE_PUBKEYS: %w", err)
		}
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(file + ".sig")
	if err != nil {
		return err
	}
	if err := update.Verify(keys, data, sig); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	_, err = fmt.Fprintf(stdout, "%s: signature OK\n", file)
	return err
}
