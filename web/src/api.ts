import { QueryClient, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useRef, useState } from 'react';

// ---- Types mirroring the hub's JSON ----

export type Kind = 'ok' | 'unknown' | 'partial' | 'blocked' | 'down' | 'probe_offline' | 'probe_no_net' | 'host' | 'users_drop' | 'node_down' | 'agent_update' | 'paused';
export type Severity = 'critical' | 'warning' | 'info';

export interface ProbeLine {
  probe: string; name: string; region?: string; ok: boolean; reason?: string; error?: string; latency_ms?: number; bytes?: number;
}

export interface Incident {
  id: string; key: string; name: string; group?: string; type?: string; target?: string;
  kind: Kind; worst: Kind; region?: string; severity: Severity; lines?: ProbeLine[]; note?: string;
  peak_failing?: number; peak_total?: number;
  started: string; updated: string; resolved?: string;
  ack_by?: string; muted_until?: string; muted_by?: string; notified?: boolean;
  /** The incident that explains this one (it was not announced), and what an incident took down with it. */
  cause?: string; affected?: string[];
  /** Host threshold incidents: the metric, its latest and worst value, and the rule. */
  host?: { metric: HostMetric; value: number; peak: number; above: number; for_s?: number };
  /** Users online collapse on a Remnawave node: the usual level, now, the lowest seen. */
  drop?: { before: number; now: number; low: number; connected: boolean };
}

export type HostMetric = 'cpu' | 'mem' | 'disk' | 'swap' | 'steal' | 'iowait' | 'load';
export interface HostRule { metric: HostMetric; above: number; for?: string; off?: boolean }

export interface ProbeCell {
  probe: string; failing: boolean; status?: 'up' | 'down'; latency?: number; ttfb?: number; bps?: number;
  reason?: string; error?: string; at?: string; uptime_24h?: number; dc_ms?: string; dc_down?: string;
}

export interface CheckRow {
  id: string; name: string; group?: string; tags?: string[]; parents?: string[]; parents_auto?: boolean; type: string; target?: string; parent?: string; interval?: string;
  verdict: Kind; region?: string; paused?: boolean; managed?: boolean; subscription?: boolean; children?: number;
  probes: ProbeCell[]; uptime_24h?: number; latency_24h?: number; incident?: Incident;
}

export interface SysInfo {
  hostname: string; os: string; platform?: string; kernel?: string; arch: string; cpu_model?: string; cores?: number;
  mem_total?: number; boot_time?: number; nics?: string[]; link_mbps?: number; virtualization?: string;
}

export interface Metrics {
  cpu: number; mem_used: number; mem_total: number; swap_used?: number; disk_used: number; disk_total: number;
  rx: number; tx: number; load1: number; load5: number; load15: number; uptime: number; tcp?: number; ext?: HostExt;
}

/** Detail sent by agents from this version on; absent from older ones. */
export interface HostExt {
  cpu_user: number; cpu_system: number; cpu_iowait: number; cpu_steal: number; cpu_irq: number; mem_cache: number; swap_total: number;
  disk_read: number; disk_write: number; disk_rops: number; disk_wops: number; disk_util: number;
  rx_pkts: number; tx_pkts: number; net_err: number; procs: number; procs_blocked: number;
}

export interface ProbeStatus {
  id: string; name: string; labels: Record<string, string>; region?: string; mode: 'agent' | 'via' | 'local';
  online: boolean; internet: boolean; since: string; version?: string; host?: string;
  sysinfo?: SysInfo; metrics?: Metrics; metrics_at?: string; checks: number; failing: number;
  /** Where an agent connects from; the address only for admins. */
  network?: NetworkView;
}

export interface NetworkView { ip?: string; country?: string; asn?: string; org?: string; isp?: string; at: string }

