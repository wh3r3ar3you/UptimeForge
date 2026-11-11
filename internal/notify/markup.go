package notify

import (
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode"
)

// telegramTags are the tags Telegram's HTML parse mode accepts.
var telegramTags = map[string]bool{"b": true, "strong": true, "i": true, "em": true, "u": true, "ins": true, "s": true,
	"strike": true, "del": true, "a": true, "code": true, "pre": true, "blockquote": true, "tg-spoiler": true}

// richTags are what rich messages add on top.
var richTags = map[string]bool{"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "p": true, "footer": true,
	"table": true, "tr": true, "th": true, "td": true, "details": true, "summary": true, "ul": true, "ol": true, "li": true,
	"caption": true, "mark": true}

// Attributes are checked as strictly as tags: the template editor previews
// rich markup as HTML in the admin's browser, where an attribute is script
// (<details open ontoggle=…>, <a href="javascript:…">). Each kind says what
// a value may be.
const (
	attrFlag = "flag" // no value: <blockquote expandable>
	attrURL  = "url"  // an http, https or tg link
	attrNum  = "num"  // a small number: colspan="2"
	attrLang = "lang" // language-<name>: <code class="language-go">
)

// telegramAttrs are the attributes Telegram's HTML parse mode uses.
var telegramAttrs = map[string]map[string]string{
	"a":          {"href": attrURL},
	"blockquote": {"expandable": attrFlag},
	"code":       {"class": attrLang},
}

// richAttrs are what rich messages add on top.
var richAttrs = map[string]map[string]string{
	"table":   {"bordered": attrFlag, "striped": attrFlag, "compact": attrFlag},
	"td":      {"colspan": attrNum, "rowspan": attrNum},
	"th":      {"colspan": attrNum, "rowspan": attrNum},
	"details": {"open": attrFlag},
}

// ValidateHTML says why Telegram would refuse s in HTML parse mode ("can't
// parse entities"), and a refused message is a lost alert: only its tags,
// balanced, with only their own attributes, and every & starting a valid
// entity.
func ValidateHTML(s string) error { return validateMarkup(s, telegramTags, telegramAttrs) }

// ValidateRich is ValidateHTML for rich message markup.
func ValidateRich(s string) error {
	all := map[string]bool{"hr": true}
	for k := range telegramTags {
		all[k] = true
	}
	for k := range richTags {
		all[k] = true
	}
	attrs := map[string]map[string]string{}
	for k, v := range telegramAttrs {
		attrs[k] = v
	}
	for k, v := range richAttrs {
		attrs[k] = v
	}
	return validateMarkup(s, all, attrs)
}

func validateMarkup(s string, allowed map[string]bool, attrs map[string]map[string]string) error {
	var stack []string
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<':
			name, closing, n, err := parseTag(s[i:], allowed, attrs)
			if err != nil {
				return err
			}
			switch {
			case name == "hr":
			case closing:
				if len(stack) == 0 || stack[len(stack)-1] != name {
					return errors.New("unbalanced </" + name + ">")
				}
				stack = stack[:len(stack)-1]
			default:
				stack = append(stack, name)
			}
			i += n - 1
		case '&':
			if err := checkEntity(s[i:]); err != nil {
				return err
			}
		}
	}
	if len(stack) > 0 {
		return errors.New("unclosed <" + stack[len(stack)-1] + ">")
	}
	return nil
}

