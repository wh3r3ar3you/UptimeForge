package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// agentSums caches the sha256 of each agent binary by size and mtime: the
// checksums are public, and hashing every binary per request would let
// anyone keep the hub busy.
type agentSums struct {
	mu   sync.Mutex
	sums map[string]agentSum
}

type agentSum struct {
	size int64
	mod  time.Time
	sum  string
}

// checksums serves checksums.txt for /download in the format of a release
// (sha256, two spaces, file name). A checksums.txt shipped next to the
// binaries is served as is; otherwise it is made from the binaries there.
func (s *Server) checksums(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	shipped := filepath.Join(s.agentDir, "checksums.txt")
	if st, err := os.Stat(shipped); err == nil && st.Mode().IsRegular() {
		http.ServeFile(w, r, shipped)
		return
	}
	body, err := s.agentSums.list(s.agentDir)
	if err != nil {
		s.log.Warn("agent checksums", "err", err)
		http.Error(w, "checksums unavailable", http.StatusInternalServerError)
		return
	}
	if body == "" {
		http.NotFound(w, r)
		return
	}
	io.WriteString(w, body)
}

func (c *agentSums) list(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sums == nil {
		c.sums = map[string]agentSum{}
	}
	var names []string
	for _, e := range entries {
		if !e.Type().IsRegular() || !agentFile.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return "", err
		}
		cur, ok := c.sums[e.Name()]
		if !ok || cur.size != info.Size() || !cur.mod.Equal(info.ModTime()) {
			sum, err := fileSHA256(filepath.Join(dir, e.Name()))
			if err != nil {
				return "", err
			}
			cur = agentSum{size: info.Size(), mod: info.ModTime(), sum: sum}
			c.sums[e.Name()] = cur
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "%s  %s\n", c.sums[n].sum, n)
	}
	return b.String(), nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// agentFilesTTL is how long agentFiles trusts its last look at the dir: the
// hub asks on every tick, while the dir changes with a new image at most.
const agentFilesTTL = 10 * time.Second

// agentFiles tells the hub (hub.AgentFiles) what /download/ serves agents to
// update from: checksums.txt.sig and the agent binaries in the agents dir.
type agentFiles struct {
	dir   string
	mu    sync.Mutex
	read  time.Time
	names map[string]bool
}

func newAgentFiles(dir string) *agentFiles { return &agentFiles{dir: dir} }

// Signed says the release signature is there to serve.
func (f *agentFiles) Signed() bool { return f.list()["checksums.txt.sig"] }

// Has says an agent binary of that name is there to serve.
func (f *agentFiles) Has(name string) bool { return agentFile.MatchString(name) && f.list()[name] }

// expire makes the next question read the dir again.
func (f *agentFiles) expire() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.read = time.Time{}
}

// list is the regular files in the dir, read at most every agentFilesTTL;
// none if it cannot be read.
func (f *agentFiles) list() map[string]bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.names != nil && time.Since(f.read) < agentFilesTTL {
		return f.names
	}
	f.names, f.read = map[string]bool{}, time.Now()
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		return f.names
	}
	for _, e := range entries {
		if e.Type().IsRegular() {
			f.names[e.Name()] = true
		}
	}
	return f.names
}
