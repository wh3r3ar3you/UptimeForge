package checks

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func init() {
	registerType(TypeInfo{
		Type: "sftp", Category: "network",
		Fields: []Field{fHost, fPort("22"), required(fUsername),
			{Name: "auth", Kind: "select", Default: "password", Options: []string{"password", "key"}},
			{Name: "password", Kind: "password", Required: true, Secret: true, ShowIf: "auth=password"},
			{Name: "private_key", Kind: "textarea", Required: true, Secret: true, ShowIf: "auth=key"},
			{Name: "passphrase", Kind: "password", Secret: true, ShowIf: "auth=key"},
			{Name: "path", Kind: "text"},
			{Name: "host_key", Kind: "text", Advanced: true},
		},
		target: targetAddr,
		validate: func(p params) error {
			if p["auth"] == "key" {
				_, err := sftpSigner(p)
				return err
			}
			return nil
		},
	}, checkSFTP)
}

func sftpSigner(p params) (ssh.Signer, error) {
	key := []byte(p["private_key"])
	var signer ssh.Signer
	var err error
	if p["passphrase"] != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(p["passphrase"]))
	} else {
		signer, err = ssh.ParsePrivateKey(key)
	}
	if err != nil {
		// The parser's own message can quote parts of the key file.
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			return nil, errors.New("private_key is encrypted and needs a passphrase")
		}
		return nil, errors.New("private_key cannot be read, or the passphrase is wrong")
	}
	return signer, nil
}

var errHostKey = errors.New("host key does not match")

// checkSFTP logs in over SSH and starts the SFTP subsystem. Without host_key
// any server key is accepted: a monitor has no one to ask, and pinning is there
// for those who want it.
func checkSFTP(ctx context.Context, s *Spec, env *Env) Result {
	p := paramsOf(s)
	var auth ssh.AuthMethod
	if p["auth"] == "key" {
		signer, err := sftpSigner(p)
		if err != nil {
			return fail("config", err)
		}
		auth = ssh.PublicKeys(signer)
	} else {
		auth = ssh.Password(p["password"])
	}
	cfg := &ssh.ClientConfig{User: p["username"], Auth: []ssh.AuthMethod{auth}, ClientVersion: "SSH-2.0-UptimeForge"}
	if want := p["host_key"]; want != "" {
		cfg.HostKeyCallback = func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if ssh.FingerprintSHA256(key) != want {
				return fmt.Errorf("%w: server has %s", errHostKey, ssh.FingerprintSHA256(key))
			}
			return nil
		}
	} else {
		cfg.HostKeyCallback = ssh.InsecureIgnoreHostKey()
	}
	start := time.Now()
	conn, err := env.Dial(ctx, "tcp", p.addr())
	if err != nil {
		return fail(reasonOf(ctx, err), err)
	}
	defer conn.Close()
	stop := closeOnDone(ctx, conn)
	defer stop()
	sc, chans, reqs, err := ssh.NewClientConn(conn, p.addr(), cfg)
	if err != nil {
		switch {
		case errors.Is(err, errHostKey):
			return fail("host_key", err)
		case strings.Contains(err.Error(), "unable to authenticate"):
			return fail("auth", errors.New("server refused these credentials"))
		}
		return fail(reasonOf(ctx, err), err)
	}
	client := ssh.NewClient(sc, chans, reqs)
	defer client.Close()
	fs, err := sftp.NewClient(client)
	if err != nil {
		if ctx.Err() != nil {
			return fail("timeout", err)
		}
		return fail("bad_response", fmt.Errorf("SFTP subsystem: %w", err))
	}
	defer fs.Close()
	if path := p["path"]; path != "" {
		if _, err := fs.Stat(path); err != nil {
			switch {
			case errors.Is(err, os.ErrNotExist):
				return fail("not_found", fmt.Errorf("%s does not exist", path))
			case errors.Is(err, os.ErrPermission):
				return fail("auth", fmt.Errorf("no permission to see %s", path))
			}
			return fail(reasonOf(ctx, err), err)
		}
	} else if _, err := fs.Getwd(); err != nil {
		return fail(reasonOf(ctx, err), err)
	}
	return Result{LatencyMS: ms(time.Since(start))}
}
