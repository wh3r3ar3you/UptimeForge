package hub

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadConfigEnv(t *testing.T) {
	t.Setenv("UF_TEST_TOKEN", `p4ss: "with" #specials and spaces`)
	t.Setenv("UF_TEST_CHAT", "-1001234567890")
	p := writeConfig(t, `
# ${NOT_SET_BUT_ONLY_IN_A_COMMENT}
probes:
  - id: home
    token: ${UF_TEST_TOKEN} # ${ALSO_A_COMMENT}
    labels: {country: ru}
checks:
  - id: api
    type: http
    http: {url: "https://example.com/"}
telegram:
  token: "1:x"
  chat_id: ${UF_TEST_CHAT}
alerts:
  quiet_hours: "23:00-08:00"
`)
	c, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Probes[0].Token != `p4ss: "with" #specials and spaces` || c.Probes[0].TokenHash != HashToken(c.Probes[0].Token) {
		t.Fatalf("token mangled: %q", c.Probes[0].Token)
	}
	if c.Telegram.ChatID != -1001234567890 {
		t.Fatalf("chat_id = %d", c.Telegram.ChatID)
	}
	if c.Probes[0].Labels["id"] != "home" || c.Defaults.Retries != 2 || c.Alerts.RemindEvery.D() != 30*time.Minute {
		t.Fatal("defaults not applied")
	}
}

func TestLoadConfigErrors(t *testing.T) {
	cases := map[string]string{
		"unset environment": `probes: [{id: a, token: "${UF_DEFINITELY_UNSET}"}]`,
		"chekcs not found":  "probes: [{id: a, local: true}]\nchekcs: []",
		"exactly one of":    "probes: [{id: a, local: true, token: aaaaaaaaaaaaaaaaaaaa}]",
		"unknown type":      "probes: [{id: a, local: true}]\nchecks: [{id: c, type: nosuchtype}]",
		"missing tcp":       "probes: [{id: a, local: true}]\nchecks: [{id: c, type: tcp}]",
		"selector":          "probes: [{id: a, local: true}]\nchecks: [{id: c, type: tcp, tcp: {address: 'x:1'}, probes: [ru]}]",
		"at least 16":       "probes: [{id: a, token: short}]",
	}
	for want, body := range cases {
		_, err := LoadConfig(writeConfig(t, body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", want, err)
		}
	}
}

func TestQuietHours(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 1, 1, h, m, 0, 0, time.UTC) }
	if !inQuietHours("23:00-08:00", at(2, 0)) || inQuietHours("23:00-08:00", at(12, 0)) || !inQuietHours("01:00-08:00", at(1, 0)) {
		t.Fatal("quiet hours window is wrong")
	}
}

func TestNormalizeTags(t *testing.T) {
	got, err := normalizeTags([]string{"DE", " de", "VLESS"})
	if err != nil || !slices.Equal(got, []string{"de", "vless"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := normalizeTags([]string{"Германия"}); err != nil || !slices.Equal(got, []string{"германия"}) {
		t.Fatalf("cyrillic: %v, %v", got, err)
	}
	if _, err := normalizeTags([]string{"a b"}); err == nil || !strings.Contains(err.Error(), `tag "a b"`) {
		t.Fatalf("space: %v", err)
	}
	many := make([]string, 17)
	for i := range many {
		many[i] = fmt.Sprintf("t%d", i)
	}
	if _, err := normalizeTags(many); err == nil || !strings.Contains(err.Error(), "at most 16 tags") {
		t.Fatalf("17 tags: %v", err)
	}
	if _, err := normalizeTags([]string{strings.Repeat("a", 33)}); err == nil {
		t.Fatal("33 characters accepted")
	}
	if got, err := normalizeTags(nil); err != nil || got != nil {
		t.Fatalf("no tags: %v %v", got, err)
	}
}

func TestTelegramRuleValidation(t *testing.T) {
	c := &Config{DataDir: t.TempDir(), Telegram: &Telegram{Token: "1:x", ChatID: -1,
		Rules: []TelegramRule{{Tags: []string{"DE"}, MinSeverity: "loud"}}}}
	if err := c.normalize(); err == nil || !strings.Contains(err.Error(), "min_severity") {
		t.Fatalf("bad severity accepted: %v", err)
	}
	c.Telegram.Rules[0].MinSeverity = "critical"
	if err := c.normalize(); err != nil || !slices.Equal(c.Telegram.Rules[0].Tags, []string{"de"}) {
		t.Fatalf("rule tags normalize: %v %v", err, c.Telegram.Rules[0].Tags)
	}
}
