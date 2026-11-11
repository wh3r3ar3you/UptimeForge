package xray

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseLinkVLESSReality(t *testing.T) {
	l, err := ParseLink("vless://6b347655-eed3-42a8-9345-83ed50df2484@de1.example.com:443?type=tcp&security=reality&pbk=ncDrKG0PBim2ueW_moKKMcYliasKNCrZqmYjvu_6gVE&fp=chrome&sni=www.microsoft.com&sid=7db0f07cf92cf698&spx=%2F&flow=xtls-rprx-vision#de-node-1%20%F0%9F%87%A9%F0%9F%87%AA")
	if err != nil {
		t.Fatal(err)
	}
	if l.Name != "de-node-1 🇩🇪" || l.Endpoint() != "de1.example.com:443" || l.Protocol != "vless" {
		t.Fatalf("unexpected link: %+v", l)
	}
	ss := l.Outbound["streamSettings"].(map[string]any)
	r := ss["realitySettings"].(map[string]any)
	if ss["security"] != "reality" || r["publicKey"] == "" || r["serverName"] != "www.microsoft.com" || r["shortId"] != "7db0f07cf92cf698" {
		t.Fatalf("bad reality settings: %s", mustJSON(t, ss))
	}
	if l.Outbound["settings"].(map[string]any)["flow"] != "xtls-rprx-vision" {
		t.Fatal("flow lost")
	}
	// The outbound must be accepted by xray-core itself.
	if _, err := BuildConfig(l.Outbound, nil, ""); err != nil {
		t.Fatal(err)
	}
}

func TestParseLinkTransports(t *testing.T) {
	cases := map[string]string{
		"xhttp":       `vless://id@h.example:443?type=xhttp&security=tls&sni=cdn.example&path=%2Fx&mode=packet-up&extra=%7B%22xPaddingBytes%22%3A%22100-1000%22%7D#xh`,
		"ws":          `vless://id@h.example:443?type=ws&security=tls&path=%2Fws&host=cdn.example#ws`,
		"grpc":        `trojan://pass@h.example:443?type=grpc&serviceName=svc&mode=multi&sni=h.example#grpc`,
		"httpupgrade": `vless://id@h.example:80?type=httpupgrade&path=%2Fup&host=h.example#hu`,
	}
	for net, link := range cases {
		l, err := ParseLink(link)
		if err != nil {
			t.Fatalf("%s: %v", net, err)
		}
		ss := l.Outbound["streamSettings"].(map[string]any)
		if ss["network"] != net {
			t.Fatalf("%s: network=%v", net, ss["network"])
		}
	}
	l, _ := ParseLink(cases["xhttp"])
	x := l.Outbound["streamSettings"].(map[string]any)["xhttpSettings"].(map[string]any)
	if x["mode"] != "packet-up" || x["extra"].(map[string]any)["xPaddingBytes"] != "100-1000" {
		t.Fatalf("xhttp settings: %s", mustJSON(t, x))
	}
	if _, err := ParseLink(`vless://id@h.example:443?type=h2#old`); err == nil {
		t.Fatal("h2 transport must be rejected")
	}
}

func TestParseLinkShadowsocks(t *testing.T) {
	userinfo := base64.RawURLEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:secret"))
	legacy := base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:pw@1.2.3.4:8388"))
	for _, link := range []string{
		"ss://" + userinfo + "@1.2.3.4:8388#sip002",
		"ss://2022-blake3-aes-128-gcm:a2V5%3D@1.2.3.4:8388#plain",
		"ss://" + legacy + "#legacy",
	} {
		l, err := ParseLink(link)
		if err != nil {
			t.Fatalf("%s: %v", link, err)
		}
		s := l.Outbound["settings"].(map[string]any)
		if s["method"] == "" || s["password"] == "" || l.Port != 8388 {
			t.Fatalf("%s: %s", link, mustJSON(t, s))
		}
	}
}

