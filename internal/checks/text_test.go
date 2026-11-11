package checks

import (
	"os"
	"strings"
	"testing"
)

// The form has nothing else to show for a catalog entry than these words, and
// a missing key would render as its raw name.
func TestCheckTextCoversCatalog(t *testing.T) {
	raw, err := os.ReadFile("../../web/src/components/checkText.ts")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	ruStart, enStart := strings.Index(text, "\n  ru: {"), strings.Index(text, "\n  en: {")
	if ruStart < 0 || enStart < ruStart {
		t.Fatal("no ru/en sections")
	}
	if strings.Contains(text, "—") {
		t.Error("em dash in checkText.ts")
	}
	for lang, section := range map[string]string{"ru": text[ruStart:enStart], "en": text[enStart:]} {
		has := func(key string) bool {
			return strings.Contains(section, "\n    "+key+":") || strings.Contains(section, "\n    '"+key+"':")
		}
		for _, info := range Catalog() {
			name := info.Type
			if info.Preset != "" {
				name = info.Preset
			}
			for _, k := range []string{"t_" + name, "d_" + name, "c_" + info.Category} {
				if !has(k) {
					t.Errorf("%s: no %s", lang, k)
				}
			}
			for _, f := range info.Fields {
				if !has("f_" + f.Name) {
					t.Errorf("%s: no f_%s (%s)", lang, f.Name, info.Type)
				}
			}
		}
	}
}
