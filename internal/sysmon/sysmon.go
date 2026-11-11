// Package sysmon collects host metrics for the servers view: CPU, memory,
// disk, load, and traffic on the physical interfaces.
package sysmon

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"

	"github.com/wh3r3ar3you/uptimeforge/internal/proto"
)

type Collector struct {
	nics    []string // explicit interface list; empty = auto
	prev    map[string]gnet.IOCountersStat
	prevAt  time.Time
	cpuPrev *cpu.TimesStat
	disk    map[string]disk.IOCountersStat
	diskAt  time.Time

	// Where the interface layout is read from; tests point them at a fake tree.
	sysNet   string // /sys/class/net
	vlanConf string // /proc/net/vlan/config
}

// New collects traffic on nics, or on every physical-looking interface when empty.
func New(nics []string) *Collector {
	c := &Collector{nics: nics, sysNet: "/sys/class/net", vlanConf: "/proc/net/vlan/config"}
	cpu.Percent(0, false) // prime the CPU delta
	// Prime the counters too, so the first sample is a real rate and not a zero.
	c.cpuBreakdown(context.Background(), &proto.HostExt{})
	c.diskIO(context.Background(), &proto.HostExt{})
	return c
}

// Virtual and container interfaces would double count traffic that also
// crosses the physical NIC.
var skipPrefixes = []string{"lo", "docker", "veth", "br-", "virbr", "cni", "flannel", "cali", "vxlan", "tun", "tap",
	"wg", "kube", "lxc", "vnet", "dummy", "ifb", "gre", "sit", "ip6tnl", "tailscale", "zt", "awg", "nekoray", "xray"}

func (c *Collector) countable(name string) bool {
	if len(c.nics) > 0 {
		return slices.Contains(c.nics, name)
	}
	for _, p := range skipPrefixes {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	return true
}

// pick chooses the interfaces whose counters are summed. Name rules drop the
// obvious virtual ones. Where sysfs shows the layout (Linux), layered
// interfaces that carry the same bytes again are dropped too: bridges (on
// Proxmox the physical port carries the traffic, the bridge only the host's
// own), bond slaves (the bond counts), VLAN children (the parent counts), and
// then only physical NICs and bonds are kept (ppp over Ethernet, macvlan,
// firewall ports...). A container's eth0 has no device; when nothing
// physical is left, the layered-filtered set is used as it is.
func (c *Collector) pick(names []string) map[string]bool {
	out := map[string]bool{}
	for _, n := range names {
		if c.countable(n) {
			out[n] = true
		}
	}
	if len(c.nics) > 0 || c.sysNet == "" {
		return out
	}
	if _, err := os.Stat(c.sysNet); err != nil {
		return out // no sysfs: Windows, macOS
	}
	has := func(name, entry string) bool {
		_, err := os.Lstat(filepath.Join(c.sysNet, name, entry))
		return err == nil
	}
	for n := range out {
		if has(n, "bridge") {
			delete(out, n)
		}
		if b, err := os.ReadFile(filepath.Join(c.sysNet, n, "bonding", "slaves")); err == nil {
			for _, s := range strings.Fields(string(b)) {
				delete(out, s)
			}
		}
	}
	for _, v := range c.vlanChildren() {
		delete(out, v)
	}
	physical := map[string]bool{}
	for n := range out {
		if has(n, "device") || has(n, "bonding") {
			physical[n] = true
		}
	}
	if len(physical) > 0 {
		return physical
	}
	return out
}

// vlanChildren lists VLAN interfaces from /proc/net/vlan/config, whatever
// their names ("eth0.100", "vlan100").
func (c *Collector) vlanChildren() []string {
	b, err := os.ReadFile(c.vlanConf)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		name, _, ok := strings.Cut(line, "|")
		if name = strings.TrimSpace(name); ok && name != "" && name != "VLAN Dev name" {
			out = append(out, name)
		}
	}
	return out
}

