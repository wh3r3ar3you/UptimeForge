// Package remnawave is a read-only client for the Remnawave panel REST API,
// shaped after @remnawave/backend-contract 3.4.x.
package remnawave

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Client struct {
	BaseURL string            // panel root: https://panel.example.com or http://127.0.0.1:3000
	Token   string            // API token from the panel (admin JWTs are refused for API calls)
	TZ      string            // IANA zone for "today"/calendar periods in BandwidthStats; panel default is UTC
	Headers map[string]string // extra headers, applied last (e.g. Cookie for a protected proxy)
	HTTP    *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{BaseURL: baseURL, Token: token, HTTP: &http.Client{Timeout: 20 * time.Second}}
}

type APIError struct {
	Method, Path string
	Status       int
	Code         string // panel errorCode, e.g. "A048"; empty for plain NestJS errors
	Message      string
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("remnawave: %s %s: HTTP %d", e.Method, e.Path, e.Status)
	if e.Code != "" {
		s += " " + e.Code
	}
	return s + ": " + e.Message
}

// Nodes lists all nodes with their live state (GET /api/nodes/).
func (c *Client) Nodes(ctx context.Context) ([]Node, error) {
	return get[[]Node](ctx, c, "/api/nodes/", nil)
}

// SystemStats is the home-page counters block (GET /api/system/stats).
func (c *Client) SystemStats(ctx context.Context) (*SystemStats, error) {
	return get[*SystemStats](ctx, c, "/api/system/stats", nil)
}

// BandwidthStats is total node traffic per period vs the previous period (GET /api/system/stats/bandwidth).
func (c *Client) BandwidthStats(ctx context.Context) (*BandwidthStats, error) {
	var q url.Values
	if c.TZ != "" {
		q = url.Values{"tz": {c.TZ}}
	}
	return get[*BandwidthStats](ctx, c, "/api/system/stats/bandwidth", q)
}

// Health returns runtime metrics of each panel process (GET /api/system/health).
func (c *Client) Health(ctx context.Context) ([]RuntimeMetric, error) {
	r, err := get[struct {
		RuntimeMetrics []RuntimeMetric `json:"runtimeMetrics"`
	}](ctx, c, "/api/system/health", nil)
	return r.RuntimeMetrics, err
}

// NodesMetrics is the realtime per-node view from the panel's Prometheus
// exporter (GET /api/system/nodes/metrics). The panel returns an empty list,
// not an error, when its metrics endpoint is unreachable.
func (c *Client) NodesMetrics(ctx context.Context) ([]NodeMetrics, error) {
	r, err := get[struct {
		Nodes []NodeMetrics `json:"nodes"`
	}](ctx, c, "/api/system/nodes/metrics", nil)
	return r.Nodes, err
}

// NodesStatistics is per-node daily traffic for the last 7 days (GET /api/system/stats/nodes).
func (c *Client) NodesStatistics(ctx context.Context) ([]NodeDayTraffic, error) {
	r, err := get[struct {
		LastSevenDays []NodeDayTraffic `json:"lastSevenDays"`
	}](ctx, c, "/api/system/stats/nodes", nil)
	return r.LastSevenDays, err
}

// NodesUsage is per-node daily traffic for the UTC days start..end inclusive
// (GET /api/bandwidth-stats/nodes/). top <= 0 uses the panel default (20).
func (c *Client) NodesUsage(ctx context.Context, start, end time.Time, top int) (*NodesUsage, error) {
	q := url.Values{"start": {start.UTC().Format(time.DateOnly)}, "end": {end.UTC().Format(time.DateOnly)}}
	if top > 0 {
		q.Set("topNodesLimit", fmt.Sprint(top))
	}
	return get[*NodesUsage](ctx, c, "/api/bandwidth-stats/nodes/", q)
}

// Stats gathers the panel home-page numbers. Runtime is nil when the token
// may not read /api/system/health or the panel lacks it.
func (c *Client) Stats(ctx context.Context) (*Stats, error) {
	nodes, err := c.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	return c.PanelStats(ctx, nodes)
}

