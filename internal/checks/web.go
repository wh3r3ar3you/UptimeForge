package checks

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func init() {
	registerType(TypeInfo{
		Type: "websocket-upgrade", Category: "web",
		Fields: []Field{
			{Name: "url", Kind: "text", Required: true, URL: true},
			{Name: "subprotocol", Kind: "text", Advanced: true},
			{Name: "headers", Kind: "textarea", Secret: true, Advanced: true},
			{Name: "ignore_accept", Kind: "bool", Advanced: true},
			fInsecure,
		},
		target: targetURL("url"),
		validate: func(p params) error {
			u, _ := url.Parse(p["url"])
			if u == nil || u.Scheme != "ws" && u.Scheme != "wss" {
				return errors.New("parameter url must start with ws:// or wss://")
			}
			if _, err := parseHeaders(p["headers"]); err != nil {
				return fmt.Errorf("parameter headers %w", err)
			}
			return nil
		},
	}, checkWebSocket)

	registerType(TypeInfo{
		Type: "globalping", Category: "web", Keyword: true,
		Fields: []Field{
			{Name: "subtype", Kind: "select", Required: true, Default: "ping", Options: []string{"ping", "http", "dns"}},
			{Name: "target", Kind: "text", Required: true, URL: true},
			{Name: "location", Kind: "text"},
			{Name: "ping_count", Kind: "number", Default: "3", ShowIf: "subtype=ping"},
			{Name: "record_type", Kind: "select", Default: "A", Options: dnsRecordNames(), ShowIf: "subtype=dns"},
			{Name: "method", Kind: "select", Default: "GET", Options: []string{"GET", "HEAD", "OPTIONS"}, ShowIf: "subtype=http"},
			{Name: "accepted_status", Kind: "text", Default: "200-299", ShowIf: "subtype=http"},
			{Name: "token", Kind: "password", Secret: true, Advanced: true},
		},
		target: func(p map[string]string) string {
			if p["subtype"] == "http" {
				return targetURL("target")(p)
			}
			return p["target"]
		},
		validate: func(p params) error {
			if p["subtype"] == "http" {
				if err := validateURLs("target", p.lines("target")); err != nil {
					return err
				}
				_, _, err := statusRange(p["accepted_status"])
				return err
			}
			if strings.ContainsAny(p["target"], "/@ \t\r\n") {
				return errors.New("parameter target must be a bare host name or address")
			}
			return nil
		},
	}, checkGlobalping)
}

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func checkWebSocket(ctx context.Context, s *Spec, env *Env) Result {
	p := paramsOf(s)
	u, err := url.Parse(p["url"])
	if err != nil {
		return fail("config", errors.New("bad url"))
	}
	u.Scheme = map[string]string{"ws": "http", "wss": "https"}[u.Scheme]
	client := newHTTPClient(env, p.bool("insecure"))
	defer client.CloseIdleConnections()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	nonce := make([]byte, 16)
	rand.Read(nonce)
	key := base64.StdEncoding.EncodeToString(nonce)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fail("config", plainErr(err))
	}
	req.Header.Set("User-Agent", "UptimeForge/1.0")
	headers, err := parseHeaders(p["headers"])
	if err != nil {
		return fail("config", fmt.Errorf("headers %w", err))
	}
	setHeaders(req, headers)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", key)
	if p["subprotocol"] != "" {
		req.Header.Set("Sec-WebSocket-Protocol", p["subprotocol"])
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		err = plainErr(err)
		return fail(reasonOf(ctx, err), err)
	}
	defer resp.Body.Close()
	r := Result{LatencyMS: ms(time.Since(start)), Info: map[string]string{"status_code": fmt.Sprint(resp.StatusCode)}}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		r.Status, r.Reason, r.Error = Down, "http_status", fmt.Sprintf("HTTP %d", resp.StatusCode)
		return r
	}
	if !p.bool("ignore_accept") && resp.Header.Get("Sec-WebSocket-Accept") != wsAcceptFor(key) {
		r.Status, r.Reason, r.Error = Down, "bad_response", "invalid Sec-WebSocket-Accept header"
		return r
	}
	if p["subprotocol"] != "" {
		got := resp.Header.Get("Sec-WebSocket-Protocol")
		if got == "" {
			r.Status, r.Reason, r.Error = Down, "bad_response", "server chose no subprotocol"
			return r
		}
		r.Info["subprotocol"] = got
	}
	if w, ok := resp.Body.(io.Writer); ok {
		w.Write(wsCloseFrame()) // be polite: it keeps our probe out of the server's abnormal-close logs
	}
	return r
}

