package checks

import "testing"

func TestURLCarriesSecret(t *testing.T) {
	for in, want := range map[string]bool{
		"https://sub.example.com/p_q7Xk2LmN9vRt4Z":                true,
		"https://x.example.com/?token=abc":                        true,
		"https://u:p@host/":                                       true,
		"https://example.com/health":                              false,
		"https://example.com/health?x=1":                          false,
		"https://app.example.com/api/v1/branding/telegram-widget": false,
		"https://api.example.com/hook?room=Ab12Cd34Ef56Gh":        true,
		"https://sub.example.com/sub/0f8e6c1d2b3a4f5e/vless":      true,
		"https://example.com/health#frag-0f8e6c1d2b3a4f5e":        false,
		"":            false,
		"example.com": false,
		// encoded, padded or dotted tokens are tokens too
		"https://x.example.com/?t=dGhpc2lzYXNlY3JldDEyMw==":                                                                         true,
		"https://x.example.com/?%74oken=abc":                                                                                        true,
		"https://x.example.com/?q=abc%2D123%2Dxyz%2D789":                                                                            true,
		"https://x.example.com/p%5Fq7Xk2LmN9vRt4Z":                                                                                  true,
		"https://api.telegram.org/bot123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw/getMe":                                            true,
		"https://x.example.com/cb?jwt=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U": true,
		// what cannot be decoded is assumed to hide one
		"https://x.example.com/?q=100%zz": true,
		// versioned file names and plain words are not tokens
		"https://cdn.example.com/lib/jquery-3.7.1.min.js":      false,
		"https://example.com/search?q=hello%20world":           false,
		"https://example.com/report?from=2026-11-14T10:00:00Z": false,
	} {
		if got := URLCarriesSecret(in); got != want {
			t.Errorf("URLCarriesSecret(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestCarriesSecretsURL(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
		want bool
	}{
		{"http url path token", Spec{Type: "http", HTTP: &HTTPParams{URL: "https://sub.example.com/p_q7Xk2LmN9vRt4Z"}}, true},
		{"http url query token", Spec{Type: "http", HTTP: &HTTPParams{URL: "https://x.example.com/?token=abc"}}, true},
		{"http url userinfo", Spec{Type: "http", HTTP: &HTTPParams{URL: "https://u:p@host/"}}, true},
		{"http url plain", Spec{Type: "http", HTTP: &HTTPParams{URL: "https://example.com/health"}}, false},
		{"browser url param token", Spec{Type: "real-browser", Params: map[string]string{"url": "https://sub.example.com/p_q7Xk2LmN9vRt4Z"}}, true},
		{"browser url param plain", Spec{Type: "real-browser", Params: map[string]string{"url": "https://example.com/health"}}, false},
		{"url param second line", Spec{Type: "real-browser", Params: map[string]string{"url": "https://example.com/health\nhttps://x.example.com/?token=abc"}}, true},
	}
	for _, c := range cases {
		if _, ok := TypeOf(c.spec.Type); !ok {
			t.Fatalf("%s: unknown type %q", c.name, c.spec.Type)
		}
		if got := CarriesSecrets(&c.spec); got != c.want {
			t.Errorf("%s: CarriesSecrets = %v, want %v", c.name, got, c.want)
		}
	}
}
