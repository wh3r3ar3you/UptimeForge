package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// Enroll returns the probe token. A code is single use, so the token it was
// swapped for is saved in stateDir with the hash of the code next to it
// (<stateDir>/join): a restart, or join.sh run again with the same spent
// code, starts on the saved token without asking the hub. A different code
// (the point was deleted and added again) is exchanged and replaces both; so
// is a code with no hash saved. Without a code the saved token is used.
func Enroll(ctx context.Context, hub, code, stateDir string) (string, error) {
	saved := readState(stateDir, "token")
	if saved != "" && (code == "" || readState(stateDir, "join") == codeHash(code)) {
		return saved, nil
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
	if err := saveState(stateDir, "token", out.Token); err != nil {
		return "", fmt.Errorf("enrolled, but the token cannot be saved (the code is now spent): %w", err)
	}
	if err := saveState(stateDir, "join", codeHash(code)); err != nil {
		return "", fmt.Errorf("enrolled, but the invite code's hash cannot be saved: %w", err)
	}
	return out.Token, nil
}

// codeHash is what is kept of an invite code: enough to tell it is the same
// one, useless to anyone who reads the state directory.
func codeHash(code string) string {
	h := sha256.Sum256([]byte(code))
	return hex.EncodeToString(h[:])
}

func readState(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// saveState writes a state file atomically with mode 0600 into a 0700 directory.
func saveState(dir, name, value string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+name+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename
	if err := f.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		f.Close()
		return err
	}
	if _, err := f.WriteString(value + "\n"); err != nil {
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
	return os.Rename(tmp, filepath.Join(dir, name))
}

// CheckHubURL applies the Run rule (no clear-text hub beyond loopback unless
// insecure) so the join request, which carries the invite code, obeys it too.
func CheckHubURL(hub string, insecure bool) error { return checkHubURL(hub, insecure) }