export interface Spec {
  id: string; type: string; interval?: string; timeout?: string;
  http?: { url?: string; method?: string; headers?: Record<string, string>; expect_status?: number[]; keyword?: string; insecure?: boolean };
  tcp?: { address: string };
  tls?: { address: string; server_name?: string; min_days?: number };
  dns?: { name: string; server?: string; expect?: string };
  xray?: {
    link?: string; outbound?: Record<string, unknown>; subscription?: string; user_agent?: string; filter?: string;
    url?: string; download_url?: string; download_bytes?: number; heavy_every?: number; stall_timeout?: string;
    egress?: boolean; expect_egress?: string[];
  };
  mtproto?: { link?: string; address?: string; secret?: string; dc?: number };
  params?: Record<string, string>; keyword?: string; keyword_invert?: boolean;
}

export interface CheckConfig extends Spec {
  name?: string; group?: string; tags?: string[]; depends_on?: string[]; probes?: string[]; exclude?: string[]; retries?: number; min_failing?: number;
  severity?: '' | 'critical' | 'warning'; refresh?: string; paused?: boolean;
}

export interface CatalogField {
  name: string; kind: 'text' | 'password' | 'number' | 'bool' | 'select' | 'textarea'; required?: boolean; secret?: boolean;
  default?: string; options?: string[]; advanced?: boolean; show_if?: string;
}
export interface CatalogEntry {
  type: string; preset?: string; category: string; fields?: CatalogField[]; keyword?: boolean; hub_only?: boolean; needs?: string;
}

export interface TelegramRoute { off?: boolean; chat_id?: number; topic?: number; silent?: boolean }
export interface TelegramRule { tags?: string[]; groups?: string[]; events?: string[]; min_severity?: 'critical' | 'warning'; route: TelegramRoute }

export interface ProbeConfig {
  id: string; name: string; labels?: Record<string, string>; concurrency?: number; baseline?: Spec[];
  via?: string; local?: boolean; probing?: boolean;
  /** '' or 'server': our infrastructure; 'checker': a volunteer probe point. Agents only. */
  role?: '' | 'server' | 'checker';
  /** A checker that may run checks carrying secrets (VPN links, passwords). */
  trusted?: boolean;
  /** Labels set by hand: the network lookup leaves them alone. */
  labels_locked?: string[];
}

/** isChecker: a volunteer probe point, not our infrastructure. */
export const isChecker = (p: { config?: ProbeConfig; labels?: Record<string, string> }) =>
  p.config?.role === 'checker' || p.labels?.role === 'checker';

export interface ProbeRow extends ProbeStatus {
  config: ProbeConfig; managed: boolean;
  /** Bytes moved over the last day, from stored metrics. */
  traffic_24h?: { rx: number; tx: number };
}

/** An invite code for a checker agent; the code itself is shown once, on creation. */
export interface Invite { id: string; name?: string; trusted: boolean; created: string; expires: string; used: boolean }
export interface InviteCreated { invite: Invite; code: string; command: string; expires: string; warning?: string }

export interface Install { token: string; compose: string; compose_cmd: string; script: string; docker: string; binary: string; warning?: string }

export interface Point { t: number; n: number; up: number; lat?: number; lat_min?: number; lat_max?: number; ttfb?: number; bps?: number }

export interface SysPoint {
  t: number; cpu: number; cpu_max: number; mem: number; mem_total: number; disk: number; disk_total: number;
  rx: number; rx_max: number; tx: number; tx_max: number; load1: number; load5: number; load15: number;
  /** Detail columns and their "<name>_max" peaks; missing where old agents sent none. */
  x?: Record<string, number>;
}

export interface Spark { t: number[]; cpu: number[]; rx: number[]; tx: number[] }

export interface UptimeDay { day: string; up: number | null; worst?: Kind; down_s: number; degraded_s: number; incidents: number }

export interface BaseStat { current: string; previous: string; difference: string }

