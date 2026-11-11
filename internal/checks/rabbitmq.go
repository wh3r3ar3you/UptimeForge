package checks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func init() {
	registerType(TypeInfo{
		Type: "rabbitmq", Category: "messaging",
		Fields: []Field{
			{Name: "nodes", Kind: "textarea", Required: true},
			required(fUsername), required(fPassword), fInsecure,
		},
		target: func(p map[string]string) string {
			nodes := params(p).lines("nodes")
			if len(nodes) == 0 {
				return ""
			}
			return targetURL("n")(map[string]string{"n": nodes[0]})
		},
	}, checkRabbitMQ)
}

// checkRabbitMQ asks the management API of each node about cluster alarms, as
// Kuma does. One healthy answer is enough: a node that does not answer while
// another reports no alarms is a question for that node's own check.
func checkRabbitMQ(ctx context.Context, s *Spec, env *Env) Result {
	p := paramsOf(s)
	client := newHTTPClient(env, p.bool("insecure"))
	defer client.CloseIdleConnections()
	nodes := p.lines("nodes")
	start := time.Now()
	var last Result
	var failures []string
	for _, base := range nodes {
		r := rabbitNode(ctx, client, p, base)
		if r.Status == "" {
			r.LatencyMS = ms(time.Since(start))
			return r
		}
		last = r
		failures = append(failures, r.Error)
	}
	if len(failures) > 1 {
		last.Error = strings.Join(failures, "; ")
	}
	return last
}

func rabbitNode(ctx context.Context, client *http.Client, p params, base string) Result {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/api/health/checks/alarms/", nil)
	if err != nil {
		return fail("config", plainErr(err))
	}
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(p["username"], p["password"])
	resp, err := client.Do(req)
	if err != nil {
		err = plainErr(err)
		return fail(reasonOf(ctx, err), err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fail(reasonOf(ctx, err), err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return Result{}
	case http.StatusServiceUnavailable:
		var out struct {
			Reason string `json:"reason"`
		}
		json.Unmarshal(body, &out)
		if out.Reason == "" {
			out.Reason = "the node reports an alarm"
		}
		return fail("unhealthy", errors.New(clip(out.Reason, 200)))
	case http.StatusUnauthorized, http.StatusForbidden:
		return fail("auth", fmt.Errorf("management API refused these credentials (HTTP %d)", resp.StatusCode))
	}
	return fail("http_status", fmt.Errorf("HTTP %d", resp.StatusCode))
}
