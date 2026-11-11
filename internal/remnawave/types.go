package remnawave

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Node mirrors NodesSchema (models/nodes.schema).
type Node struct {
	UUID                      string        `json:"uuid"`
	ID                        int64         `json:"id"`
	Name                      string        `json:"name"`
	Address                   string        `json:"address"`
	Port                      *int          `json:"port"`
	ProxyURL                  *string       `json:"proxyUrl"`
	IsConnected               bool          `json:"isConnected"`
	IsDisabled                bool          `json:"isDisabled"`
	IsConnecting              bool          `json:"isConnecting"`
	LastStatusChange          *time.Time    `json:"lastStatusChange"`
	LastStatusMessage         *string       `json:"lastStatusMessage"`
	IsTrafficTrackingActive   bool          `json:"isTrafficTrackingActive"`
	TrafficResetDay           *int          `json:"trafficResetDay"`
	TrafficLimitBytes         *int64        `json:"trafficLimitBytes"`
	TrafficUsedBytes          *int64        `json:"trafficUsedBytes"`
	NotifyPercent             *int          `json:"notifyPercent"`
	ViewPosition              int           `json:"viewPosition"`
	CountryCode               string        `json:"countryCode"`
	ConsumptionMultiplier     float64       `json:"consumptionMultiplier"`
	NodeConsumptionMultiplier float64       `json:"nodeConsumptionMultiplier"`
	Tags                      []string      `json:"tags"`
	IntegrationUUIDs          []string      `json:"integrationUuids"`
	IPs                       []NodeIP      `json:"ips"`
	CreatedAt                 time.Time     `json:"createdAt"`
	UpdatedAt                 time.Time     `json:"updatedAt"`
	ConfigProfile             ConfigProfile `json:"configProfile"`
	ProviderUUID              *string       `json:"providerUuid"`
	Provider                  *Provider     `json:"provider"`
	ActivePluginUUID          *string       `json:"activePluginUuid"`
	System                    *NodeSystem   `json:"system"` // nil until the node has reported
	Versions                  *NodeVersions `json:"versions"`
	XrayUptime                float64       `json:"xrayUptime"` // seconds, 0 when unknown
	UsersOnline               int           `json:"usersOnline"`
	Note                      *string       `json:"note"`
}

type NodeIP struct {
	IP     string `json:"ip"`
	Status string `json:"status"` // INBOUND, OUTBOUND, MANAGEMENT, TRANSIT, MONITORING, RESERVE, BLOCKED, FLAGGED, DEPRECATED, UNKNOWN
}

type ConfigProfile struct {
	ActiveConfigProfileUUID *string   `json:"activeConfigProfileUuid"`
	ActiveInbounds          []Inbound `json:"activeInbounds"`
}

type Inbound struct {
	UUID        string          `json:"uuid"`
	ProfileUUID string          `json:"profileUuid"`
	Tag         string          `json:"tag"`
	Type        string          `json:"type"`
	Network     *string         `json:"network"`
	Security    *string         `json:"security"`
	Port        *int            `json:"port"`
	RawInbound  json.RawMessage `json:"rawInbound"`
}

