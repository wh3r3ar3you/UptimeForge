package store

import "fmt"

// sysCols is the host detail added in schema 2. The order is the order of
// SysExt.values. Never edit or reorder it: a new metric needs a new
// migration and its own list.
var sysCols = []struct {
	name string
	max  bool // also keep the peak of each bucket
}{
	{"cpu_user", false}, {"cpu_system", false}, {"cpu_iowait", true}, {"cpu_steal", true}, {"cpu_irq", false},
	{"mem_cache", false}, {"swap_used", true}, {"swap_total", false},
	{"disk_read", true}, {"disk_write", true}, {"disk_rops", true}, {"disk_wops", true}, {"disk_util", true},
	{"rx_pkts", true}, {"tx_pkts", true}, {"net_err", true},
	{"tcp", true}, {"procs", true}, {"procs_blocked", true},
}

// SysExt is the detail behind the headline host metrics. Nil on a sample
// means the agent is too old to send it, which is not the same as zero.
type SysExt struct {
	CPUUser, CPUSystem, CPUIowait, CPUSteal, CPUIRQ float64 // percent
	MemCache, SwapUsed, SwapTotal                   float64 // bytes
	DiskRead, DiskWrite, DiskROps, DiskWOps         float64 // per second
	DiskUtil                                        float64 // percent
	RXPkts, TXPkts, NetErr                          float64 // per second
	TCP, Procs, ProcsBlocked                        float64
}

func (e *SysExt) values() []float64 {
	return []float64{e.CPUUser, e.CPUSystem, e.CPUIowait, e.CPUSteal, e.CPUIRQ, e.MemCache, e.SwapUsed, e.SwapTotal,
		e.DiskRead, e.DiskWrite, e.DiskROps, e.DiskWOps, e.DiskUtil, e.RXPkts, e.TXPkts, e.NetErr, e.TCP, e.Procs, e.ProcsBlocked}
}

