// Package model holds the incident types shared by the hub and notifiers.
package model

import "time"

// Kind is the verdict for a check across all probes, or a probe-level problem.
type Kind string

const (
	KindOK      Kind = "ok"
	KindUnknown Kind = "unknown" // no fresh data from any eligible probe
	KindPartial Kind = "partial" // fails on some probes only
	KindBlocked Kind = "blocked" // fails on every probe of one region, works elsewhere
	KindDown    Kind = "down"    // fails everywhere

	KindProbeOffline Kind = "probe_offline" // agent lost connection to the hub
	KindProbeNoNet   Kind = "probe_no_net"  // agent is connected but its baseline checks fail
	KindHost         Kind = "host"          // a server metric stayed past its threshold
	KindUsersDrop    Kind = "users_drop"    // users online on a VPN node collapsed
	KindNodeDown     Kind = "node_down"     // the Remnawave panel lost a node
	KindAgentUpdate  Kind = "agent_update"  // agents sent an update did not come back on it
)

// Rank orders kinds by badness for "worst during the incident".
func (k Kind) Rank() int {
	switch k {
	case KindDown:
		return 4
	case KindBlocked, KindUsersDrop, KindNodeDown:
		return 3
	case KindPartial:
		return 2
	case KindProbeOffline, KindProbeNoNet, KindHost, KindAgentUpdate:
		return 1
	}
	return 0
}

type Severity string

const (
	Critical Severity = "critical"
	Warning  Severity = "warning"
	Info     Severity = "info"
)

// ProbeLine is one probe's view of a check at the time of the snapshot.
type ProbeLine struct {
	Probe     string  `json:"probe"`
	Name      string  `json:"name"`
	Region    string  `json:"region,omitempty"`
	OK        bool    `json:"ok"`
	Reason    string  `json:"reason,omitempty"`
	Error     string  `json:"error,omitempty"`
	LatencyMS float64 `json:"latency_ms,omitempty"`
	Bytes     int64   `json:"bytes,omitempty"`
}

type Incident struct {
	ID    string   `json:"id"`
	Key   string   `json:"key"` // check id, or probe:<id> / probe-net:<id> / sub:<id> / host:<probe>:<metric> / rw:<node uuid>
	Name  string   `json:"name"`
	Group string   `json:"group,omitempty"`
	Tags  []string `json:"tags,omitempty"`
	// CauseKey is the incident that explains this one (a check on a node that
	// is down); such an incident is not announced.
	CauseKey string `json:"cause,omitempty"`
	// Affected names the checks this incident took down with it.
	Affected []string    `json:"affected,omitempty"`
	Type     string      `json:"type,omitempty"`
	Target   string      `json:"target,omitempty"`
	Kind     Kind        `json:"kind"`
	Worst    Kind        `json:"worst"`
	Region   string      `json:"region,omitempty"`
	Severity Severity    `json:"severity"`
	Lines    []ProbeLine `json:"lines,omitempty"`
	Note     string      `json:"note,omitempty"`

	// Peak is the worst failing/total probe count seen during the incident.
	PeakFailing int `json:"peak_failing,omitempty"`
	PeakTotal   int `json:"peak_total,omitempty"`

	// Node and Users come from the VPN panel: the node behind the check and
	// how many users were online on it (the highest count seen).
	Node  string `json:"node,omitempty"`
	Users int    `json:"users,omitempty"`

	// Host is what a host threshold incident measures.
	Host *HostValue `json:"host,omitempty"`
	// Drop is the collapse behind a users_drop incident.
	Drop *UsersDrop `json:"drop,omitempty"`

	Started  time.Time `json:"started"`
	Updated  time.Time `json:"updated"`
	Resolved time.Time `json:"resolved,omitzero"`

	AckBy        string    `json:"ack_by,omitempty"`
	ResolvedBy   string    `json:"resolved_by,omitempty"` // closed by hand, not by recovery
	MutedUntil   time.Time `json:"muted_until,omitzero"`
	MutedBy      string    `json:"muted_by,omitempty"`
	Notified     bool      `json:"notified,omitempty"`
	LastNotified time.Time `json:"last_notified,omitzero"`
}

// HostValue is the metric behind a host incident: the latest value, the
// worst one seen while it was open, and the rule that raised it.
type HostValue struct {
	Metric string  `json:"metric"`
	Value  float64 `json:"value"`
	Peak   float64 `json:"peak"`
	Above  float64 `json:"above"`
	For    int64   `json:"for_s,omitempty"` // seconds
}

// UsersDrop is users online on a node: the baseline before the drop, now,
// the lowest seen, and whether the node was still connected to the panel.
type UsersDrop struct {
	Before    int  `json:"before"`
	Now       int  `json:"now"`
	Low       int  `json:"low"`
	Connected bool `json:"connected"`
}

func (i *Incident) Failing() (n int) {
	for _, l := range i.Lines {
		if !l.OK {
			n++
		}
	}
	return n
}

// TrackPeak records the current snapshot if it is the worst so far.
func (i *Incident) TrackPeak() {
	if n := i.Failing(); n > i.PeakFailing {
		i.PeakFailing, i.PeakTotal = n, len(i.Lines)
	}
}

type EventType string

const (
	EventOpen    EventType = "open"
	EventUpdate  EventType = "update" // verdict or probe set changed
	EventResolve EventType = "resolve"
	EventRemind  EventType = "remind"
	EventRefresh EventType = "refresh" // re-render only (ack, mute), never makes a sound
)

type Event struct {
	Type      EventType
	Incident  Incident // a copy: notifiers run asynchronously
	Escalated bool     // severity went up: deliver loudly even on update
	Silent    bool
}

type Notifier interface {
	Notify(Event)
}

// Actions are what notifiers' buttons can do.
type Actions interface {
	Ack(incidentID, by string) error
	Mute(incidentID string, d time.Duration, by string) error
	// Resolve closes an incident by hand; nothing is raised for the same
	// thing again until it has recovered.
	Resolve(incidentID, by string) error
}
