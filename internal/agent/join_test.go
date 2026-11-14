package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func joinHub(t *testing.T, status int, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/api/agent/join" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected request %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		var body struct{ Code, Hostname, OS, Arch string }
		json.NewDecoder(r.Body).Decode(&body)
		if body.Code != "INV-1" || body.OS != runtime.GOOS || body.Arch != runtime.GOARCH || body.Hostname == "" {
			t.Errorf("body %+v", body)
		}
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"id": "p1", "token": "secret-token"})
	}))
}

func TestEnrollSavesToken(t *testing.T) {
	var calls atomic.Int32
	srv := joinHub(t, 200, &calls)
	defer srv.Close()
	dir := filepath.Join(t.TempDir(), "state")
	tok, err := Enroll(context.Background(), srv.URL, "INV-1", dir)
	if err != nil || tok != "secret-token" {
		t.Fatalf("token %q err %v", tok, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "token"))
	if err != nil || strings.TrimSpace(string(b)) != "secret-token" {
		t.Fatalf("file %q err %v", b, err)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(filepath.Join(dir, "token")); st.Mode().Perm() != 0o600 {
			t.Fatalf("token mode %v", st.Mode().Perm())
		}
		if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode %v", st.Mode().Perm())
		}
	}
	if got := readJoin(t, dir); got != wantHash("INV-1") {
		t.Fatalf("join file %q, want the code's hash", got)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(filepath.Join(dir, "join")); st.Mode().Perm() != 0o600 {
			t.Fatalf("join mode %v", st.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("want token and join only, got %v", entries)
	}
}

func wantHash(code string) string {
	h := sha256.Sum256([]byte(code))
	return hex.EncodeToString(h[:])
}

func readJoin(t *testing.T, dir string) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(dir, "join"))
	return strings.TrimSpace(string(b))
}

// fakeEnrolled fakes an earlier enrolment: a token and, if code is set, the
// hash of the code it came from.
func fakeEnrolled(t *testing.T, dir, token, code string) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, "token"), []byte(token+"\n"), 0o600)
	if code != "" {
		os.WriteFile(filepath.Join(dir, "join"), []byte(wantHash(code)+"\n"), 0o600)
	}
}

// The code is single use: once the token is saved a restart, or join.sh run
// again with the same spent code, must not ask the hub again (it would
// answer 410).
func TestEnrollSameSpentCodeKeepsSavedToken(t *testing.T) {
	var calls atomic.Int32
	srv := joinHub(t, 410, &calls)
	defer srv.Close()
	dir := t.TempDir()
	fakeEnrolled(t, dir, "saved-token", "INV-1")
	tok, err := Enroll(context.Background(), srv.URL, "INV-1", dir)
	if err != nil || tok != "saved-token" {
		t.Fatalf("token %q err %v", tok, err)
	}
	// Without a code (the token alone) it starts too.
	if tok, err := Enroll(context.Background(), srv.URL, "", dir); err != nil || tok != "saved-token" {
		t.Fatalf("no code: token %q err %v", tok, err)
	}
	if calls.Load() != 0 {
		t.Fatal("the hub was asked although a token from this code is saved")
	}
}

// A point deleted in the UI and added again gets a new invite code; the token
// saved from the old one is dead, so the new code is exchanged and replaces it.
// A token with no record of its code is treated the same way.
func TestEnrollNewCodeReplacesStaleToken(t *testing.T) {
	for name, oldCode := range map[string]string{"other code": "INV-OLD", "no code saved": ""} {
		var calls atomic.Int32
		srv := joinHub(t, 200, &calls)
		dir := t.TempDir()
		fakeEnrolled(t, dir, "stale-token", oldCode)
		tok, err := Enroll(context.Background(), srv.URL, "INV-1", dir)
		srv.Close()
		if err != nil || tok != "secret-token" {
			t.Fatalf("%s: token %q err %v", name, tok, err)
		}
		if calls.Load() != 1 {
			t.Fatalf("%s: the hub was asked %d times, want 1", name, calls.Load())
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "token")); strings.TrimSpace(string(b)) != "secret-token" {
			t.Fatalf("%s: saved token %q", name, b)
		}
		if got := readJoin(t, dir); got != wantHash("INV-1") {
			t.Fatalf("%s: join file %q, want the new code's hash", name, got)
		}
	}
}

// A new code the hub refuses leaves the old state alone.
func TestEnrollRefusedNewCodeKeepsState(t *testing.T) {
	var calls atomic.Int32
	srv := joinHub(t, 410, &calls)
	defer srv.Close()
	dir := t.TempDir()
	fakeEnrolled(t, dir, "stale-token", "INV-OLD")
	if _, err := Enroll(context.Background(), srv.URL, "INV-1", dir); err == nil {
		t.Fatal("a refused code must fail the start")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "token")); strings.TrimSpace(string(b)) != "stale-token" {
		t.Fatalf("token %q", b)
	}
	if got := readJoin(t, dir); got != wantHash("INV-OLD") {
		t.Fatalf("join file %q", got)
	}
}

func TestEnrollRejectedCode(t *testing.T) {
	var calls atomic.Int32
	srv := joinHub(t, 410, &calls)
	defer srv.Close()
	dir := t.TempDir()
	_, err := Enroll(context.Background(), srv.URL, "INV-1", dir)
	if err == nil || err.Error() != "invite code is used or expired: ask for a new one" {
		t.Fatalf("err %v", err)
	}
	if _, e := os.Stat(filepath.Join(dir, "token")); e == nil {
		t.Fatal("a token file appeared")
	}
	srv404 := joinHub(t, 404, &calls)
	defer srv404.Close()
	if _, err := Enroll(context.Background(), srv404.URL, "INV-1", dir); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("404: %v", err)
	}
}

func TestJoinURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://hub.example.com":            "https://hub.example.com/api/agent/join",
		"https://hub.example.com/":           "https://hub.example.com/api/agent/join",
		"http://127.0.0.1:8080":              "http://127.0.0.1:8080/api/agent/join",
		"wss://hub.example.com/api/agent/ws": "https://hub.example.com/api/agent/join",
		"hub.example.com":                    "https://hub.example.com/api/agent/join",
	} {
		if got := JoinURL(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}