func root() string {
	if runtime.GOOS == "windows" {
		return `C:\`
	}
	return "/"
}

func (c *Collector) Info(ctx context.Context) proto.SysInfo {
	si := proto.SysInfo{OS: runtime.GOOS, Arch: runtime.GOARCH}
	if h, err := host.InfoWithContext(ctx); err == nil {
		si.Hostname, si.Kernel, si.BootTime = h.Hostname, h.KernelVersion, int64(h.BootTime)
		si.Platform = strings.TrimSpace(h.Platform + " " + h.PlatformVersion)
		if h.VirtualizationRole == "guest" {
			si.Virtualization = h.VirtualizationSystem
		}
	}
	if infos, err := cpu.InfoWithContext(ctx); err == nil && len(infos) > 0 {
		si.CPUModel = strings.TrimSpace(infos[0].ModelName)
	}
	si.Cores, _ = cpu.CountsWithContext(ctx, true)
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		si.MemTotal = vm.Total
	}
	if ios, err := gnet.IOCountersWithContext(ctx, true); err == nil {
		use := c.pick(ioNames(ios))
		for _, io := range ios {
			if use[io.Name] {
				si.NICs = append(si.NICs, io.Name)
				si.LinkMbps = max(si.LinkMbps, linkSpeed(io.Name))
			}
		}
	}
	return si
}

func (c *Collector) Collect(ctx context.Context) proto.Metrics {
	var m proto.Metrics
	if p, err := cpu.PercentWithContext(ctx, 0, false); err == nil && len(p) > 0 {
		m.CPU = p[0]
	}
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		m.MemUsed, m.MemTotal = vm.Used, vm.Total
	}
	if sw, err := mem.SwapMemoryWithContext(ctx); err == nil {
		m.SwapUsed = sw.Used
	}
	if d, err := disk.UsageWithContext(ctx, root()); err == nil {
		m.DiskUsed, m.DiskTotal = d.Used, d.Total
	}
	if l, err := load.AvgWithContext(ctx); err == nil {
		m.Load1, m.Load5, m.Load15 = l.Load1, l.Load5, l.Load15
	}
	if up, err := host.UptimeWithContext(ctx); err == nil {
		m.Uptime = up
	}
	m.TCP = tcpInUse()

	ext := &proto.HostExt{}
	c.cpuBreakdown(ctx, ext)
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		ext.MemCache = vm.Buffers + vm.Cached
	}
	if sw, err := mem.SwapMemoryWithContext(ctx); err == nil {
		ext.SwapTotal = sw.Total
	}
	if misc, err := load.MiscWithContext(ctx); err == nil {
		ext.Procs, ext.ProcsD = misc.ProcsTotal, misc.ProcsBlocked
	}
	c.diskIO(ctx, ext)
	m.Ext = ext

	now := time.Now()
	if ios, err := gnet.IOCountersWithContext(ctx, true); err == nil {
		cur := map[string]gnet.IOCountersStat{}
		use := c.pick(ioNames(ios))
		for _, io := range ios {
			if use[io.Name] {
				cur[io.Name] = io
			}
		}
		if dt := now.Sub(c.prevAt).Seconds(); c.prev != nil && dt > 0 {
			for name, io := range cur {
				if p, ok := c.prev[name]; ok && io.BytesRecv >= p.BytesRecv && io.BytesSent >= p.BytesSent {
					m.RX += float64(io.BytesRecv-p.BytesRecv) / dt
					m.TX += float64(io.BytesSent-p.BytesSent) / dt
					ext.RXPkts += rate(io.PacketsRecv, p.PacketsRecv, dt)
					ext.TXPkts += rate(io.PacketsSent, p.PacketsSent, dt)
					ext.NetErr += rate(io.Errin+io.Errout+io.Dropin+io.Dropout, p.Errin+p.Errout+p.Dropin+p.Dropout, dt)
				}
			}
		}
		c.prev, c.prevAt = cur, now
	}
	return m
}

func ioNames(ios []gnet.IOCountersStat) []string {
	out := make([]string, len(ios))
	for i, io := range ios {
		out[i] = io.Name
	}
	return out
}

func linkSpeed(nic string) int {
	b, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/speed", nic))
	if err != nil {
		return 0
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || v <= 0 {
		return 0
	}
	return v
}

// tcpInUse reads the kernel's socket counters: cheap even with 100k
// connections, unlike enumerating them.
func tcpInUse() int {
	total := 0
	for _, f := range []string{"/proc/net/sockstat", "/proc/net/sockstat6"} {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) >= 3 && (fields[0] == "TCP:" || fields[0] == "TCP6:") && fields[1] == "inuse" {
				n, _ := strconv.Atoi(fields[2])
				total += n
			}
		}
		fh.Close()
	}
	return total
}

// rate is a counter's change per second; a reset (reboot, driver reload) counts as zero.
func rate(cur, prev uint64, dt float64) float64 {
	if cur < prev || dt <= 0 {
		return 0
	}
	return float64(cur-prev) / dt
}

// cpuBreakdown splits CPU time since the previous call, the way top and Zabbix
// do. Steal is time the hypervisor gave to another guest: on a VPS it is the
// one number that says "the neighbour, not us".
func (c *Collector) cpuBreakdown(ctx context.Context, ext *proto.HostExt) {
	ts, err := cpu.TimesWithContext(ctx, false)
	if err != nil || len(ts) == 0 {
		return
	}
	cur := ts[0]
	prev := c.cpuPrev
	c.cpuPrev = &cur
	if prev == nil {
		return
	}
	busy := func(t *cpu.TimesStat) (user, sys, iowait, steal, irq, total float64) {
		user, sys, iowait, steal, irq = t.User+t.Nice, t.System, t.Iowait, t.Steal, t.Irq+t.Softirq
		return user, sys, iowait, steal, irq, user + sys + iowait + steal + irq + t.Idle
	}
	u1, s1, w1, st1, i1, t1 := busy(&cur)
	u0, s0, w0, st0, i0, t0 := busy(prev)
	dt := t1 - t0
	if dt <= 0 {
		return
	}
	pct := func(a, b float64) float64 { return max(0, min(100, (a-b)/dt*100)) }
	ext.CPUUser, ext.CPUSystem, ext.CPUIowait, ext.CPUSteal, ext.CPUIRQ = pct(u1, u0), pct(s1, s0), pct(w1, w0), pct(st1, st0), pct(i1, i0)
}

// wholeDisk keeps physical block devices and drops partitions, loop, RAM and
// device-mapper layers, which would count the same bytes twice.
func wholeDisk(name string) bool {
	for _, p := range []string{"loop", "ram", "zram", "sr", "fd", "dm-", "md", "nbd"} {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	if runtime.GOOS != "linux" {
		return true
	}
	_, err := os.Stat("/sys/block/" + name)
	return err == nil
}

func (c *Collector) diskIO(ctx context.Context, ext *proto.HostExt) {
	ios, err := disk.IOCountersWithContext(ctx)
	if err != nil {
		return
	}
	now := time.Now()
	cur := map[string]disk.IOCountersStat{}
	for name, io := range ios {
		if wholeDisk(name) {
			cur[name] = io
		}
	}
	if dt := now.Sub(c.diskAt).Seconds(); c.disk != nil && dt > 0 {
		for name, io := range cur {
			p, ok := c.disk[name]
			if !ok {
				continue
			}
			ext.DiskRead += rate(io.ReadBytes, p.ReadBytes, dt)
			ext.DiskWrite += rate(io.WriteBytes, p.WriteBytes, dt)
			ext.DiskROps += rate(io.ReadCount, p.ReadCount, dt)
			ext.DiskWOps += rate(io.WriteCount, p.WriteCount, dt)
			ext.DiskUtil = max(ext.DiskUtil, min(100, rate(io.IoTime, p.IoTime, dt)/10)) // ms busy per s -> %
		}
	}
	c.disk, c.diskAt = cur, now
}