// PanelStats is Stats for a node list already fetched: the system,
// bandwidth and health endpoints, in parallel.
func (c *Client) PanelStats(ctx context.Context, nodes []Node) (*Stats, error) {
	var (
		s                Stats
		sys              *SystemStats
		bw               *BandwidthStats
		errS, errB, errH error
		wg               sync.WaitGroup
	)
	wg.Go(func() { sys, errS = c.SystemStats(ctx) })
	wg.Go(func() { bw, errB = c.BandwidthStats(ctx) })
	wg.Go(func() { s.Runtime, errH = c.Health(ctx) })
	wg.Wait()
	var ae *APIError
	if errors.As(errH, &ae) && (ae.Status == http.StatusForbidden || ae.Status == http.StatusNotFound) {
		errH = nil
	}
	if err := errors.Join(errS, errB, errH); err != nil {
		return nil, err
	}
	s.System, s.Bandwidth = *sys, *bw
	s.SetNodes(nodes)
	return &s, nil
}

// SetNodes recounts the node totals from a node list.
func (s *Stats) SetNodes(nodes []Node) {
	s.NodesTotal, s.NodesOnline, s.NodesDisabled = len(nodes), 0, 0
	for _, n := range nodes {
		switch {
		case n.IsDisabled:
			s.NodesDisabled++
		case n.IsConnected:
			s.NodesOnline++
		}
	}
}

var defaultHTTP = &http.Client{Timeout: 20 * time.Second}

func get[T any](ctx context.Context, c *Client, path string, q url.Values) (T, error) {
	var v T
	err := c.get(ctx, path, q, &v)
	return v, err
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	base := strings.TrimSuffix(strings.TrimRight(c.BaseURL, "/"), "/api")
	// The URL may carry credentials (user:password@, a key in the query):
	// errors about it never quote it.
	u, err := url.Parse(base + path)
	if err != nil || u.Host == "" {
		return errors.New("remnawave: bad panel URL: want https://panel.example.com")
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("remnawave: GET %s: %w", path, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "UptimeForge/1.0")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if u.Scheme == "http" {
		// The panel drops plain requests that do not look reverse-proxied over
		// HTTPS; these are the contract's REMNAWAVE_BYPASS_HTTPS_RESTRCTIONS.
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Forwarded-For", "127.0.0.1")
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	hc := c.HTTP
	if hc == nil {
		hc = defaultHTTP
	}
	resp, err := hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		if errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) {
			return fmt.Errorf("remnawave: GET %s: %w (the panel drops requests without X-Forwarded-For and X-Forwarded-Proto: https)", path, err)
		}
		return fmt.Errorf("remnawave: GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("remnawave: GET %s: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return newAPIError(http.MethodGet, path, resp.StatusCode, body)
	}
	var env struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("remnawave: GET %s: HTTP %d: not a panel API reply: %w", path, resp.StatusCode, err)
	}
	if len(env.Response) == 0 || string(env.Response) == "null" {
		return fmt.Errorf("remnawave: GET %s: reply has no \"response\" field", path)
	}
	if err := json.Unmarshal(env.Response, out); err != nil {
		return fmt.Errorf("remnawave: GET %s: decode: %w", path, err)
	}
	return nil
}

// newAPIError reads both the panel's {timestamp,path,message,errorCode} body
// and NestJS's default {statusCode,message,error}.
func newAPIError(method, path string, status int, body []byte) *APIError {
	e := &APIError{Method: method, Path: path, Status: status}
	var r struct {
		Message   json.RawMessage `json:"message"`
		ErrorCode string          `json:"errorCode"`
		Error     string          `json:"error"`
	}
	if json.Unmarshal(body, &r) == nil {
		e.Code = r.ErrorCode
		var msgs []string
		if json.Unmarshal(r.Message, &e.Message) != nil && json.Unmarshal(r.Message, &msgs) == nil {
			e.Message = strings.Join(msgs, "; ")
		}
		if e.Message == "" {
			e.Message = r.Error
		}
	} else if s := strings.TrimSpace(string(body)); s != "" && !strings.HasPrefix(s, "<") {
		e.Message = s[:min(len(s), 200)]
	}
	if e.Message == "" {
		e.Message = http.StatusText(status)
	}
	return e
}