// checkEntity checks the entity s starts with.
func checkEntity(s string) error {
	end := strings.IndexByte(s, ';')
	ent := ""
	if end > 0 {
		ent = s[1:end]
	}
	ok := ent == "lt" || ent == "gt" || ent == "amp" || ent == "quot"
	if strings.HasPrefix(ent, "#") && len(ent) > 1 {
		ok = strings.Trim(ent[1:], "0123456789") == ""
	}
	if !ok {
		return errors.New("bad entity at " + s[:min(len(s), 10)])
	}
	return nil
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func isNameByte(c byte) bool { return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' }

// parseTag reads the tag s starts with: its name, whether it closes, and its
// length. Every attribute must be one attrs allows for the tag, once.
func parseTag(s string, allowed map[string]bool, attrs map[string]map[string]string) (name string, closing bool, n int, err error) {
	end := strings.IndexByte(s, '>')
	if end < 0 {
		return "", false, 0, errors.New("unclosed <")
	}
	shown := s[:end+1]
	i := 1
	if i < len(s) && s[i] == '/' {
		closing, i = true, i+1
	}
	start := i
	for i < len(s) && isNameByte(s[i]) {
		i++
	}
	name = s[start:i]
	if name == "" || !allowed[name] || i >= len(s) || s[i] != '>' && !isSpace(s[i]) {
		return "", false, 0, errors.New("unsupported tag " + shown)
	}
	seen := map[string]bool{}
	for {
		for i < len(s) && isSpace(s[i]) {
			i++
		}
		if i >= len(s) {
			return "", false, 0, errors.New("unclosed <" + name)
		}
		if s[i] == '>' {
			return name, closing, i + 1, nil
		}
		if closing {
			return "", false, 0, errors.New("unsupported tag " + shown)
		}
		start := i
		for i < len(s) && isNameByte(s[i]) {
			i++
		}
		attr := s[start:i]
		kind, ok := attrs[name][attr]
		if attr == "" || !ok {
			return "", false, 0, fmt.Errorf("unsupported attribute %q in <%s>", strings.TrimSpace(firstWord(s[start:])), name)
		}
		if seen[attr] {
			return "", false, 0, fmt.Errorf("attribute %s repeated in <%s>", attr, name)
		}
		seen[attr] = true
		var val string
		has := i < len(s) && s[i] == '='
		if has {
			i++
			switch {
			case i < len(s) && (s[i] == '"' || s[i] == '\''):
				q := s[i]
				close := strings.IndexByte(s[i+1:], q)
				if close < 0 {
					return "", false, 0, fmt.Errorf("unclosed quote in <%s %s>", name, attr)
				}
				val, i = s[i+1:i+1+close], i+2+close
			default:
				start := i
				for i < len(s) && !isSpace(s[i]) && s[i] != '>' {
					i++
				}
				val = s[start:i]
				if val == "" || strings.ContainsAny(val, "\"'<=`") {
					return "", false, 0, fmt.Errorf("bad value of %s in <%s>", attr, name)
				}
			}
			if i < len(s) && s[i] != '>' && !isSpace(s[i]) {
				return "", false, 0, fmt.Errorf("bad value of %s in <%s>", attr, name)
			}
		}
		if err := checkAttr(name, attr, kind, val, has); err != nil {
			return "", false, 0, err
		}
	}
}

func firstWord(s string) string {
	if i := strings.IndexFunc(s, func(r rune) bool { return r == '=' || r == '>' || unicode.IsSpace(r) }); i >= 0 {
		return s[:i]
	}
	return s
}

var (
	numRe  = regexp.MustCompile(`^[0-9]{1,3}$`)
	langRe = regexp.MustCompile(`^language-[A-Za-z0-9_+#.-]{1,32}$`)
)

func checkAttr(tag, attr, kind, val string, has bool) error {
	bad := func(why string) error { return fmt.Errorf("attribute %s of <%s> %s", attr, tag, why) }
	if kind == attrFlag {
		if has {
			return bad("takes no value")
		}
		return nil
	}
	if !has {
		return bad("needs a value")
	}
	for i := 0; i < len(val); i++ {
		if val[i] == '&' {
			if err := checkEntity(val[i:]); err != nil {
				return err
			}
		}
	}
	switch kind {
	case attrNum:
		if !numRe.MatchString(val) {
			return bad("must be a number")
		}
	case attrLang:
		if !langRe.MatchString(val) {
			return bad("must be language-<name>")
		}
	case attrURL:
		if !safeLink(html.UnescapeString(val)) {
			return bad("must be an http, https or tg link")
		}
	}
	return nil
}

// safeLink says whether u is an absolute http, https or tg link with nothing
// a browser would strip or reinterpret (spaces, control characters).
func safeLink(u string) bool {
	if strings.ContainsFunc(u, func(r rune) bool { return r < 0x20 || r == 0x7f || unicode.IsSpace(r) }) {
		return false
	}
	scheme, rest, ok := strings.Cut(u, ":")
	if !ok {
		return false
	}
	switch strings.ToLower(scheme) {
	case "http", "https":
		return strings.HasPrefix(rest, "//") && len(rest) > 2
	case "tg":
		return rest != ""
	}
	return false
}

var richToHTML = []struct {
	re  *regexp.Regexp
	out string
}{
	{regexp.MustCompile(`<h[1-6][^>]*>`), "<b>"},
	{regexp.MustCompile(`</h[1-6]>`), "</b>\n"},
	{regexp.MustCompile(`<details[^>]*>`), "<blockquote expandable>"},
	{regexp.MustCompile(`</details>`), "</blockquote>\n"},
	{regexp.MustCompile(`<summary>`), ""},
	{regexp.MustCompile(`</summary>`), "\n"},
	{regexp.MustCompile(`</t[dh]>\s*<td[^>]*>`), " — "},
	{regexp.MustCompile(`</th>\s*<th[^>]*>`), "</b> — <b>"},
	{regexp.MustCompile(`<th[^>]*>`), "<b>"},
	{regexp.MustCompile(`</th>`), "</b>"},
	{regexp.MustCompile(`</?(td|table|caption|ul|ol)[^>]*>`), ""},
	{regexp.MustCompile(`<tr>`), ""},
	{regexp.MustCompile(`</tr>`), "\n"},
	{regexp.MustCompile(`<p>`), ""},
	{regexp.MustCompile(`</p>`), "\n"},
	{regexp.MustCompile(`<footer>`), "<i>"},
	{regexp.MustCompile(`</footer>`), "</i>\n"},
	{regexp.MustCompile(`<li>`), "• "},
	{regexp.MustCompile(`</li>`), "\n"},
	{regexp.MustCompile(`<hr>`), "———\n"},
	{regexp.MustCompile(`<mark>`), "<u>"},
	{regexp.MustCompile(`</mark>`), "</u>"},
	{regexp.MustCompile(`</blockquote>(\n?)`), "</blockquote>\n"},
	{regexp.MustCompile(`\n{3,}`), "\n\n"},
}

// RichToHTML turns a rich card into Telegram HTML for API servers without
// rich messages: headings bold, rows "a — b", details an expandable quote.
func RichToHTML(rich string) string {
	s := rich
	for _, r := range richToHTML {
		s = r.re.ReplaceAllString(s, r.out)
	}
	return strings.TrimRight(s, "\n")
}
