package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
	"github.com/wh3r3ar3you/uptimeforge/internal/store/pgtest"
)

// A push URL is accepted only once its check is saved: a save that fails
// must not leave the token answering 200 on the open /api/push endpoint.
// Short tokens, guessable on that endpoint, are refused.
func TestPushTokenOnlyAfterSave(t *testing.T) {
	a, ctx := secretsApp(t)
	const tok = "job-token-failed-save-01"
	err := a.SaveCheck(ctx, hub.CheckConfig{Spec: checks.Spec{ID: "cron", Type: "push", Params: map[string]string{"token": tok}},
		DependsOn: []string{"no-such-check"}}, true)
	if err == nil {
		t.Fatal("a check with an unknown parent must not be saved")
	}
	if checks.Beat(tok, true, "", 0) {
		t.Fatal("the token of a check that was not saved is accepted")
	}
	err = a.SaveCheck(ctx, hub.CheckConfig{Spec: checks.Spec{ID: "cron", Type: "push", Params: map[string]string{"token": "abc"}}}, true)
	if err == nil || !isValidation(err) {
		t.Fatalf("a short push token must be refused: %v", err)
	}
	if err := a.SaveCheck(ctx, hub.CheckConfig{Spec: checks.Spec{ID: "cron", Type: "push", Params: map[string]string{"token": tok}}}, true); err != nil {
		t.Fatal(err)
	}
	defer checks.ForgetPush(tok)
	if !checks.Beat(tok, true, "", 0) {
		t.Fatal("the token of a saved check is refused")
	}
}

// sink is a server that records every request: the attacker's host.
type sink struct {
	*httptest.Server
	mu   sync.Mutex
	seen []string
}