func wsAcceptFor(key string) string {
	sum := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// wsCloseFrame is a masked close frame with status 1000.
func wsCloseFrame() []byte {
	mask := make([]byte, 4)
	rand.Read(mask)
	payload := binary.BigEndian.AppendUint16(nil, 1000)
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	return append(append([]byte{0x88, 0x80 | byte(len(payload))}, mask...), payload...)
}

// globalpingBase is replaced in tests.
var globalpingBase = "https://api.globalping.io"

type gpMeasurement struct {
	Status  string `json:"status"`
	Results []struct {
		Probe struct {
			City    string `json:"city"`
			Country string `json:"country"`
			Network string `json:"network"`
		} `json:"probe"`
		Result struct {
			Status     string `json:"status"`
			RawOutput  string `json:"rawOutput"`
			RawBody    string `json:"rawBody"`
			StatusCode int    `json:"statusCode"`
			Stats      struct {
				Avg  float64 `json:"avg"`
				Loss float64 `json:"loss"`
			} `json:"stats"`
			Timings json.RawMessage `json:"timings"`
			Answers []struct {
				Value string `json:"value"`
			} `json:"answers"`
		} `json:"result"`
	} `json:"results"`
}

// checkGlobalping measures from the Globalping network instead of this probe:
// the probe only places the order, through its own way to the world.
func checkGlobalping(ctx context.Context, s *Spec, env *Env) Result {
	p := paramsOf(s)
	body := map[string]any{"type": p["subtype"], "limit": 1, "inProgressUpdates": false}
	if p["location"] != "" {
		body["locations"] = []map[string]string{{"magic": p["location"]}}
	}
	switch p["subtype"] {
	case "ping":
		body["target"] = p["target"]
		body["measurementOptions"] = map[string]any{"packets": p.int("ping_count")}
	case "dns":
		body["target"] = p["target"]
		body["measurementOptions"] = map[string]any{"query": map[string]string{"type": p["record_type"]}}
	case "http":
		u, err := url.Parse(p["target"])
		if err != nil {
			return fail("config", errors.New("bad target url"))
		}
		body["target"] = u.Hostname()
		opts := map[string]any{"protocol": strings.ToUpper(u.Scheme),
			"request": map[string]any{"host": u.Hostname(), "path": u.Path, "query": u.RawQuery, "method": p["method"]}}
		if u.Port() != "" {
			port, err := strconv.Atoi(u.Port())
			if err != nil {
				return fail("config", errors.New("bad port in target url"))
			}
			opts["port"] = port // the API takes a number
		}
		body["measurementOptions"] = opts
	}

	client := newHTTPClient(env, false)
	defer client.CloseIdleConnections()
	start := time.Now()
	id, res := gpCreate(ctx, client, p["token"], body)
	if res != nil {
		return *res
	}
	var m gpMeasurement
	for {
		if res := gpFetch(ctx, client, p["token"], id, &m); res != nil {
			return *res
		}
		if m.Status != "in-progress" {
			break
		}
		select {
		case <-ctx.Done():
			return fail("timeout", errors.New("the measurement did not finish in time"))
		case <-time.After(500 * time.Millisecond):
		}
	}
	if len(m.Results) == 0 {
		return fail("bad_response", errors.New("globalping returned no results"))
	}
	rr := m.Results[0].Result
	where := strings.Trim(m.Results[0].Probe.City+", "+m.Results[0].Probe.Country, ", ")
	r := Result{LatencyMS: ms(time.Since(start)), Info: map[string]string{"location": where, "network": m.Results[0].Probe.Network}}
	if rr.Status == "failed" {
		r.Status, r.Reason, r.Error = Down, "remote_failed", where+": "+clip(rr.RawOutput, 200)
		return r
	}
	switch p["subtype"] {
	case "ping":
		if rr.Stats.Loss >= 100 {
			r.Status, r.Reason, r.Error = Down, "timeout", where+": every packet lost"
			return r
		}
		if rr.Stats.Avg > 0 {
			r.LatencyMS = rr.Stats.Avg
		}
		r.Text = rr.RawOutput
	case "http":
		lo, hi, _ := statusRange(p["accepted_status"])
		r.Info["status_code"] = fmt.Sprint(rr.StatusCode)
		if rr.StatusCode < lo || rr.StatusCode > hi {
			r.Status, r.Reason, r.Error = Down, "http_status", fmt.Sprintf("HTTP %d", rr.StatusCode)
			return r
		}
		r.Text = rr.RawBody
		if r.Text == "" {
			r.Text = rr.RawOutput
		}
	case "dns":
		values := make([]string, len(rr.Answers))
		for i, a := range rr.Answers {
			values[i] = a.Value
		}
		if len(values) == 0 {
			r.Status, r.Reason, r.Error = Down, "dns", where+": no records"
			return r
		}
		r.Text = strings.Join(values, "\n")
		r.Info["answers"] = strings.Join(values, ",")
	}
	return r
}

func gpRequest(ctx context.Context, method, path, token string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, globalpingBase+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "UptimeForge/1.0")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req, nil
}

