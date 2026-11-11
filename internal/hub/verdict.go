package hub

import (
	"slices"

	"github.com/wh3r3ar3you/uptimeforge/internal/model"
)

// classify turns the eligible probes' views of a check into one verdict.
//
//   - nobody fails (or fewer than minFailing)  -> ok
//   - everybody fails                           -> down: the service itself is broken
//   - every probe of one region fails and all
//     failures are in that region               -> blocked: it is alive, but filtered there
//   - anything else                             -> partial: some networks/ISPs can't reach it
//
// Probes that are offline, stale or without internet never get here, so a dead
// probe can't produce a false "blocked".
func classify(lines []model.ProbeLine, minFailing int) (model.Kind, string) {
	return classifyExpecting(lines, minFailing, 0)
}

// classifyExpecting is classify with `expected` more probes that have not
// reported yet (just after a hub restart or a reconnect) and may still. They
// count towards minFailing's "of how many", and while any is outstanding no
// "down" or "blocked" is made from the probes that did report: the next one
// may well disagree. That verdict is unknown until they report or are
// confirmed offline.
func classifyExpecting(lines []model.ProbeLine, minFailing, expected int) (model.Kind, string) {
	if len(lines) == 0 {
		return model.KindUnknown, ""
	}
	failing := 0
	for _, l := range lines {
		if !l.OK {
			failing++
		}
	}
	if failing == 0 || failing < min(minFailing, len(lines)+expected) {
		return model.KindOK, ""
	}
	kind, region := spread(lines, failing)
	if expected > 0 && (kind == model.KindDown || kind == model.KindBlocked) {
		return model.KindUnknown, ""
	}
	return kind, region
}

// spread tells down, blocked and partial apart by where the failures are.
func spread(lines []model.ProbeLine, failing int) (model.Kind, string) {
	if failing == len(lines) {
		return model.KindDown, ""
	}
	region := ""
	for _, l := range lines {
		if l.OK {
			continue
		}
		if l.Region == "" || (region != "" && l.Region != region) {
			return model.KindPartial, ""
		}
		region = l.Region
	}
	for _, l := range lines {
		if l.Region == region && l.OK {
			return model.KindPartial, ""
		}
	}
	return model.KindBlocked, region
}

func severityFor(kind model.Kind, override string) model.Severity {
	s := model.Warning
	if kind == model.KindDown || kind == model.KindBlocked {
		s = model.Critical
	}
	switch override {
	case "warning":
		s = model.Warning
	case "critical":
		s = model.Critical
	}
	return s
}

func failingSet(lines []model.ProbeLine) []string {
	var out []string
	for _, l := range lines {
		if !l.OK {
			out = append(out, l.Probe)
		}
	}
	slices.Sort(out)
	return out
}
