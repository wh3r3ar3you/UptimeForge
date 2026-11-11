package sysmon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCollect(t *testing.T) {
	c := New(nil)
	ctx := context.Background()
	info := c.Info(ctx)
	if info.OS == "" || info.Cores == 0 || info.MemTotal == 0 {
		t.Fatalf("info: %+v", info)
	}
	c.Collect(ctx)
	time.Sleep(200 * time.Millisecond)
	m := c.Collect(ctx)
	if m.MemTotal == 0 || m.DiskTotal == 0 || m.MemUsed > m.MemTotal || m.CPU < 0 || m.CPU > 100 {
		t.Fatalf("metrics: %+v", m)
	}
}

func TestCountable(t *testing.T) {
	c := New(nil)
	for name, want := range map[string]bool{"eth0": true, "ens3": true, "lo": false, "docker0": false, "veth12ab": false, "wg0": false} {
		if c.countable(name) != want {
			t.Errorf("%s: want %v", name, want)
		}
	}
	if !New([]string{"wg0"}).countable("wg0") {
		t.Error("explicit list must win")
	}
}

// fakeNet builds a /sys/class/net and /proc/net/vlan/config under a temp dir.
func fakeNet(t *testing.T, ifaces map[string][]string, vlans string) *Collector {
	t.Helper()
	dir := t.TempDir()
	sys := filepath.Join(dir, "sys")
	for name, entries := range ifaces {
		if err := os.MkdirAll(filepath.Join(sys, name), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			path, content, isFile := strings.Cut(e, "=")
			p := filepath.Join(sys, name, filepath.FromSlash(path))
			if isFile {
				os.MkdirAll(filepath.Dir(p), 0o755)
				if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			} else if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	vlanConf := filepath.Join(dir, "vlan-config")
	os.WriteFile(vlanConf, []byte("VLAN Dev name | VLAN ID\nName-Type: VLAN_NAME_TYPE_RAW_PLUS_VID_NO_PAD\n"+vlans), 0o644)
	return &Collector{sysNet: sys, vlanConf: vlanConf}
}

func picked(c *Collector, names ...string) []string {
	var out []string
	for n := range c.pick(names) {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// The same bytes cross a bond and its slaves, a VLAN and its parent, a PPPoE
// link and its Ethernet, and a Proxmox bridge's port: count each once.
func TestPickCountsEachByteOnce(t *testing.T) {
	c := fakeNet(t, map[string][]string{
		"eno1":      {"device"},                     // port of vmbr0
		"eno2":      {"device"},                     // slave of bond0
		"eno3":      {"device"},                     // slave of bond0
		"bond0":     {"bonding/slaves=eno2 eno3\n"}, // port of vmbr1
		"vmbr0":     {"bridge", "brif/eno1"},        // Proxmox bridge
		"vmbr1":     {"bridge", "brif/bond0"},       // bridge over the bond
		"eno1.100":  {},                             // VLAN on eno1
		"vlan200":   {},                             // VLAN named freely
		"ppp0":      {},                             // PPPoE over eno1
		"fwpr100p0": {},                             // Proxmox firewall bridge port
		"lo":        {},
	}, "eno1.100 | 100 | eno1\nvlan200 | 200 | bond0\n")
	all := []string{"eno1", "eno2", "eno3", "bond0", "vmbr0", "vmbr1", "eno1.100", "vlan200", "ppp0", "fwpr100p0", "lo", "tap100i0"}
	if got := picked(c, all...); !slices.Equal(got, []string{"bond0", "eno1"}) {
		t.Fatalf("picked %v, want [bond0 eno1]", got)
	}
	// In a container the only interface is a veth-backed eth0 without a
	// device: it still counts rather than nothing.
	c = fakeNet(t, map[string][]string{"eth0": {}, "lo": {}}, "")
	if got := picked(c, "eth0", "lo"); !slices.Equal(got, []string{"eth0"}) {
		t.Fatalf("container: picked %v", got)
	}
	// Without sysfs (Windows, macOS) the name rules stay.
	c = &Collector{sysNet: filepath.Join(t.TempDir(), "none")}
	if got := picked(c, "Ethernet", "lo", "wg0"); !slices.Equal(got, []string{"Ethernet"}) {
		t.Fatalf("no sysfs: picked %v", got)
	}
	// An explicit list wins.
	c.nics = []string{"wg0"}
	if got := picked(c, "Ethernet", "wg0"); !slices.Equal(got, []string{"wg0"}) {
		t.Fatalf("explicit: picked %v", got)
	}
}

func TestExtBreakdown(t *testing.T) {
	c := New(nil)
	time.Sleep(300 * time.Millisecond)
	m := c.Collect(context.Background())
	e := m.Ext
	if e == nil {
		t.Fatal("no ext")
	}
	sum := e.CPUUser + e.CPUSystem + e.CPUIowait + e.CPUSteal + e.CPUIRQ
	if sum < 0 || sum > 100.01 || e.Procs < 0 {
		t.Fatalf("ext: %+v", e)
	}
}

func TestWholeDisk(t *testing.T) {
	for _, n := range []string{"loop0", "dm-1", "ram0", "zram0", "sr0", "md0"} {
		if wholeDisk(n) {
			t.Errorf("%s must not count", n)
		}
	}
}

func TestRateReset(t *testing.T) {
	if rate(5, 10, 1) != 0 || rate(10, 5, 0) != 0 || rate(30, 10, 2) != 10 {
		t.Fatal("rate")
	}
}
