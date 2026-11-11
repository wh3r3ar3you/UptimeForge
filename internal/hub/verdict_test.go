package hub

import (
	"testing"

	"github.com/wh3r3ar3you/uptimeforge/internal/model"
)

func lines(spec ...string) []model.ProbeLine {
	// "ru:-" = probe in ru failing, "de:+" = probe in de ok
	var out []model.ProbeLine
	for i, s := range spec {
		out = append(out, model.ProbeLine{Probe: string(rune('a' + i)), Region: s[:2], OK: s[3] == '+'})
	}
	return out
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name   string
		lines  []model.ProbeLine
		min    int
		kind   model.Kind
		region string
	}{
		{"no data", nil, 1, model.KindUnknown, ""},
		{"all ok", lines("ru:+", "de:+"), 1, model.KindOK, ""},
		{"all down", lines("ru:-", "ru:-", "de:-"), 1, model.KindDown, ""},
		{"blocked in ru", lines("ru:-", "ru:-", "de:+", "nl:+"), 1, model.KindBlocked, "ru"},
		{"one ru isp only", lines("ru:-", "ru:+", "de:+"), 1, model.KindPartial, ""},
		{"failures in two regions", lines("ru:-", "de:-", "nl:+"), 1, model.KindPartial, ""},
		{"below min_failing", lines("ru:-", "ru:+", "de:+"), 2, model.KindOK, ""},
		{"min_failing capped by eligible probes", lines("ru:-"), 2, model.KindDown, ""},
		{"only foreign fails", lines("ru:+", "de:-"), 1, model.KindBlocked, "de"},
	}
	for _, c := range cases {
		kind, region := classify(c.lines, c.min)
		if kind != c.kind || region != c.region {
			t.Errorf("%s: got %s/%q, want %s/%q", c.name, kind, region, c.kind, c.region)
		}
	}
}

// Probes that have not reported yet may still disagree: no down or blocked
// from the ones that did, and they count towards min_failing.
func TestClassifyExpecting(t *testing.T) {
	cases := []struct {
		name     string
		lines    []model.ProbeLine
		min, exp int
		kind     model.Kind
	}{
		{"first back fails", lines("ru:-"), 1, 2, model.KindUnknown},
		{"one ru blocked so far", lines("ru:-", "de:+"), 1, 1, model.KindUnknown},
		{"partial stays partial", lines("ru:-", "ru:+"), 1, 1, model.KindPartial},
		{"min_failing counts the expected", lines("ru:-"), 2, 2, model.KindOK},
		{"nothing expected", lines("ru:-"), 2, 0, model.KindDown},
	}
	for _, c := range cases {
		if kind, _ := classifyExpecting(c.lines, c.min, c.exp); kind != c.kind {
			t.Errorf("%s: got %s, want %s", c.name, kind, c.kind)
		}
	}
}
