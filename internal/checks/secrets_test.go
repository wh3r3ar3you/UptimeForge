package checks

import "testing"

func TestCarriesSecrets(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
		want bool
	}{
		{"xray link", Spec{Type: "xray", Xray: &XrayParams{Link: "vless://u@h:443"}}, true},
		{"xray subscription", Spec{Type: "xray", Xray: &XrayParams{Subscription: "https://sub.example/x"}}, true},
		{"xray outbound", Spec{Type: "xray", Xray: &XrayParams{Outbound: map[string]any{"protocol": "vless"}}}, true},
		{"xray empty", Spec{Type: "xray", Xray: &XrayParams{URL: "https://example.com"}}, false},
		{"mtproto link", Spec{Type: "mtproto", MTProto: &MTProtoParams{Link: "tg://proxy?server=h&port=1&secret=aa"}}, true},
		{"mtproto secret", Spec{Type: "mtproto", MTProto: &MTProtoParams{Address: "h:443", Secret: "aa"}}, true},
		{"mtproto address only", Spec{Type: "mtproto", MTProto: &MTProtoParams{Address: "h:443"}}, false},
		{"http plain", Spec{Type: "http", HTTP: &HTTPParams{URL: "https://example.com", Method: "GET"}}, false},
		{"http basic password", Spec{Type: "http", HTTP: &HTTPParams{URL: "https://example.com", BasicPassword: "p"}}, true},
		{"http bearer", Spec{Type: "http", HTTP: &HTTPParams{URL: "https://example.com", BearerToken: "t"}}, true},
		{"http headers", Spec{Type: "http", HTTP: &HTTPParams{URL: "https://example.com", Headers: map[string]string{"Authorization": "x"}}}, true},
		{"http param basic_password", Spec{Type: "http", Params: map[string]string{"basic_password": "p"}}, true},
		{"http param plain", Spec{Type: "http", Params: map[string]string{"url": "https://example.com"}}, false},
		{"tcp", Spec{Type: "tcp", TCP: &TCPParams{Address: "h:1"}}, false},
		{"ping", Spec{Type: "ping", Params: map[string]string{"host": "h"}}, false},
		{"dns", Spec{Type: "dns", DNS: &DNSParams{Name: "example.com"}}, false},
		{"tls", Spec{Type: "tls", TLS: &TLSParams{Address: "h:443"}}, false},
		{"catalog secret set", Spec{Type: "radius", Params: map[string]string{"secret": "s"}}, true},
		{"catalog secret empty", Spec{Type: "radius", Params: map[string]string{"secret": "", "host": "h"}}, false},
		{"unknown type with params", Spec{Type: "nope", Params: map[string]string{"x": "y"}}, true},
		{"unknown type empty", Spec{Type: "nope"}, false},
	}
	for _, c := range cases {
		if got := CarriesSecrets(&c.spec); got != c.want {
			t.Errorf("%s: CarriesSecrets = %v, want %v", c.name, got, c.want)
		}
	}
}