func gpCreate(ctx context.Context, client *http.Client, token string, body any) (string, *Result) {
	raw, _ := json.Marshal(body)
	req, err := gpRequest(ctx, http.MethodPost, "/v1/measurements", token, bytes.NewReader(raw))
	if err != nil {
		r := fail("config", plainErr(err))
		return "", &r
	}
	resp, err := client.Do(req)
	if err != nil {
		err = plainErr(err)
		r := fail(reasonOf(ctx, err), err)
		return "", &r
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		r := fail(reasonOf(ctx, err), err)
		return "", &r
	}
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		r := gpFailure(resp.StatusCode, token != "", data)
		return "", &r
	}
	var out struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(data, &out) != nil || out.ID == "" {
		r := fail("bad_response", errors.New("globalping returned no measurement id"))
		return "", &r
	}
	return out.ID, nil
}

func gpFetch(ctx context.Context, client *http.Client, token, id string, m *gpMeasurement) *Result {
	req, err := gpRequest(ctx, http.MethodGet, "/v1/measurements/"+url.PathEscape(id), token, nil)
	if err != nil {
		r := fail("config", plainErr(err))
		return &r
	}
	resp, err := client.Do(req)
	if err != nil {
		err = plainErr(err)
		r := fail(reasonOf(ctx, err), err)
		return &r
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		r := fail(reasonOf(ctx, err), err)
		return &r
	}
	if resp.StatusCode != http.StatusOK {
		r := gpFailure(resp.StatusCode, token != "", data)
		return &r
	}
	if err := json.Unmarshal(data, m); err != nil {
		r := fail("bad_response", errors.New("globalping answered with something that is not a measurement"))
		return &r
	}
	return nil
}

// gpFailure separates what the API refused from what the target did: a quota
// running out says nothing about the target.
func gpFailure(code int, hasToken bool, data []byte) Result {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(data, &e)
	msg := clip(e.Error.Message, 200)
	switch {
	case code == http.StatusTooManyRequests && hasToken:
		return fail("rate_limited", errors.New("globalping credits are used up for this token"))
	case code == http.StatusTooManyRequests:
		return fail("rate_limited", errors.New("globalping anonymous limit reached, add a token"))
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return fail("auth", fmt.Errorf("globalping refused the token (HTTP %d)", code))
	case code >= 400 && code < 500:
		return fail("config", fmt.Errorf("globalping refused the request: %s", msg))
	}
	return fail("http_status", fmt.Errorf("HTTP %d", code))
}