func TestParseLinkVMess(t *testing.T) {
	js := `{"v":"2","ps":"vm","add":"v.example","port":"443","id":"b831381d-6324-4d53-ad4f-8cda48b30811","aid":"0","scy":"auto","net":"ws","type":"none","host":"v.example","path":"/vm","tls":"tls","sni":"v.example"}`
	l, err := ParseLink("vmess://" + base64.StdEncoding.EncodeToString([]byte(js)))
	if err != nil {
		t.Fatal(err)
	}
	ss := l.Outbound["streamSettings"].(map[string]any)
	if l.Name != "vm" || ss["network"] != "ws" || ss["security"] != "tls" {
		t.Fatalf("vmess: %s", mustJSON(t, l.Outbound))
	}
}

func TestParseLinkHysteria2(t *testing.T) {
	l, err := ParseLink("hy2://user:pw@hy.example:443,20000-30000/?sni=hy.example&obfs=salamander&obfs-password=xyz&insecure=1#hy")
	if err != nil {
		t.Fatal(err)
	}
	ss := l.Outbound["streamSettings"].(map[string]any)
	if l.Port != 443 || ss["network"] != "hysteria" || ss["hysteriaSettings"].(map[string]any)["auth"] != "user:pw" || ss["finalmask"] == nil {
		t.Fatalf("hy2: %s", mustJSON(t, l.Outbound))
	}
	if _, err := BuildConfig(l.Outbound, nil, ""); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeSubscription(t *testing.T) {
	links := "vless://id@a.example:443?security=tls#A\ntrojan://p@b.example:443#B\nbogus://x\n"
	got, err := DecodeSubscription([]byte(base64.StdEncoding.EncodeToString([]byte(links))))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "A" || got[1].Endpoint != "b.example:443" {
		t.Fatalf("got %+v", got)
	}

	js := `[{"remarks":"🇩🇪 Germany","outbounds":[{"tag":"proxy","protocol":"vless","settings":{"vnext":[{"address":"de.example","port":443,"users":[{"id":"x","encryption":"none"}]}]}},{"protocol":"freedom","tag":"direct"}]}]`
	got, err = DecodeSubscription([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "🇩🇪 Germany" || got[0].Endpoint != "de.example:443" || got[0].Outbound["tag"] != nil {
		t.Fatalf("got %+v", got)
	}
}

func TestBuildConfigChainsThroughVia(t *testing.T) {
	target, _ := ParseLink("vless://id@a.example:443?security=tls#A")
	via, _ := ParseLink("socks5://u:p@10.0.0.1:1080")
	b, err := BuildConfig(target.Outbound, via.Outbound, "")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	json.Unmarshal(b, &cfg)
	so := cfg.Outbounds[0]["streamSettings"].(map[string]any)["sockopt"].(map[string]any)
	if so["dialerProxy"] != "via" || cfg.Outbounds[1]["tag"] != "via" {
		t.Fatalf("chain not wired: %s", b)
	}
	if _, ok := target.Outbound["tag"]; ok {
		t.Fatal("BuildConfig mutated its input")
	}
}

// An empty port used to panic in ParseLink and, through a subscription,
// kill the hub. Such a link is an error; the good lines around it survive.
func TestEmptyPortIsAnError(t *testing.T) {
	for _, l := range []string{
		"vless://b831381d-6324-4d53-ad4f-8cda48b30811@de1.example.com:?type=tcp#de",
		"trojan://pass@de1.example.com:#de",
		"hy2://pass@de1.example.com:/?sni=x#de",
	} {
		if _, err := ParseLink(l); err == nil {
			t.Errorf("ParseLink(%q): expected an error", l)
		}
	}
	sub := base64.StdEncoding.EncodeToString([]byte("vless://b831381d-6324-4d53-ad4f-8cda48b30811@ok.example.com:443?type=tcp#ok\n" +
		"vless://b831381d-6324-4d53-ad4f-8cda48b30811@bad.example.com:?type=tcp#bad\n"))
	got, err := DecodeSubscription([]byte(sub))
	if err != nil || len(got) != 1 || got[0].Name != "ok" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestFetchSubscriptionErrorHidesURL(t *testing.T) {
	_, err := FetchSubscription(context.Background(), http.DefaultClient, "http://127.0.0.1:1/sub-token-123", "")
	if err == nil || strings.Contains(err.Error(), "sub-token-123") {
		t.Fatalf("got %v", err)
	}
}