// sysExtMigration is schema 2. Rollups keep a sum per column and a count of
// the rows that had the detail at all (n2): a bucket where the agent was
// upgraded halfway must not be averaged over rows that never carried the
// value. It is a literal on purpose: an applied migration never changes, so
// it must not be built from a list someone may edit.
const sysExtMigration = `
ALTER TABLE sys ADD COLUMN cpu_user DOUBLE PRECISION, ADD COLUMN cpu_system DOUBLE PRECISION,
	ADD COLUMN cpu_iowait DOUBLE PRECISION, ADD COLUMN cpu_steal DOUBLE PRECISION, ADD COLUMN cpu_irq DOUBLE PRECISION,
	ADD COLUMN mem_cache DOUBLE PRECISION, ADD COLUMN swap_used DOUBLE PRECISION, ADD COLUMN swap_total DOUBLE PRECISION,
	ADD COLUMN disk_read DOUBLE PRECISION, ADD COLUMN disk_write DOUBLE PRECISION, ADD COLUMN disk_rops DOUBLE PRECISION,
	ADD COLUMN disk_wops DOUBLE PRECISION, ADD COLUMN disk_util DOUBLE PRECISION, ADD COLUMN rx_pkts DOUBLE PRECISION,
	ADD COLUMN tx_pkts DOUBLE PRECISION, ADD COLUMN net_err DOUBLE PRECISION, ADD COLUMN tcp DOUBLE PRECISION,
	ADD COLUMN procs DOUBLE PRECISION, ADD COLUMN procs_blocked DOUBLE PRECISION;
ALTER TABLE sys_5m ADD COLUMN n2 INTEGER, ADD COLUMN cpu_user_sum DOUBLE PRECISION, ADD COLUMN cpu_system_sum DOUBLE PRECISION,
	ADD COLUMN cpu_iowait_sum DOUBLE PRECISION, ADD COLUMN cpu_iowait_max DOUBLE PRECISION, ADD COLUMN cpu_steal_sum DOUBLE PRECISION,
	ADD COLUMN cpu_steal_max DOUBLE PRECISION, ADD COLUMN cpu_irq_sum DOUBLE PRECISION, ADD COLUMN mem_cache_sum DOUBLE PRECISION,
	ADD COLUMN swap_used_sum DOUBLE PRECISION, ADD COLUMN swap_used_max DOUBLE PRECISION, ADD COLUMN swap_total_sum DOUBLE PRECISION,
	ADD COLUMN disk_read_sum DOUBLE PRECISION, ADD COLUMN disk_read_max DOUBLE PRECISION, ADD COLUMN disk_write_sum DOUBLE PRECISION,
	ADD COLUMN disk_write_max DOUBLE PRECISION, ADD COLUMN disk_rops_sum DOUBLE PRECISION, ADD COLUMN disk_rops_max DOUBLE PRECISION,
	ADD COLUMN disk_wops_sum DOUBLE PRECISION, ADD COLUMN disk_wops_max DOUBLE PRECISION, ADD COLUMN disk_util_sum DOUBLE PRECISION,
	ADD COLUMN disk_util_max DOUBLE PRECISION, ADD COLUMN rx_pkts_sum DOUBLE PRECISION, ADD COLUMN rx_pkts_max DOUBLE PRECISION,
	ADD COLUMN tx_pkts_sum DOUBLE PRECISION, ADD COLUMN tx_pkts_max DOUBLE PRECISION, ADD COLUMN net_err_sum DOUBLE PRECISION,
	ADD COLUMN net_err_max DOUBLE PRECISION, ADD COLUMN tcp_sum DOUBLE PRECISION, ADD COLUMN tcp_max DOUBLE PRECISION,
	ADD COLUMN procs_sum DOUBLE PRECISION, ADD COLUMN procs_max DOUBLE PRECISION, ADD COLUMN procs_blocked_sum DOUBLE PRECISION,
	ADD COLUMN procs_blocked_max DOUBLE PRECISION;
ALTER TABLE sys_1h ADD COLUMN n2 INTEGER, ADD COLUMN cpu_user_sum DOUBLE PRECISION, ADD COLUMN cpu_system_sum DOUBLE PRECISION,
	ADD COLUMN cpu_iowait_sum DOUBLE PRECISION, ADD COLUMN cpu_iowait_max DOUBLE PRECISION, ADD COLUMN cpu_steal_sum DOUBLE PRECISION,
	ADD COLUMN cpu_steal_max DOUBLE PRECISION, ADD COLUMN cpu_irq_sum DOUBLE PRECISION, ADD COLUMN mem_cache_sum DOUBLE PRECISION,
	ADD COLUMN swap_used_sum DOUBLE PRECISION, ADD COLUMN swap_used_max DOUBLE PRECISION, ADD COLUMN swap_total_sum DOUBLE PRECISION,
	ADD COLUMN disk_read_sum DOUBLE PRECISION, ADD COLUMN disk_read_max DOUBLE PRECISION, ADD COLUMN disk_write_sum DOUBLE PRECISION,
	ADD COLUMN disk_write_max DOUBLE PRECISION, ADD COLUMN disk_rops_sum DOUBLE PRECISION, ADD COLUMN disk_rops_max DOUBLE PRECISION,
	ADD COLUMN disk_wops_sum DOUBLE PRECISION, ADD COLUMN disk_wops_max DOUBLE PRECISION, ADD COLUMN disk_util_sum DOUBLE PRECISION,
	ADD COLUMN disk_util_max DOUBLE PRECISION, ADD COLUMN rx_pkts_sum DOUBLE PRECISION, ADD COLUMN rx_pkts_max DOUBLE PRECISION,
	ADD COLUMN tx_pkts_sum DOUBLE PRECISION, ADD COLUMN tx_pkts_max DOUBLE PRECISION, ADD COLUMN net_err_sum DOUBLE PRECISION,
	ADD COLUMN net_err_max DOUBLE PRECISION, ADD COLUMN tcp_sum DOUBLE PRECISION, ADD COLUMN tcp_max DOUBLE PRECISION,
	ADD COLUMN procs_sum DOUBLE PRECISION, ADD COLUMN procs_max DOUBLE PRECISION, ADD COLUMN procs_blocked_sum DOUBLE PRECISION,
	ADD COLUMN procs_blocked_max DOUBLE PRECISION;
`

// sysRollupCols names the rollup columns and the aggregates that fill them
// from raw rows, in the same order.
func sysRollupCols() (cols, aggs []string) {
	cols, aggs = []string{"n2"}, []string{"COUNT(cpu_user)"}
	for _, c := range sysCols {
		cols, aggs = append(cols, c.name+"_sum"), append(aggs, "SUM("+c.name+")")
		if c.max {
			cols, aggs = append(cols, c.name+"_max"), append(aggs, "MAX("+c.name+")")
		}
	}
	return cols, aggs
}

// sysReadExprs reads the detail back per chart bucket, with the JSON key of
// each value: from raw rows as plain AVG/MAX, from rollups weighted by n2.
func sysReadExprs(raw bool) (exprs, keys []string) {
	for _, c := range sysCols {
		if raw {
			exprs = append(exprs, "AVG("+c.name+")")
		} else {
			exprs = append(exprs, fmt.Sprintf("SUM(%s_sum) / NULLIF(SUM(n2), 0)", c.name))
		}
		keys = append(keys, c.name)
		if c.max {
			if raw {
				exprs = append(exprs, "MAX("+c.name+")")
			} else {
				exprs = append(exprs, "MAX("+c.name+"_max)")
			}
			keys = append(keys, c.name+"_max")
		}
	}
	return exprs, keys
}
