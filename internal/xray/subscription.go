package xray

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
)

// Entry is one proxy config taken from a subscription.
type Entry struct {
	Name     string
	Endpoint string
	Outbound Outbound
}

// withoutURL strips the URL Go puts into its errors: a subscription URL and a
// share link are secrets, and this text reaches alerts and viewers.
func withoutURL(err error) error {
	var ue *neturl.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// FetchSubscription downloads a subscription and decodes every config in it.
// Panels (Remnawave, Marzban, 3x-ui) pick the response format by User-Agent,
// so it is configurable.
func FetchSubscription(ctx context.Context, client *http.Client, url, userAgent string) ([]Entry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, withoutURL(err)
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, withoutURL(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("subscription: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return DecodeSubscription(body)
}

// DecodeSubscription accepts a base64 or plain list of share links, or Xray
// JSON (a single config or an array of configs, as served for Xray clients).
func DecodeSubscription(body []byte) ([]Entry, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, fmt.Errorf("subscription is empty")
	}
	if body[0] == '[' || body[0] == '{' {
		return decodeXrayJSON(body)
	}
	text := string(body)
	if !strings.Contains(text, "://") {
		dec, err := decodeB64(strings.Join(strings.Fields(text), ""))
		if err != nil {
			return nil, fmt.Errorf("subscription: neither links, base64 nor JSON")
		}
		text = string(dec)
	}
	var out []Entry
	var errs []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		l, err := ParseLink(line)
		if err != nil {
			errs = append(errs, withoutURL(err).Error())
			continue
		}
		out = append(out, Entry{Name: l.Name, Endpoint: l.Endpoint(), Outbound: l.Outbound})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("subscription has no usable configs: %s", strings.Join(errs, "; "))
	}
	return out, nil
}

var nonProxyProtocols = map[string]bool{"freedom": true, "blackhole": true, "dns": true, "loopback": true}

func decodeXrayJSON(body []byte) ([]Entry, error) {
	var configs []map[string]any
	if body[0] == '{' {
		var one map[string]any
		if err := json.Unmarshal(body, &one); err != nil {
			return nil, err
		}
		configs = append(configs, one)
	} else if err := json.Unmarshal(body, &configs); err != nil {
		return nil, err
	}
	var out []Entry
	for i, c := range configs {
		obs, _ := c["outbounds"].([]any)
		for _, o := range obs {
			ob, _ := o.(map[string]any)
			proto, _ := ob["protocol"].(string)
			if ob == nil || nonProxyProtocols[proto] {
				continue
			}
			name, _ := c["remarks"].(string)
			if name == "" {
				name = fmt.Sprintf("config-%d", i+1)
			}
			delete(ob, "tag")
			delete(ob, "mux")
			out = append(out, Entry{Name: name, Endpoint: outboundEndpoint(ob), Outbound: Outbound(ob)})
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("subscription JSON has no proxy outbounds")
	}
	return out, nil
}

// outboundEndpoint extracts host:port from flat or vnext/servers settings.
func outboundEndpoint(ob map[string]any) string {
	s, _ := ob["settings"].(map[string]any)
	if s == nil {
		return ""
	}
	pick := func(m map[string]any) string {
		addr, _ := m["address"].(string)
		port, _ := m["port"].(float64)
		if addr == "" {
			return ""
		}
		return fmt.Sprintf("%s:%d", addr, int(port))
	}
	if e := pick(s); e != "" {
		return e
	}
	for _, k := range []string{"vnext", "servers"} {
		if list, _ := s[k].([]any); len(list) > 0 {
			if m, _ := list[0].(map[string]any); m != nil {
				return pick(m)
			}
		}
	}
	return ""
}