export interface RemnawaveNode {
  uuid: string; name: string; address: string; port: number | null; isConnected: boolean; isDisabled: boolean; isConnecting: boolean;
  lastStatusChange: string | null; lastStatusMessage: string | null; trafficLimitBytes: number | null; trafficUsedBytes: number | null;
  countryCode: string; usersOnline: number; xrayUptime: number; versions: { xray: string; node: string } | null;
  system: {
    info: { cpus: number; cpuModel: string; memoryTotal: number; hostname: string };
    stats: { memoryFree: number; memoryUsed: number; uptime: number; loadAvg: number[];
      interface: { interface: string; rxBytesPerSec: number; txBytesPerSec: number } | null };
  } | null;
  provider: { name: string } | null; tags: string[];
}

export interface RemnawaveView {
  configured: boolean; fetched?: string; error?: string;
  stats?: {
    system: {
      cpu: { cores: number }; memory: { total: number; free: number; used: number }; uptime: number;
      users: { statusCounts: Record<string, number>; totalUsers: number };
      onlineStats: { lastDay: number; lastWeek: number; neverOnline: number; onlineNow: number };
      nodes: { totalOnline: number; totalBytesLifetime: string };
    };
    bandwidth: {
      bandwidthLastTwoDays: BaseStat; bandwidthLastSevenDays: BaseStat; bandwidthLast30Days: BaseStat;
      bandwidthCalendarMonth: BaseStat; bandwidthCurrentYear: BaseStat;
    };
    runtime: { rss: number; heapUsed: number; eventLoopDelayMs: number; instanceType: string; instanceId: string }[] | null;
    nodesTotal: number; nodesOnline: number; nodesDisabled: number;
  };
  nodes?: RemnawaveNode[];
  usage?: { categories: string[]; topNodes: { uuid: string; name: string; countryCode: string; total: number }[] };
}

export interface Overview {
  time: string; counts: Record<string, number>; checks: CheckRow[]; probes: ProbeStatus[];
  incidents: Incident[]; recent: Incident[]; traffic: { rx: number; tx: number }; remnawave: RemnawaveView;
}

export interface HubConfig {
  public_url?: string; timezone?: string; lang?: string; region_label?: string; offline_grace?: string;
  heartbeat: { url?: string; interval?: string };
  defaults: { interval?: string; timeout?: string; retries?: number };
  baseline: Spec[] | null;
  alerts: { quiet_hours?: string; remind_every?: string; host?: HostRule[]; report?: { off?: boolean; day?: string; at?: string } };
  templates?: Record<string, Record<string, string>>;
  telegram?: { token: string; chat_id: number; topics?: Record<string, number>; routes?: Record<string, TelegramRoute>; rules?: TelegramRule[]; api_url?: string; proxy?: string; admins?: number[] } | null;
  webhook?: { url: string; headers?: Record<string, string>; secret?: string } | null;
  remnawave?: { url: string; token: string; headers?: Record<string, string> ; drop?: { off?: boolean; percent?: number; min_users?: number; window?: string }; prune?: boolean } | null;
}

export interface Settings { config: HubConfig; managed: Record<string, boolean> }

export interface User { id: number; username: string; role: 'admin' | 'viewer'; created: string }

export interface Meta {
  version: string; time: string; timezone: string; lang: string; region_label: string; public_url: string;
  check_types: string[]; check_catalog: CatalogEntry[]; user: User;
  /** The built-in server thresholds; settings override them metric by metric. */
  host_rules: HostRule[];
}

export interface RunOutcome { probe: string; name: string; region?: string; result?: Result; error?: string }
export interface Result {
  check: string; status: 'up' | 'down'; reason?: string; error?: string; latency_ms: number;
  phases?: Record<string, number>; bytes?: number; bps?: number; heavy?: boolean; info?: Record<string, string>;
}
export interface RunRow { name: string; target: string; type: string; outcomes: RunOutcome[]; error?: string }

export interface Location { node: string; name?: string; country?: string; hidden?: boolean }
export interface Locations { mode?: 'country' | 'node' | 'country_expand'; items?: Location[]; uptime?: boolean }
export interface StatusPage {
  slug: string; title: string; description?: string; groups: { name: string; checks: string[] }[];
  kind?: 'groups' | 'locations'; locations?: Locations;
}

