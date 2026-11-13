// Package proto is the agent <-> hub wire protocol: JSON envelopes over a
// WebSocket that the agent opens, so probes behind NAT need no open ports.
package proto

import (
	"encoding/json"

	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
)

const Version = 1

const (
	TypeHello     = "hello"      // agent -> hub, first message
	TypeAssign    = "assign"     // hub -> agent, full set of checks (replaces the previous one)
	TypeResults   = "results"    // agent -> hub, batch of results
	TypeSysInfo   = "sysinfo"    // agent -> hub, static host facts, after hello
	TypeMetrics   = "metrics"    // agent -> hub, host metrics every few seconds
	TypeRun       = "run"        // hub -> agent, run one check now (UI tools)
	TypeRunResult = "run_result" // agent -> hub
	TypeLive      = "live"       // hub -> agent, send host metrics fast for a while
)

// Live asks for host metrics every IntervalMS for TTLMS; each new Live
// restarts the TTL, and when it runs out the agent returns to its own
// interval. Ignored by an agent whose metrics are switched off.
type Live struct {
	IntervalMS int `json:"interval_ms"`
	TTLMS      int `json:"ttl_ms"`
}

type Envelope struct {
	Type string          `json:"t"`
	Data json.RawMessage `json:"d,omitempty"`
}

func Encode(typ string, v any) ([]byte, error) {
	d, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Envelope{Type: typ, Data: d})
}

type Hello struct {
	Proto    int    `json:"proto"`
	Version  string `json:"version"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
}

type Assign struct {
	Probe       string          `json:"probe"`
	Via         json.RawMessage `json:"via,omitempty"` // upstream proxy (Xray outbound) for every check
	Concurrency int             `json:"concurrency,omitempty"`
	Checks      []checks.Spec   `json:"checks"`
	// Metrics false tells the agent to send no host metrics or sysinfo; nil
	// (older hubs) means send them.
	Metrics *bool `json:"metrics,omitempty"`
}

type Results struct {
	Results []checks.Result `json:"results"`
}

// SysInfo describes the host an agent runs on.
type SysInfo struct {
	Hostname       string   `json:"hostname"`
	OS             string   `json:"os"`
	Platform       string   `json:"platform,omitempty"` // ubuntu 24.04, openwrt, ...
	Kernel         string   `json:"kernel,omitempty"`
	Arch           string   `json:"arch"`
	CPUModel       string   `json:"cpu_model,omitempty"`
	Cores          int      `json:"cores,omitempty"`
	MemTotal       uint64   `json:"mem_total,omitempty"`
	BootTime       int64    `json:"boot_time,omitempty"`
	NICs           []string `json:"nics,omitempty"`      // interfaces counted in rx/tx
	LinkMbps       int      `json:"link_mbps,omitempty"` // fastest of NICs, 0 if unknown
	Virtualization string   `json:"virtualization,omitempty"`
}

// Metrics is a host snapshot; rates are averaged since the previous one.
type Metrics struct {
	CPU       float64 `json:"cpu"` // percent of all cores
	MemUsed   uint64  `json:"mem_used"`
	MemTotal  uint64  `json:"mem_total"`
	SwapUsed  uint64  `json:"swap_used,omitempty"`
	DiskUsed  uint64  `json:"disk_used"`
	DiskTotal uint64  `json:"disk_total"`
	RX        float64 `json:"rx"` // bytes per second
	TX        float64 `json:"tx"`
	Load1     float64 `json:"load1"`
	Load5     float64 `json:"load5"`
	Load15    float64 `json:"load15"`
	Uptime    uint64  `json:"uptime"`        // seconds
	TCP       int     `json:"tcp,omitempty"` // sockets in use (VPN nodes: tracks user load)
	Age       float64 `json:"age,omitempty"`
	// Ext is absent from agents older than the field, so "missing" stays
	// distinguishable from a real zero steal or iowait.
	Ext *HostExt `json:"ext,omitempty"`
}

// HostExt is the detail behind CPU, memory, disk and network: the breakdown
// that tells a noisy neighbour (steal) from a slow disk (iowait) from load.
type HostExt struct {
	CPUUser   float64 `json:"cpu_user"` // percent of all cores, nice included
	CPUSystem float64 `json:"cpu_system"`
	CPUIowait float64 `json:"cpu_iowait"`
	CPUSteal  float64 `json:"cpu_steal"`
	CPUIRQ    float64 `json:"cpu_irq"`   // hard and soft interrupts
	MemCache  uint64  `json:"mem_cache"` // buffers and page cache
	SwapTotal uint64  `json:"swap_total"`
	DiskRead  float64 `json:"disk_read"` // bytes per second over whole disks
	DiskWrite float64 `json:"disk_write"`
	DiskROps  float64 `json:"disk_rops"` // operations per second
	DiskWOps  float64 `json:"disk_wops"`
	DiskUtil  float64 `json:"disk_util"` // percent of time the busiest disk had I/O in flight
	RXPkts    float64 `json:"rx_pkts"`   // packets per second
	TXPkts    float64 `json:"tx_pkts"`
	NetErr    float64 `json:"net_err"` // errors and drops per second, both directions
	Procs     int     `json:"procs"`
	ProcsD    int     `json:"procs_blocked"` // waiting on I/O: the queue behind iowait
}

type Run struct {
	ID   string      `json:"id"`
	Spec checks.Spec `json:"spec"`
}

type RunResult struct {
	ID     string        `json:"id"`
	Result checks.Result `json:"result"`
}
