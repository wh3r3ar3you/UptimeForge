package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DefaultStateDir is where the agent keeps its token after enrolling.
const DefaultStateDir = "/var/lib/uptimeforge-agent"

// JoinURL builds the enrolment endpoint from the same hub address the agent
// derives its WebSocket URL from.
func JoinURL(hub string) string {
	u := strings.TrimRight(hub, "/")
	u = strings.TrimSuffix(u, "/api/agent/ws")
	switch {
	case strings.HasPrefix(u, "wss://"):
		u = "https://" + strings.TrimPrefix(u, "wss://")
	case strings.HasPrefix(u, "ws://"):
		u = "http://" + strings.TrimPrefix(u, "ws://")
	case !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://"):
		u = "https://" + u
	}
	return u + "/api/agent/join"
}

// Enroll returns the probe token. A token saved in stateDir wins: an invite
// code is single use, so after the first start the code is never sent again.
// Otherwise the code is exchanged at the hub and the token is saved there.
func Enroll(ctx context.Context, hub, code, stateDir string) (string, error) {
	path := filepath.Join(stateDir, "token")
	if b, err := os.ReadFile(path); err == nil {
		if tok := strings.TrimSpace(string(b)); tok != "" {
			return tok, nil
		}
	}
	if code == "" {
		return "", errors.New("no invite code and no saved token")
	}
	host, _ := os.Hostname()
	body, _ := json.Marshal(map[string]string{"code": code, "hostname": host, "os": runtime.GOOS, "arch": runtime.GOARCH})
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, JoinURL(hub), bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("bad hub address: %w", plainURLErr(err))
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("cannot reach the hub to join: %w", plainURLErr(err))
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return "", errors.New("invite code is unknown: check it for typos")
	case http.StatusGone:
		return "", errors.New("invite code is used or expired: ask for a new one")
	case http.StatusTooManyRequests:
		return "", errors.New("too many join attempts: wait a minute and try again")
	default:
		return "", fmt.Errorf("hub refused to enrol this probe: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil || out.Token == "" {
		return "", errors.New("hub answered the join without a token")
	}
	if err := saveToken(stateDir, out.Token); err != nil {
		return "", fmt.Errorf("enrolled, but the token cannot be saved (the code is now spent): %w", err)
	}
	return out.Token, nil
}

// saveToken writes the token atomically with mode 0600 into a 0700 directory.
func saveToken(dir, token string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".token-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename
	if err := f.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		f.Close()
		return err
	}
	if _, err := f.WriteString(token + "\n"); err != nil {
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
	return os.Rename(tmp, filepath.Join(dir, "token"))
}

// CheckHubURL applies the Run rule (no clear-text hub beyond loopback unless
// insecure) so the join request, which carries the invite code, obeys it too.
func CheckHubURL(hub string, insecure bool) error { return checkHubURL(hub, insecure) }