// ---- Transport ----

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

export async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let data: unknown = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    /* not JSON */
  }
  if (!res.ok) {
    const msg = (data as { error?: string } | null)?.error ?? `HTTP ${res.status}`;
    if (res.status === 401 && !path.startsWith('/api/v1/auth/')) {
      queryClient.invalidateQueries({ queryKey: ['auth'] });
    }
    throw new ApiError(res.status, msg);
  }
  return data as T;
}

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: { retry: (n, e) => !(e instanceof ApiError && e.status < 500) && n < 2, refetchOnWindowFocus: true, staleTime: 3000 },
  },
});

export const enc = encodeURIComponent;
/** How often a page that shows live state polls. */
const LIVE = 15_000;

/**
 * Live is how a query keeps up: polled on the page that shows it, or taken
 * from the cache (whoever else polls it keeps it fresh) where it only names
 * things, such as dependency labels.
 */
export type Live = { live?: boolean };
const cadence = (o: Live | undefined, every: number) =>
  o?.live === false ? { refetchInterval: false as const, staleTime: 60_000 } : { refetchInterval: every };

// ---- Queries ----

export const useAuth = () =>
  useQuery({ queryKey: ['auth'], queryFn: () => api<{ setup_required: boolean; user?: User }>('GET', '/api/v1/auth/state') });
export const useMeta = () => useQuery({ queryKey: ['meta'], queryFn: () => api<Meta>('GET', '/api/v1/meta') });
/** useIsAdmin: viewers see the same pages without the buttons that change things. */
export const useIsAdmin = () => useMeta().data?.user.role === 'admin';
const overviewQuery = { queryKey: ['overview'], queryFn: () => api<Overview>('GET', '/api/v1/overview') };
export const useOverview = () => useQuery({ ...overviewQuery, refetchInterval: LIVE });
/**
 * useOverviewCounts feeds the navigation badge on every page. It polls slowly
 * and shares the overview query: on the dashboard, whose own observer polls
 * faster, it costs nothing extra; select keeps the layout from re-rendering
 * when only the rest of the overview changed.
 */
export const useOverviewCounts = () =>
  useQuery({ ...overviewQuery, refetchInterval: 30_000, select: (d: Overview) => d.counts });
/** Bar is one bucket of a check's pulse (see /api/v1/checks/bars). */
export interface Bar { n: number; up: number; worst?: string }

export const useCheckBars = () =>
  useQuery({
    queryKey: ['check-bars'],
    queryFn: () => api<{ from: number; to: number; step_s: number; bars: Record<string, Bar[]> }>('GET', '/api/v1/checks/bars?window=24h&buckets=48'),
    refetchInterval: 60_000,
  });

export const useChecks = (o?: Live) =>
  useQuery({
    queryKey: ['checks'],
    queryFn: () => api<{ checks: CheckRow[]; probes: ProbeStatus[] }>('GET', '/api/v1/checks'),
    ...cadence(o, LIVE),
  });
export const useCheck = (id: string) =>
  useQuery({
    queryKey: ['check', id],
    queryFn: () =>
      api<{ check: CheckRow | null; config: CheckConfig; managed: boolean; children: CheckRow[]; incidents: Incident[]; probes: ProbeStatus[] }>(
        'GET', `/api/v1/checks/${enc(id)}`),
    refetchInterval: LIVE,
  });
export const useCheckSeries = (id: string, q: string) =>
  useQuery({
    queryKey: ['series', id, q],
    queryFn: () => api<{ from: number; to: number; probes: Record<string, Point[]> }>('GET', `/api/v1/checks/${enc(id)}/series?${q}`),
    refetchInterval: q.startsWith('from=') ? false : 30_000,
    placeholderData: (prev) => prev,
  });
