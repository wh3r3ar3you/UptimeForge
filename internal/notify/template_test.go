package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/model"
)

func TestRenderEscapes(t *testing.T) {
	got, err := Render("<p>{name}</p>", Vars{"name": Text("a<b & c>")})
	if err != nil || got != "<p>a&lt;b &amp; c&gt;</p>" {
		t.Fatalf("%q %v", got, err)
	}
	got, _ = Render("{probes}", Vars{"probes": HTML("<b>x</b>")})
	if got != "<b>x</b>" {
		t.Fatalf("a fragment the code built stays as is: %q", got)
	}
}

func TestRenderConditional(t *testing.T) {
	tpl := "A{?users} 👥 {users}{/users}B"
	if got, _ := Render(tpl, Vars{"users": Text("")}); got != "AB" {
		t.Fatalf("empty: %q", got)
	}
	if got, _ := Render(tpl, Vars{"users": Text("12")}); got != "A 👥 12B" {
		t.Fatalf("set: %q", got)
	}
	if got, _ := Render("{{literal}}", Vars{}); got != "{literal}" {
		t.Fatalf("braces: %q", got)
	}
}

func TestRenderUnknownPlaceholder(t *testing.T) {
	if _, err := Render("{nope}", Vars{}); err == nil || !strings.Contains(err.Error(), "unknown placeholder {nope}") {
		t.Fatalf("%v", err)
	}
	if _, err := Render("{?name}x", Vars{"name": Text("y")}); err == nil || !strings.Contains(err.Error(), "{/name}") {
		t.Fatalf("unclosed block: %v", err)
	}
}

func TestRichToHTML(t *testing.T) {
	rich := `<h3>X &amp; Y</h3><p>line</p><table bordered striped compact><tr><th>Проба</th><th>Состояние</th></tr><tr><td>a</td><td>b</td></tr></table>` +
		`<details><summary>2 of 9</summary><table><tr><td>c</td><td>d</td></tr></table></details><blockquote>💡 why</blockquote><footer>⏱ since</footer>`
	got := RichToHTML(rich)
	for _, want := range []string{"<b>X &amp; Y</b>", "line", "a — b", "<blockquote expandable>2 of 9", "c — d", "<blockquote>💡 why</blockquote>", "<i>⏱ since</i>"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if err := ValidateHTML(got); err != nil {
		t.Fatalf("not valid Telegram HTML: %v\n%s", err, got)
	}
}

func webDown() *model.Incident {
	return &model.Incident{Key: "web", Name: "web", Kind: model.KindDown, Worst: model.KindDown, Severity: model.Critical, Started: time.Now()}
}

// A user's card template is what goes out, rich and as HTML alike.
func TestCustomTemplateUsed(t *testing.T) {
	f := &Formatter{Lang: "ru", Loc: time.UTC, Custom: Templates{"ru": {KeyCard: "<p>{emoji} {name}</p>"}}}
	if got := f.RichIncident(webDown(), time.Now()); got != "<p>🔴 web</p>" {
		t.Fatalf("rich: %q", got)
	}
	if got := f.Incident(webDown(), time.Now()); got != "🔴 web" {
		t.Fatalf("html: %q", got)
	}
}

// A template for one kind wins over the general one for that kind only.
func TestPerKindTemplate(t *testing.T) {
	f := &Formatter{Lang: "en", Loc: time.UTC, Custom: Templates{"en": {"card.host": "<p>host {name}</p>"}}}
	host := &model.Incident{Key: "host:p:cpu", Name: "srv", Type: "host", Kind: model.KindHost, Host: &model.HostValue{Metric: "cpu"}, Started: time.Now()}
	if got := f.RichIncident(host, time.Now()); got != "<p>host srv</p>" {
		t.Fatalf("host: %q", got)
	}
	plain := &Formatter{Lang: "en", Loc: time.UTC}
	if f.RichIncident(webDown(), time.Now()) != plain.RichIncident(webDown(), time.Now()) {
		t.Fatal("other kinds keep the default")
	}
}

// A template that cannot render costs nothing: the default goes out and
// the failure is reported.
func TestRuntimeRenderFailureFallsBack(t *testing.T) {
	var failed []error
	f := &Formatter{Lang: "ru", Loc: time.UTC, Custom: Templates{"ru": {KeyCard: "<p>{nope}</p>"}}, OnError: func(err error) { failed = append(failed, err) }}
	plain := &Formatter{Lang: "ru", Loc: time.UTC}
	now := time.Now()
	if f.RichIncident(webDown(), now) != plain.RichIncident(webDown(), now) || len(failed) != 1 {
		t.Fatalf("fallback: %v", failed)
	}
}

func TestValidateTemplate(t *testing.T) {
	for _, c := range []struct {
		key, text, err string
	}{
		{"card", "<p>{emoji} {name}</p>", ""},
		{"card", "<b>{name}", "unclosed"},
		{"card", "<p>{nope}</p>", "unknown placeholder"},
		{"card.down", "<p>{name}</p>", ""},
		{"card.lunch", "<p>{name}</p>", "unknown"},
		{"remind", "⏰ <b>{name}</b> {text}", ""},
		{"remind", "<h3>{name}</h3>", "unsupported tag"},
	} {
		err := ValidateTemplate("ru", TemplateKey(c.key), c.text)
		if c.err == "" && err != nil || c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)) {
			t.Errorf("%s %q: %v", c.key, c.text, err)
		}
	}
}