type Provider struct {
	UUID        string    `json:"uuid"`
	Name        string    `json:"name"`
	FaviconLink *string   `json:"faviconLink"`
	LoginURL    *string   `json:"loginUrl"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type NodeVersions struct {
	Xray string `json:"xray"`
	Node string `json:"node"`
}

type NodeSystem struct {
	Info  NodeSystemInfo  `json:"info"`
	Stats NodeSystemStats `json:"stats"`
}

type NodeSystemInfo struct {
	Arch              string   `json:"arch"`
	CPUs              int      `json:"cpus"`
	CPUModel          string   `json:"cpuModel"`
	MemoryTotal       int64    `json:"memoryTotal"`
	Hostname          string   `json:"hostname"`
	Platform          string   `json:"platform"`
	Release           string   `json:"release"`
	Type              string   `json:"type"`
	Version           string   `json:"version"`
	NetworkInterfaces []string `json:"networkInterfaces"`
}

type NodeSystemStats struct {
	MemoryFree int64             `json:"memoryFree"`
	MemoryUsed int64             `json:"memoryUsed"`
	Uptime     float64           `json:"uptime"`  // host uptime, seconds
	LoadAvg    []float64         `json:"loadAvg"` // 1, 5, 15 min
	Interface  *NetworkInterface `json:"interface"`
}

type NetworkInterface struct {
	Interface     string  `json:"interface"`
	RxBytesPerSec float64 `json:"rxBytesPerSec"`
	TxBytesPerSec float64 `json:"txBytesPerSec"`
	RxTotal       int64   `json:"rxTotal"`
	TxTotal       int64   `json:"txTotal"`
}

// SystemStats mirrors GetStatsCommand. CPU, Memory and Uptime describe the
// panel host. Nodes.TotalOnline is users online across connected nodes, not
// a node count (see Stats.NodesOnline for that).
type SystemStats struct {
	CPU struct {
		Cores int `json:"cores"`
	} `json:"cpu"`
	Memory struct {
		Total int64 `json:"total"`
		Free  int64 `json:"free"`
		Used  int64 `json:"used"`
	} `json:"memory"`
	Uptime    float64 `json:"uptime"`
	Timestamp int64   `json:"timestamp"` // unix ms
	Users     struct {
		StatusCounts map[string]int `json:"statusCounts"` // ACTIVE, DISABLED, LIMITED, EXPIRED
		TotalUsers   int            `json:"totalUsers"`
	} `json:"users"`
	OnlineStats struct {
		LastDay     int `json:"lastDay"`
		LastWeek    int `json:"lastWeek"`
		NeverOnline int `json:"neverOnline"`
		OnlineNow   int `json:"onlineNow"`
	} `json:"onlineStats"`
	Nodes struct {
		TotalOnline        int  `json:"totalOnline"`
		TotalBytesLifetime Size `json:"totalBytesLifetime"`
	} `json:"nodes"`
}

// BaseStat is a period total next to the previous period's; Difference = Current - Previous.
type BaseStat struct {
	Current    Size `json:"current"`
	Previous   Size `json:"previous"`
	Difference Size `json:"difference"`
}

// BandwidthStats mirrors GetBandwidthStatsCommand. LastTwoDays is today vs
// yesterday; CalendarMonth and CurrentYear compare to-date against the whole
// previous month/year.
type BandwidthStats struct {
	BandwidthLastTwoDays   BaseStat `json:"bandwidthLastTwoDays"`
	BandwidthLastSevenDays BaseStat `json:"bandwidthLastSevenDays"`
	BandwidthLast30Days    BaseStat `json:"bandwidthLast30Days"`
	BandwidthCalendarMonth BaseStat `json:"bandwidthCalendarMonth"`
	BandwidthCurrentYear   BaseStat `json:"bandwidthCurrentYear"`
}

// RuntimeMetric is one panel process (instanceType api, scheduler or processor).
type RuntimeMetric struct {
	RSS              int64   `json:"rss"`
	HeapUsed         int64   `json:"heapUsed"`
	HeapTotal        int64   `json:"heapTotal"`
	External         int64   `json:"external"`
	ArrayBuffers     int64   `json:"arrayBuffers"`
	EventLoopDelayMs float64 `json:"eventLoopDelayMs"`
	EventLoopP99Ms   float64 `json:"eventLoopP99Ms"`
	ActiveHandles    int     `json:"activeHandles"`
	Uptime           float64 `json:"uptime"`
	PID              int     `json:"pid"`
	Timestamp        int64   `json:"timestamp"` // unix ms
	InstanceID       string  `json:"instanceId"`
	InstanceType     string  `json:"instanceType"`
}

type NodeMetrics struct {
	NodeUUID       string       `json:"nodeUuid"`
	NodeName       string       `json:"nodeName"`
	CountryEmoji   string       `json:"countryEmoji"`
	ProviderName   string       `json:"providerName"`
	UsersOnline    int          `json:"usersOnline"`
	InboundsStats  []TagTraffic `json:"inboundsStats"`
	OutboundsStats []TagTraffic `json:"outboundsStats"`
}

type TagTraffic struct {
	Tag      string `json:"tag"`
	Upload   Size   `json:"upload"`
	Download Size   `json:"download"`
}

type NodeDayTraffic struct {
	NodeName   string `json:"nodeName"`
	Date       string `json:"date"`
	TotalBytes Size   `json:"totalBytes"`
}

// NodesUsage mirrors GetStatsNodesUsageCommand; Data and SparklineData align with Categories (dates).
type NodesUsage struct {
	Categories    []string `json:"categories"`
	SparklineData []int64  `json:"sparklineData"`
	TopNodes      []struct {
		UUID        string `json:"uuid"`
		Color       string `json:"color"`
		Name        string `json:"name"`
		CountryCode string `json:"countryCode"`
		Total       int64  `json:"total"`
	} `json:"topNodes"`
	Series []struct {
		UUID        string  `json:"uuid"`
		Name        string  `json:"name"`
		Color       string  `json:"color"`
		CountryCode string  `json:"countryCode"`
		Total       int64   `json:"total"`
		Data        []int64 `json:"data"`
	} `json:"series"`
}

// Stats is the panel home page: SystemStats + BandwidthStats + Health, plus
// node counts derived from Nodes.
type Stats struct {
	System        SystemStats     `json:"system"`
	Bandwidth     BandwidthStats  `json:"bandwidth"`
	Runtime       []RuntimeMetric `json:"runtime"`
	NodesTotal    int             `json:"nodesTotal"`
	NodesOnline   int             `json:"nodesOnline"` // connected and enabled
	NodesDisabled int             `json:"nodesDisabled"`
}

// Size is a byte amount the contract types as a string: a raw integer
// ("123456789") or, for bandwidth and per-tag metrics, IEC text with two
// decimals ("1.50 GiB", "-512.00 MiB", "0").
type Size string

func (s *Size) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] != '"' {
		if string(b) != "null" {
			*s = Size(b)
		}
		return nil
	}
	return json.Unmarshal(b, (*string)(s))
}

var sizeRe = regexp.MustCompile(`(?i)^([+-]?\d+(?:\.\d+)?(?:e[+-]?\d+)?)\s*(?:([kmgtpezy])(i?)b|b)?$`)

// Bytes parses the size; prefixes are 1024-based with "i" (KiB) and 1000-based without (kB).
func (s Size) Bytes() (int64, error) {
	t := strings.TrimSpace(string(s))
	if n, err := strconv.ParseInt(t, 10, 64); err == nil {
		return n, nil
	}
	m := sizeRe.FindStringSubmatch(t)
	if m == nil {
		return 0, fmt.Errorf("remnawave: bad size %q", string(s))
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("remnawave: bad size %q", string(s))
	}
	if m[2] != "" {
		base := 1000.0
		if m[3] != "" {
			base = 1024
		}
		v *= math.Pow(base, float64(strings.IndexByte("kmgtpezy", strings.ToLower(m[2])[0])+1))
	}
	return int64(math.Round(v)), nil
}