export const useUptime = (id: string, days = 90) =>
  useQuery({ queryKey: ['uptime', id, days], queryFn: () => api<UptimeDay[]>('GET', `/api/v1/checks/${enc(id)}/uptime?days=${days}`), refetchInterval: 60_000 });
export const useProbes = (o?: Live) =>
  useQuery({ queryKey: ['probes'], queryFn: () => api<{ probes: ProbeRow[]; hub_url: string }>('GET', '/api/v1/probes'), ...cadence(o, LIVE) });
/** q is a window from useWindow: "range=24h" or "from=…&to=…". */
export const useProbeMetrics = (id: string, q: string, enabled = true) =>
  useQuery({
    enabled,
    queryKey: ['probe-metrics', id, q],
    queryFn: () => api<{ from: number; to: number; points: SysPoint[] | null }>('GET', `/api/v1/probes/${enc(id)}/metrics?${q}`),
    refetchInterval: q.startsWith('from=') ? false : 30_000,
    placeholderData: (prev) => prev,
  });

/** One event of /probes/{id}/live: the agent's metrics, taken at `at` (unix ms, hub clock). */
export interface LivePoint extends Metrics { at: number; age?: number }

/**
 * sysPoint gives a live point the shape of a stored one, with the columns the
 * hub stores (app/recorder.go sysExt), so the charts map both the same way.
 * One sample is its own average and its own peak.
 */
export function sysPoint(m: LivePoint): SysPoint {
  const e = m.ext;
  return {
    t: m.at / 1000, cpu: m.cpu, cpu_max: m.cpu, mem: m.mem_used, mem_total: m.mem_total, disk: m.disk_used, disk_total: m.disk_total,
    rx: m.rx, rx_max: m.rx, tx: m.tx, tx_max: m.tx, load1: m.load1, load5: m.load5, load15: m.load15,
    x: e && {
      cpu_user: e.cpu_user, cpu_system: e.cpu_system, cpu_iowait: e.cpu_iowait, cpu_steal: e.cpu_steal, cpu_irq: e.cpu_irq,
      mem_cache: e.mem_cache, swap_used: m.swap_used ?? 0, swap_total: e.swap_total,
      disk_read: e.disk_read, disk_write: e.disk_write, disk_rops: e.disk_rops, disk_wops: e.disk_wops, disk_util: e.disk_util,
      rx_pkts: e.rx_pkts, tx_pkts: e.tx_pkts, net_err: e.net_err, tcp: m.tcp ?? 0, procs: e.procs, procs_blocked: e.procs_blocked,
    },
  };
}

/** The live window: this many points, a minute at the live rate. */
export const LIVE_POINTS = 60;
/** A tab hidden this long closes its stream, so a forgotten tab does not keep an agent at 1 s; it reopens when shown. */
const LIVE_HIDDEN_MS = 30_000;

/**
 * useLiveMetrics streams a server's host metrics while `on`: the last
 * LIVE_POINTS points, the stream's state, and `now` on the hub's clock,
 * ticking every second so the time axis slides between points. `slow` is
 * an agent that sent at most one point in the last 5 s: it predates live
 * mode and keeps its usual rate.
 */
