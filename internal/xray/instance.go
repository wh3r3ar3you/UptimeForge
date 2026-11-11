package xray

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"

	xnet "github.com/xtls/xray-core/common/net"
	xcore "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf/serial"
	_ "github.com/xtls/xray-core/main/distro/all" // registers every protocol and transport
)

// Instance is a running in-process xray-core with no inbounds; connections are
// opened with DialContext and leave through the first outbound.
type Instance struct {
	core *xcore.Instance
}

// Start launches xray-core with target as the default outbound. When via is
// set, target is chained through it (dialerProxy), which is how probes that
// live behind an upstream proxy run their checks. target may be nil to get a
// plain dialer through via.
func Start(target, via Outbound, logLevel string) (*Instance, error) {
	cfg, err := BuildConfig(target, via, logLevel)
	if err != nil {
		return nil, err
	}
	pb, err := serial.LoadJSONConfig(bytes.NewReader(cfg))
	if err != nil {
		return nil, fmt.Errorf("xray config: %w", err)
	}
	inst, err := xcore.New(pb)
	if err != nil {
		return nil, fmt.Errorf("xray init: %w", err)
	}
	if err := inst.Start(); err != nil {
		inst.Close()
		return nil, fmt.Errorf("xray start: %w", err)
	}
	return &Instance{core: inst}, nil
}

// BuildConfig renders the Xray JSON config used by Start.
func BuildConfig(target, via Outbound, logLevel string) ([]byte, error) {
	if target == nil && via == nil {
		return nil, fmt.Errorf("no outbound")
	}
	if logLevel == "" {
		logLevel = "none"
	}
	var outbounds []any
	if target != nil {
		t, err := clone(target)
		if err != nil {
			return nil, err
		}
		t["tag"] = "target"
		delete(t, "mux")
		if via != nil {
			ss, _ := t["streamSettings"].(map[string]any)
			if ss == nil {
				ss = map[string]any{}
				t["streamSettings"] = ss
			}
			so, _ := ss["sockopt"].(map[string]any)
			if so == nil {
				so = map[string]any{}
				ss["sockopt"] = so
			}
			so["dialerProxy"] = "via"
		}
		outbounds = append(outbounds, t)
	}
	if via != nil {
		v, err := clone(via)
		if err != nil {
			return nil, err
		}
		v["tag"] = "via"
		outbounds = append(outbounds, v)
	}
	return json.Marshal(map[string]any{
		"log":       map[string]any{"loglevel": logLevel},
		"outbounds": outbounds,
	})
}

func clone(o Outbound) (Outbound, error) {
	b, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	var c Outbound
	return c, json.Unmarshal(b, &c)
}

// DialContext opens a TCP or UDP stream through the instance.
func (i *Instance) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	dest, err := xnet.ParseDestination(network + ":" + addr)
	if err != nil {
		return nil, err
	}
	conn, err := xcore.Dial(ctx, i.core, dest)
	if err != nil {
		return nil, err
	}
	// xray connections ignore deadlines; tie their lifetime to ctx instead.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	return &ctxConn{Conn: conn, stop: stop}, nil
}

func (i *Instance) Close() error { return i.core.Close() }

type ctxConn struct {
	net.Conn
	stop func() bool
}

func (c *ctxConn) Close() error {
	c.stop()
	return c.Conn.Close()
}

// StartServer runs xray-core from a full JSON config (tests and the demo use
// it for in-process servers).
func StartServer(config string) (*Instance, error) {
	pb, err := serial.LoadJSONConfig(bytes.NewReader([]byte(config)))
	if err != nil {
		return nil, err
	}
	inst, err := xcore.New(pb)
	if err != nil {
		return nil, err
	}
	if err := inst.Start(); err != nil {
		return nil, err
	}
	return &Instance{core: inst}, nil
}