func newSink(t *testing.T) *sink {
	s := &sink{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.seen = append(s.seen, r.URL.String()+" "+r.Header.Get("Authorization")+" "+r.Header.Get("X-Api-Key"))
		s.mu.Unlock()
		http.Error(w, "nope", http.StatusTeapot)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *sink) leaked(secret string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.seen {
		if strings.Contains(r, secret) {
			return true
		}
	}
	return false
}

func isValidation(err error) bool {
	var ve *ValidationError
	return errors.As(err, &ve)
}

func secretsApp(t *testing.T) (*App, context.Context) {
	a := newApp(t, Options{DatabaseURL: pgtest.URL(t), DataDir: t.TempDir()})
	t.Cleanup(func() { a.Store.Close() })
	ctx := context.Background()
	err := a.SaveSettings(ctx, hub.Config{
		Telegram:  &hub.Telegram{Token: "1:tg-secret", ChatID: -100, APIURL: "https://tg.example.com", Proxy: "socks5://puser:ppass@proxy.example.com:1080"},
		Remnawave: &hub.Remnawave{URL: "https://panel.example.com", Token: "rw-secret", Headers: map[string]string{"X-Api-Key": "hdr-secret"}},
		Webhook:   &hub.Webhook{URL: "https://hooks.example.com/services/T0/B0/wh-secret?key=q-secret", Headers: map[string]string{"Authorization": "Bearer wh-auth"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return a, ctx
}

// The connection test with a masked token must not send the saved token to a
// URL the form changed: that hands it to whoever runs the new host.
func TestTestRemnawaveKeepsTokenOnItsHost(t *testing.T) {
	a, ctx := secretsApp(t)
	evil := newSink(t)
	_, err := a.TestRemnawave(ctx, hub.Remnawave{URL: evil.URL, Token: masked})
	if err == nil || !strings.Contains(err.Error(), "enter it again") {
		t.Fatalf("a masked token with a new URL must be refused: %v", err)
	}
	_, err = a.TestRemnawave(ctx, hub.Remnawave{URL: evil.URL, Token: "", Headers: map[string]string{"X-Api-Key": masked}})
	if err == nil {
		t.Fatal("an empty token with a new URL must be refused")
	}
	_, err = a.TestRemnawave(ctx, hub.Remnawave{URL: evil.URL, Token: "typed-again", Headers: map[string]string{"X-Api-Key": masked}})
	if err == nil || !strings.Contains(err.Error(), "enter it again") {
		t.Fatalf("a masked header with a new URL must be refused: %v", err)
	}
	if evil.leaked("rw-secret") || evil.leaked("hdr-secret") {
		t.Fatalf("saved secrets reached the new host: %v", evil.seen)
	}
}

// Finding chats with a masked token must not send it to another API server or
// through another proxy.
func TestFindTelegramChatsKeepsTokenOnItsHost(t *testing.T) {
	a, ctx := secretsApp(t)
	evil := newSink(t)
	if _, _, err := a.FindTelegramChats(ctx, masked, evil.URL, "socks5://puser:ppass@proxy.example.com:1080"); err == nil || !strings.Contains(err.Error(), "enter it again") {
		t.Fatalf("a masked token with a new API URL must be refused: %v", err)
	}
	if _, _, err := a.FindTelegramChats(ctx, masked, "https://tg.example.com", "http://evil.example.com:8080"); err == nil || !strings.Contains(err.Error(), "enter it again") {
		t.Fatalf("a masked token with a new proxy must be refused: %v", err)
	}
	if evil.leaked("tg-secret") {
		t.Fatalf("the bot token reached the new host: %v", evil.seen)
	}
}

// Saving settings with a masked secret and a new destination would make the
// hub send the saved secret there from then on.
func TestSaveSettingsKeepsSecretsOnTheirHosts(t *testing.T) {
	a, ctx := secretsApp(t)
	cases := map[string]func(c *hub.Config){
		"remnawave url":   func(c *hub.Config) { c.Remnawave.URL = "https://evil.example.com" },
		"telegram apiurl": func(c *hub.Config) { c.Telegram.APIURL = "https://evil.example.com" },
		"telegram proxy":  func(c *hub.Config) { c.Telegram.Proxy = "http://evil.example.com:3128" },
		"webhook url":     func(c *hub.Config) { c.Webhook.URL = "https://evil.example.com/hook" },
	}
	for name, change := range cases {
		cfg := a.Settings().Config
		change(&cfg)
		if err := a.SaveSettings(ctx, cfg); err == nil || !isValidation(err) || !strings.Contains(err.Error(), "enter it again") {
			t.Errorf("%s: a masked secret with a new destination must be refused: %v", name, err)
		}
	}
	got := a.Hub.Config()
	if got.Remnawave.URL != "https://panel.example.com" || got.Telegram.APIURL != "https://tg.example.com" || got.Webhook.URL != "https://hooks.example.com/services/T0/B0/wh-secret?key=q-secret" {
		t.Fatalf("a refused save changed the settings: %+v %+v %+v", got.Remnawave, got.Telegram, got.Webhook)
	}

	// Retyping the secret with the new destination is fine.
	cfg := a.Settings().Config
	cfg.Remnawave.URL, cfg.Remnawave.Token, cfg.Remnawave.Headers = "https://panel2.example.com", "rw-new", map[string]string{"X-Api-Key": "hdr-new"}
	if err := a.SaveSettings(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if rw := a.Hub.Config().Remnawave; rw.Token != "rw-new" || rw.Headers["X-Api-Key"] != "hdr-new" {
		t.Fatalf("%+v", rw)
	}
}

// Settings hide proxy logins, Remnawave header values and the webhook URL's
// path and query; saving what was shown keeps the real values.
func TestSettingsMaskEverySecret(t *testing.T) {
	a, ctx := secretsApp(t)
	st := a.Settings().Config
	for name, v := range map[string]string{
		"telegram proxy":   st.Telegram.Proxy,
		"remnawave header": st.Remnawave.Headers["X-Api-Key"],
		"webhook url":      st.Webhook.URL,
	} {
		for _, secret := range []string{"ppass", "puser", "hdr-secret", "wh-secret", "q-secret"} {
			if strings.Contains(v, secret) {
				t.Errorf("%s shows %q: %s", name, secret, v)
			}
		}
	}
	if !strings.Contains(st.Telegram.Proxy, "proxy.example.com:1080") || !strings.HasPrefix(st.Webhook.URL, "https://hooks.example.com/") {
		t.Fatalf("hosts must stay visible: %q %q", st.Telegram.Proxy, st.Webhook.URL)
	}
	if err := a.SaveSettings(ctx, st); err != nil {
		t.Fatalf("saving the masked form unchanged: %v", err)
	}
	got := a.Hub.Config()
	if got.Telegram.Proxy != "socks5://puser:ppass@proxy.example.com:1080" || got.Telegram.Token != "1:tg-secret" ||
		got.Remnawave.Headers["X-Api-Key"] != "hdr-secret" || got.Remnawave.Token != "rw-secret" ||
		got.Webhook.URL != "https://hooks.example.com/services/T0/B0/wh-secret?key=q-secret" || got.Webhook.Headers["Authorization"] != "Bearer wh-auth" {
		t.Fatalf("real values lost: %+v %+v %+v", got.Telegram, got.Remnawave, got.Webhook)
	}
}

// The webhook signing secret is masked like any other and kept while the
// webhook URL stays the same.
func TestWebhookSecretMaskedAndKept(t *testing.T) {
	a := newApp(t, Options{DatabaseURL: pgtest.URL(t), DataDir: t.TempDir()})
	defer a.Store.Close()
	ctx := context.Background()
	if err := a.SaveSettings(ctx, hub.Config{Webhook: &hub.Webhook{URL: "https://hooks.example.com/x", Secret: "s3cret-signing-key"}}); err != nil {
		t.Fatal(err)
	}
	shown := a.Settings().Config
	if shown.Webhook == nil || shown.Webhook.Secret != masked {
		t.Fatalf("shown: %+v", shown.Webhook)
	}
	if err := a.SaveSettings(ctx, shown); err != nil {
		t.Fatal(err)
	}
	if got := a.Hub.Config().Webhook.Secret; got != "s3cret-signing-key" {
		t.Fatalf("kept: %q", got)
	}
	moved := a.Settings().Config
	moved.Webhook.URL = "https://evil.example.net/x"
	if err := a.SaveSettings(ctx, moved); err == nil {
		t.Fatal("a masked secret must not follow the webhook to a new address")
	}
}