export function useLiveMetrics(id: string, on: boolean) {
  const [points, setPoints] = useState<SysPoint[]>([]);
  const [state, setState] = useState<'connecting' | 'open' | 'lost' | 'failed'>('connecting');
  const [opened, setOpened] = useState(0);
  const [arrivals, setArrivals] = useState<number[]>([]);
  const [awake, setAwake] = useState(true);
  const [tick, setTick] = useState(() => Date.now());
  // The hub's clock minus ours: points are stamped by the hub, and a browser
  // a few seconds behind would draw the newest ones past the axis end.
  const skew = useRef(0);

  useEffect(() => {
    if (!on) return;
    let timer: number | undefined;
    const check = () => {
      window.clearTimeout(timer);
      if (document.visibilityState === 'hidden') timer = window.setTimeout(() => setAwake(false), LIVE_HIDDEN_MS);
      else setAwake(true);
    };
    check();
    document.addEventListener('visibilitychange', check);
    return () => {
      window.clearTimeout(timer);
      document.removeEventListener('visibilitychange', check);
    };
  }, [on]);

  useEffect(() => {
    if (!on || !awake) return;
    setPoints([]);
    setArrivals([]);
    setState('connecting');
    const es = new EventSource(`/api/v1/probes/${enc(id)}/live`);
    es.onopen = () => {
      setState('open');
      setOpened(Date.now());
    };
    es.onmessage = (ev) => {
      let m: LivePoint;
      try {
        m = JSON.parse(ev.data);
      } catch {
        return;
      }
      const now = Date.now();
      if (!m.age || m.age < 2) skew.current = m.at - now;
      setState('open');
      setPoints((ps) => [...ps, sysPoint(m)].sort((a, b) => a.t - b.t).slice(-LIVE_POINTS));
      setArrivals((a) => [...a.filter((x) => x > now - 10_000), now]);
    };
    // EventSource retries a dropped connection by itself (CONNECTING); a
    // refused one (404, 401) is CLOSED for good.
    es.onerror = () => setState(es.readyState === EventSource.CLOSED ? 'failed' : 'lost');
    const iv = window.setInterval(() => setTick(Date.now()), 1000);
    return () => {
      es.close();
      window.clearInterval(iv);
    };
  }, [id, on, awake]);

  const slow = state === 'open' && tick - opened >= 5000 && arrivals.filter((x) => x > tick - 5000).length <= 1;
  return { points, state, slow, now: tick + skew.current };
}

export const useSparklines = () =>
  useQuery({ queryKey: ['sparklines'], queryFn: () => api<Record<string, Spark>>('GET', '/api/v1/sparklines'), refetchInterval: 60_000 });
export type IncidentList = { open: Incident[] | null; history: Incident[] | null };
/** The hub returns up to this many past incidents per request; older ones come with ?before=. */
export const INCIDENT_PAGE = 100;
export const useIncidents = () =>
  useQuery({ queryKey: ['incidents'], queryFn: () => api<IncidentList>('GET', '/api/v1/incidents'), refetchInterval: LIVE });
export const useSettings = () => useQuery({ queryKey: ['settings'], queryFn: () => api<Settings>('GET', '/api/v1/settings') });
export const useRemnawave = (o?: Live) =>
  useQuery({ queryKey: ['remnawave'], queryFn: () => api<RemnawaveView>('GET', '/api/v1/remnawave'), ...cadence(o, 30_000) });
/** q is a window from useWindow. */
export const useRemnawaveUsers = (q: string, enabled = true) =>
  useQuery({
    enabled,
    queryKey: ['rw-users', q],
    queryFn: () => api<{ from: number; to: number; nodes: Record<string, { t: number; avg: number; min: number; max: number }[]> }>('GET', `/api/v1/remnawave/users?${q}`),
    refetchInterval: q.startsWith('from=') ? false : 60_000,
    placeholderData: (prev) => prev,
  });
export const useInvites = (enabled = true) =>
  useQuery({ enabled, queryKey: ['invites'], queryFn: () => api<{ invites: Invite[] | null }>('GET', '/api/v1/checkers/invites'), refetchInterval: 30_000 });
export const useUsers = () => useQuery({ queryKey: ['users'], queryFn: () => api<User[]>('GET', '/api/v1/users') });
export const useStatusPages = () => useQuery({ queryKey: ['status-pages'], queryFn: () => api<StatusPage[]>('GET', '/api/v1/status-pages') });

/** useApiMutation runs a write and refreshes everything that may have changed. */
export function useApiMutation<TBody, TOut = unknown>(method: string, path: (b: TBody) => string, body?: (b: TBody) => unknown) {
  const qc = useQueryClient();
  return useMutation<TOut, ApiError, TBody>({
    mutationFn: (b) => api<TOut>(method, path(b), body ? body(b) : undefined),
    onSuccess: () => qc.invalidateQueries(),
  });
}
