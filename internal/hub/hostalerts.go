package hub

import (
	"fmt"
	"slices"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
	"github.com/wh3r3ar3you/uptimeforge/internal/model"
	"github.com/wh3r3ar3you/uptimeforge/internal/proto"
)

// HostRule raises an incident when a server metric stays above a threshold
// for a while, the way a Zabbix trigger does: every sample over the window
// must be above, so one spike is not an alert.
type HostRule struct {
	Metric string          `yaml:"metric" json:"metric"`
	Above  float64         `yaml:"above" json:"above"`
	For    checks.Duration `yaml:"for" json:"for,omitempty"`
	Off    bool            `yaml:"off" json:"off,omitempty"`
}

// HostMetrics are the metrics a rule can watch. All are percentages except
// load, which is the 1-minute load average per core.
var HostMetrics = []string{"cpu", "mem", "disk", "swap", "steal", "iowait", "load"}

// DefaultHostRules follow the Zabbix Linux template where it has an opinion.
func DefaultHostRules() []HostRule {
	m := func(d time.Duration) checks.Duration { return checks.Duration(d) }
	return []HostRule{
		{Metric: "cpu", Above: 90, For: m(5 * time.Minute)},
		{Metric: "mem", Above: 90, For: m(5 * time.Minute)},
		{Metric: "disk", Above: 90, For: m(time.Minute)},
		{Metric: "swap", Above: 80, For: m(10 * time.Minute)},
		{Metric: "steal", Above: 20, For: m(10 * time.Minute)},
		{Metric: "iowait", Above: 30, For: m(10 * time.Minute)},
		{Metric: "load", Above: 1.5, For: m(10 * time.Minute)},
	}
}

// HostRules is the effective rule set: the defaults with the configured
// overrides applied by metric.
func (c *Config) HostRules() []HostRule {
	rules := DefaultHostRules()
	for _, o := range c.Alerts.Host {
		if i := slices.IndexFunc(rules, func(r HostRule) bool { return r.Metric == o.Metric }); i >= 0 {
			rules[i] = o
		}
	}
	return rules
}

func (c *Config) hostRuleOn(metric string) bool {
	for _, r := range c.HostRules() {
		if r.Metric == metric {
			return !r.Off
		}
	}
	return false
}

func validateHostRules(rules []HostRule) error {
	seen := map[string]bool{}
	for _, r := range rules {
		if !slices.Contains(HostMetrics, r.Metric) {
			return fmt.Errorf("alerts.host: unknown metric %q (want one of %v)", r.Metric, HostMetrics)
		}
		if seen[r.Metric] {
			return fmt.Errorf("alerts.host: %s is listed twice", r.Metric)
		}
		seen[r.Metric] = true
		if r.Off {
			continue
		}
		if r.Above <= 0 || (r.Metric != "load" && r.Above >= 100) || (r.Metric == "load" && r.Above > 100) {
			return fmt.Errorf("alerts.host: %s threshold %v is out of range", r.Metric, r.Above)
		}
		if r.For.D() < 0 || r.For.D() > 24*time.Hour {
			return fmt.Errorf("alerts.host: %s duration must be between 0 and 24h", r.Metric)
		}
	}
	return nil
}

// hostState times one rule on one server.
type hostState struct {
	above, below time.Time // when the current run above / below began
}

// hysteresis: a value must fall this far under the threshold to count as
// recovered, so a metric hovering at the line does not flap.
const hysteresis = 0.95

// hostValue reads a metric from a snapshot; ok is false when the agent does
// not report it (older agents have no steal, iowait or swap total).
func hostValue(metric string, m proto.Metrics, cores int) (float64, bool) {
	pct := func(a, b float64) (float64, bool) {
		if b <= 0 {
			return 0, false
		}
		return a / b * 100, true
	}
	switch metric {
	case "cpu":
		return m.CPU, true
	case "mem":
		return pct(float64(m.MemUsed), float64(m.MemTotal))
	case "disk":
		return pct(float64(m.DiskUsed), float64(m.DiskTotal))
	case "swap":
		if m.Ext == nil {
			return 0, false
		}
		return pct(float64(m.SwapUsed), float64(m.Ext.SwapTotal))
	case "steal":
		if m.Ext == nil {
			return 0, false
		}
		return m.Ext.CPUSteal, true
	case "iowait":
		if m.Ext == nil {
			return 0, false
		}
		return m.Ext.CPUIowait, true
	case "load":
		if cores <= 0 {
			return 0, false
		}
		return m.Load1 / float64(cores), true
	}
	return 0, false
}

// evalHost runs the threshold rules on a fresh snapshot. Callers hold h.mu.
func (h *Hub) evalHost(p *probe, m proto.Metrics, at time.Time) {
	cores := 0
	if p.sysinfo != nil {
		cores = p.sysinfo.Cores
	}
	if p.host == nil {
		p.host = map[string]*hostState{}
	}
	for _, r := range h.cfg.HostRules() {
		key := "host:" + p.cfg.ID + ":" + r.Metric
		if r.Off {
			delete(p.host, r.Metric)
			continue // an open incident is resolved by the config change
		}
		v, ok := hostValue(r.Metric, m, cores)
		if !ok {
			continue
		}
		st := p.host[r.Metric]
		if st == nil {
			st = &hostState{}
			p.host[r.Metric] = st
		}
		hv := &model.HostValue{Metric: r.Metric, Value: v, Above: r.Above, For: int64(r.For.D() / time.Second)}
		line := h.probeLine(p, false, "host_"+r.Metric, "")
		mt := meta{name: p.cfg.Name, typ: "host", severity: "warning", host: hv}
		open := h.incidents[key] != nil
		switch {
		case v > r.Above:
			st.below = time.Time{}
			if st.above.IsZero() {
				st.above = at
			}
			if at.Sub(st.above) >= r.For.D() {
				h.transition(key, model.KindHost, "", []model.ProbeLine{line}, mt, at)
			}
		case v <= r.Above*hysteresis:
			st.above = time.Time{}
			if !open {
				st.below = time.Time{}
				h.recovered(key) // under the recovery line: a manual close is lifted
				continue
			}
			if st.below.IsZero() {
				st.below = at
			}
			if at.Sub(st.below) >= recoverAfter(r) {
				line.OK, line.Reason = true, ""
				h.transition(key, model.KindOK, "", []model.ProbeLine{line}, mt, at)
				st.below = time.Time{}
			}
		default: // between the recovery line and the threshold: neither run continues
			st.above, st.below = time.Time{}, time.Time{}
			if open {
				h.transition(key, model.KindHost, "", []model.ProbeLine{line}, mt, at) // keep the value fresh
			}
		}
	}
}

// recoverAfter is how long a metric must stay under the line before the
// incident closes: half the rule's window, at most two minutes.
func recoverAfter(r HostRule) time.Duration {
	return min(r.For.D()/2, 2*time.Minute)
}

// mergeHost keeps the peak across updates of a host incident.
func mergeHost(old, cur *model.HostValue) *model.HostValue {
	if cur == nil {
		return old
	}
	v := *cur
	v.Peak = v.Value
	if old != nil && old.Peak > v.Peak {
		v.Peak = old.Peak
	}
	return &v
}