// A template whose markup breaks only in some states (a section left
// unclosed that shows once someone acknowledges) costs no alert at runtime:
// the default goes out.
func TestRuntimeBadMarkupFallsBack(t *testing.T) {
	var failed []error
	f := &Formatter{Lang: "ru", Loc: time.UTC, Custom: Templates{"ru": {KeyCard: "<h3>{name}</h3>{?ack}<p>✋ {ack}{/ack}"}}, OnError: func(err error) { failed = append(failed, err) }}
	plain := &Formatter{Lang: "ru", Loc: time.UTC}
	inc := webDown()
	inc.AckBy = "leo"
	now := time.Now()
	if f.RichIncident(inc, now) != plain.RichIncident(inc, now) || len(failed) != 1 {
		t.Fatalf("fallback: %v", failed)
	}
}

// Saving checks every section both shown and hidden.
func TestValidateTemplateBothStates(t *testing.T) {
	if err := ValidateTemplate("ru", KeyCard, "<h3>{name}</h3>{?ack}<p>✋ {ack}{/ack}"); err == nil {
		t.Fatal("a section broken only when shown must be refused")
	}
	if err := ValidateTemplate("ru", KeyCard, "<h3>{name}</h3>{?link}<p>{link}{/link}"); err == nil {
		t.Fatal("a section broken only when the link is there must be refused")
	}
	if err := ValidateTemplate("ru", KeyResolved, "<h3>{name}</h3>{?resolved_by}<p>{resolved_by}</p>{/resolved_by}"); err != nil {
		t.Fatal(err)
	}
}

// The template editor previews rich markup as HTML in the admin's browser:
// an attribute is script there. Only links to http, https and tg, and the
// fixed attributes of rich markup, pass.
func TestMarkupAttributes(t *testing.T) {
	bad := []string{
		`<details open ontoggle="alert(1)"><summary>x</summary>y</details>`,
		`<details ontoggle=alert(1) open><summary>x</summary>y</details>`,
		`<a href="javascript:alert(1)">x</a>`,
		`<a href="JavaScript:alert(1)">x</a>`,
		`<a href="jav&#97;script:alert(1)">x</a>`,
		`<a href=" javascript:alert(1)">x</a>`,
		`<a href="java&#x09;script:alert(1)">x</a>`,
		`<a href="data:text/html,x">x</a>`,
		`<a href="//evil.example.com">x</a>`,
		`<a href="https://ok.example.com" onclick="alert(1)">x</a>`,
		`<a href="https://ok.example.com" href="javascript:alert(1)">x</a>`,
		`<b onclick="alert(1)">x</b>`,
		`<p style="background:url(javascript:alert(1))">x</p>`,
		`<table onmouseover="alert(1)"><tr><td>x</td></tr></table>`,
		`<td colspan="2 onclick=alert(1)">x</td>`,
		`<blockquote expandable="x" onclick="y">x</blockquote>`,
		`<code class="x onmouseover=alert(1)">x</code>`,
		`<a href="https://x.example.com>x</a>`,
	}
	for _, s := range bad {
		if err := ValidateRich(s); err == nil {
			t.Errorf("rich accepted %s", s)
		}
		if err := ValidateHTML(s); err == nil {
			t.Errorf("html accepted %s", s)
		}
	}
	good := []string{
		`<a href="https://hub.example.com/checks/x?a=1&amp;b=2">x</a>`,
		`<a href='http://hub.example.com'>x</a>`,
		`<a href="tg://resolve?domain=uptimeforge">x</a>`,
		`<blockquote expandable>x</blockquote>`,
		`<pre><code class="language-go">x</code></pre>`,
	}
	for _, s := range good {
		if err := ValidateHTML(s); err != nil {
			t.Errorf("html refused %s: %v", s, err)
		}
	}
	for _, s := range append(good, `<table bordered striped compact><tr><td colspan="2">x</td></tr></table>`, `<details open><summary>x</summary>y</details>`) {
		if err := ValidateRich(s); err != nil {
			t.Errorf("rich refused %s: %v", s, err)
		}
	}
	if err := ValidateTemplate("ru", KeyCard, `<p><a href="javascript:alert(1)">{name}</a></p>`); err == nil || !strings.Contains(err.Error(), "href") {
		t.Fatalf("a template with a script link must be refused: %v", err)
	}
	if err := ValidateTemplate("ru", KeyCard, `<p>{name}</p>{?link}<p><a href="{link}">open</a></p>{/link}`); err != nil {
		t.Fatalf("a template linking to the hub: %v", err)
	}
}
