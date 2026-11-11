//go:build review

package xray

import (
	"testing"
)

func FuzzReviewParseLink(f *testing.F) {
	for _, s := range []string{
		"vless://b831381d-6324-4d53-ad4f-8cda48b30811@de1.example.com:443?type=ws&security=tls&sni=a&path=/x#de",
		"trojan://pass@h:443?type=grpc&serviceName=x#t",
		"ss://YWVzLTI1Ni1nY206cGFzcw@1.2.3.4:8388#ss",
		"vmess://eyJhZGQiOiJoIiwicG9ydCI6IjQ0MyIsImlkIjoieCJ9",
		"hy2://auth@h:443,2000-3000/?sni=x&obfs=salamander&obfs-password=p#h",
		"socks5://u:p@h:1080", "https://u:p@h:8443",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		ParseLink(s)
	})
}
