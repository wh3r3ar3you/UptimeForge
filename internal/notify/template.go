package notify

import (
	"fmt"
	"slices"
	"strings"
)

// Value is what a placeholder stands for: text, escaped when it is put into
// the card, or a fragment of markup the formatter built, put in as is.
type Value struct {
	s    string
	html bool
}

func Text(s string) Value { return Value{s: s} }
func HTML(s string) Value { return Value{s: s, html: true} }

// Vars are a card's placeholder values.
type Vars map[string]Value

// Placeholders are every name a template may use, for the editor and to
// tell a typo from a value that is empty for this incident.
var Placeholders = []string{
	"emoji", "title", "name", "group", "tags", "target", "region", "flag",
	"details", "probes", "affected", "why", "since", "started", "duration",
	"users", "users_count", "node", "link", "open", "ack", "muted",
	"failing", "total", "drop_before", "drop_now", "host_metric", "host_value", "host_limit", "peak",
	"lines", "resolved_by", "text",
	"period", "uptime", "uptime_delta", "groups", "worst", "nodes", "bans", "incidents", "mttr", "online", "traffic",
}

func validName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

// Render fills a template: {name} is a value (text escaped), {?name}…{/name}
// shows its inside only when the value is not empty, {{ and }} are literal braces.
// A name that is no placeholder at all is an error; a known one with no
// value here renders empty.
func Render(tpl string, v Vars) (string, error) {
	var b strings.Builder
	if err := render(&b, tpl, v); err != nil {
		return "", err
	}
	return b.String(), nil
}

func render(b *strings.Builder, tpl string, v Vars) error {
	for i := 0; i < len(tpl); {
		if tpl[i] != '{' {
			j := strings.IndexByte(tpl[i:], '{')
			if j < 0 {
				b.WriteString(strings.ReplaceAll(tpl[i:], "}}", "}"))
				return nil
			}
			b.WriteString(strings.ReplaceAll(tpl[i:i+j], "}}", "}"))
			i += j
			continue
		}
		if strings.HasPrefix(tpl[i:], "{{") {
			b.WriteByte('{')
			i += 2
			continue
		}
		end := strings.IndexByte(tpl[i:], '}')
		if end < 0 {
			b.WriteString(tpl[i:])
			return nil
		}
		tag := tpl[i+1 : i+end]
		switch {
		case strings.HasPrefix(tag, "?") && validName(tag[1:]):
			name := tag[1:]
			if !slices.Contains(Placeholders, name) {
				return fmt.Errorf("unknown placeholder {%s}", name)
			}
			closer := "{/" + name + "}"
			k := strings.Index(tpl[i+end+1:], closer)
			if k < 0 {
				return fmt.Errorf("{?%s} has no closing %s", name, closer)
			}
			inner := tpl[i+end+1 : i+end+1+k]
			if v[name].s != "" {
				if err := render(b, inner, v); err != nil {
					return err
				}
			}
			i += end + 1 + k + len(closer)
		case strings.HasPrefix(tag, "/") && validName(tag[1:]):
			return fmt.Errorf("%s{%s} without {?%s}", "", tag, tag[1:])
		case validName(tag):
			if !slices.Contains(Placeholders, tag) {
				return fmt.Errorf("unknown placeholder {%s}", tag)
			}
			if x := v[tag]; x.html {
				b.WriteString(x.s)
			} else {
				b.WriteString(esc(x.s))
			}
			i += end + 1
		default: // not a placeholder: a literal brace
			b.WriteByte('{')
			i++
		}
	}
	return nil
}
