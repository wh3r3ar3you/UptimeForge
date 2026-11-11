import {
  Alert, Anchor, Box, Button, Card, Grid, Group, SimpleGrid, Skeleton, Stack, Table, Text, Tooltip, UnstyledButton,
} from '@mantine/core';
import {
  IconActivity, IconAffiliate, IconAlertTriangle, IconArrowsSort, IconBrandSpeedtest, IconCalendar, IconCalendarMonth,
  IconCalendarStats, IconCalendarWeek, IconChartArea, IconClock, IconCpu, IconDatabase, IconDeviceDesktopAnalytics, IconServer,
  IconServerBolt, IconSortAscending, IconSortDescending, IconStack2, IconTrendingDown, IconUserCheck, IconUserOff, IconUsers, IconUsersGroup,
  IconWorldUpload,
} from '@tabler/icons-react';
import type { EChartsCoreOption } from 'echarts/core';
import { useMemo, useState, type ReactNode } from 'react';
import { Link } from 'react-router';
import { enc, useChecks, useOverview, useRemnawave, type BaseStat, type Incident, type CheckRow, type Kind, type RemnawaveNode, type RemnawaveView } from '../api';
import { Empty, Section, StatCard } from '../components/Cards';
import { Chart, usePalette } from '../components/Chart';
import { PageHeader, QueryError } from '../components/Misc';
import { NodeUsersChart } from '../components/NodeUsersChart';
import { Dot, VerdictBadge } from '../components/Status';
import { ago, bitrate, bytes, days, flag, ms, num } from '../format';
import { useLang, useT } from '../i18n';
import { status } from '../theme';

/**
 * StatGrid keeps one tile per row on phones: values here are long ("312.81 MiB",
 * "2 ч 25 мин") and would be cut off in a two-column phone grid.
 */
function StatGrid({ children, cols = 4 }: { children: ReactNode; cols?: number }) {
  return <SimpleGrid cols={{ base: 1, xs: 2, md: Math.min(cols, 3), lg: cols }} spacing="md">{children}</SimpleGrid>;
}

const L = {
  ru: {
    title: 'Remnawave', subtitle: 'Состояние панели и нод · обновлено {0}', subtitle_wait: 'Состояние панели и нод',
    not_configured: 'Remnawave не подключён', not_configured_hint: 'Укажите адрес панели и API-токен — здесь появятся нагрузка, трафик, онлайн и состояние каждой ноды, а UptimeForge сопоставит ноды со своими проверками.',
    connect: 'Подключить', unreachable: 'Панель не отвечает', stale: 'Ниже — последние данные, которые удалось получить.',
    load: 'Нагрузка Remnawave', processes: 'Процессов', rss: 'Память (RSS)', heap: 'Heap', heap_of: 'из {0}', loop: 'Задержка event loop',
    loop_sub: 'в среднем · p99 {0}', process_details: 'Подробности процесса', proc_sub: 'heap {0} · loop {1}', proc_n: '{0} × процесс',
    traffic: 'Трафик', today: 'Сегодня', d7: '7 дней', d30: '30 дней', month: 'Календарный месяц', year: 'Текущий год',
    vs_yesterday: 'к вчера', vs_prev7: 'к прошлым 7 дням', vs_prev30: 'к прошлым 30 дням', vs_month: 'к прошлому месяцу', vs_year: 'к прошлому году',
    same: 'как в прошлом периоде',
    online: 'Статистика онлайна', online_now: 'Сейчас онлайн', online_day: 'За сегодня', online_week: 'За неделю', never: 'Не подключались',
    of_users: 'из {0} пользователей', was: 'было {0}',
    system: 'Система', users_on_nodes: 'Онлайн на нодах', lifetime: 'Трафик за всё время', ram: 'ОЗУ панели', ram_sub: 'из {0} · {1}%',
    nodes_online: 'Ноды онлайн', nodes_sub: 'отключено: {0}',
    nodes: 'Ноды', st: 'Статус', node: 'Нода', address: 'Адрес', users: 'Онлайн', used: 'Трафик', rate: 'RX / TX', load_avg: 'Load avg',
    xray: 'Xray', xray_uptime: 'Аптайм Xray', ours: 'UptimeForge', message: 'Последнее сообщение',
    connected: 'Подключена', connecting: 'Подключается', disabled: 'Отключена', offline: 'Нет связи',
    no_match: 'Нет проверки с адресом этой ноды', matched: 'Проверки этой ноды:', no_nodes: 'В панели нет нод',
    top: 'Топ нод за 7 дней', top_empty: 'Данных о трафике пока нет', node_version: 'нода {0}', limit: 'лимит {0}', up_for: 'работает {0}',
    api: 'API', scheduler: 'Планировщик', processor: 'Обработчик',
  },
  en: {
    title: 'Remnawave', subtitle: 'Panel and node state · updated {0}', subtitle_wait: 'Panel and node state',
    not_configured: 'Remnawave is not connected', not_configured_hint: 'Add the panel address and an API token to see load, traffic, online users and every node here — UptimeForge also matches nodes with its own checks.',
    connect: 'Connect', unreachable: 'The panel does not answer', stale: 'Below is the last data that could be fetched.',
    load: 'Remnawave load', processes: 'Processes', rss: 'Memory (RSS)', heap: 'Heap', heap_of: 'of {0}', loop: 'Event loop delay',
    loop_sub: 'mean · p99 {0}', process_details: 'Process details', proc_sub: 'heap {0} · loop {1}', proc_n: '{0} × process',
    traffic: 'Traffic', today: 'Today', d7: '7 days', d30: '30 days', month: 'Calendar month', year: 'Current year',
    vs_yesterday: 'vs yesterday', vs_prev7: 'vs previous 7 days', vs_prev30: 'vs previous 30 days', vs_month: 'vs last month', vs_year: 'vs last year',
    same: 'same as the previous period',
    online: 'Online', online_now: 'Online now', online_day: 'Today', online_week: 'This week', never: 'Never connected',
    of_users: 'of {0} users', was: 'was {0}',
    system: 'System', users_on_nodes: 'Online on nodes', lifetime: 'Lifetime traffic', ram: 'Panel RAM', ram_sub: 'of {0} · {1}%',
    nodes_online: 'Nodes online', nodes_sub: 'disabled: {0}',
    nodes: 'Nodes', st: 'Status', node: 'Node', address: 'Address', users: 'Online', used: 'Traffic', rate: 'RX / TX', load_avg: 'Load avg',
    xray: 'Xray', xray_uptime: 'Xray uptime', ours: 'UptimeForge', message: 'Last message',
    connected: 'Connected', connecting: 'Connecting', disabled: 'Disabled', offline: 'Unreachable',
    no_match: 'No check targets this node address', matched: 'Checks of this node:', no_nodes: 'The panel has no nodes',
    top: 'Top nodes over 7 days', top_empty: 'No traffic data yet', node_version: 'node {0}', limit: 'limit {0}', up_for: 'up {0}',
    api: 'API', scheduler: 'Scheduler', processor: 'Processor',
  },
};

type Runtime = NonNullable<NonNullable<RemnawaveView['stats']>['runtime']>[number] & { heapTotal?: number; eventLoopP99Ms?: number };

/** Remnawave sends byte amounts as strings: raw integers or preformatted IEC text ("17.94 TiB", "-6.94 TiB"). */
function size(s?: string | null) {
  if (s == null || s === '') return '—';
  const t = s.trim();
  return /^-?\d+$/.test(t) ? bytes(Number(t)) : t;
}

function delta(b: BaseStat, caption: string) {
  const t = (b.difference ?? '').trim();
  const negative = t.startsWith('-');
  const abs = t.replace(/^[+-]/, '');
  if (!abs || /^0+(\.0+)?(\s*[A-Za-z]+)?$/.test(abs)) return undefined;
  return { value: `${negative ? '−' : '+'}${size(abs)}`, negative, caption };
}

const rank: Record<string, number> = { down: 5, blocked: 4, partial: 3, probe_offline: 2, probe_no_net: 2, host: 2, users_drop: 4, node_down: 4, ok: 1, unknown: 0, paused: 0 };

function escapeRe(s: string) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/** checksFor finds our checks whose target ("VLESS · host:port", "host:port", URL) points at the node. */
function checksFor(n: RemnawaveNode, checks: CheckRow[]) {
  const addr = n.address?.trim();
  const re = addr ? new RegExp(`(^|[\\s/@·\\[])${escapeRe(addr)}(\\]?:|/|$)`, 'i') : null;
  let found = checks.filter((c) => !c.subscription && c.target && re?.test(c.target));
  if (!found.length) found = checks.filter((c) => !c.subscription && c.id.toLowerCase() === n.name.toLowerCase());
  return found.sort((a, b) => (rank[b.verdict] ?? 0) - (rank[a.verdict] ?? 0));
}

function NodeState({ n }: { n: RemnawaveNode }) {
  const t = useT(L);
  const s = n.isDisabled ? { c: status.unknown, l: t('disabled') }
    : n.isConnected ? { c: status.ok, l: t('connected'), p: true }
    : n.isConnecting ? { c: status.partial, l: t('connecting') }
    : { c: status.down, l: t('offline') };
  return (
    <Group gap={8} wrap="nowrap">
      <Dot color={s.c} pulse={s.p} />
      <Text size="sm">{s.l}</Text>
    </Group>
  );
}

type SortKey = 'users' | 'name' | 'traffic';

function Th({ label, k, sort, onSort }: { label: string; k: SortKey; sort: { k: SortKey; desc: boolean } | null; onSort: (k: SortKey) => void }) {
  const active = sort?.k === k;
  const I = !active ? IconArrowsSort : sort.desc ? IconSortDescending : IconSortAscending;
  return (
    <Table.Th>
      <UnstyledButton onClick={() => onSort(k)} style={{ display: 'inline-flex', alignItems: 'center', gap: 4, fontWeight: 'inherit', fontSize: 'inherit' }}>
        {label}<I size={14} style={{ opacity: active ? 0.9 : 0.4 }} />
      </UnstyledButton>
    </Table.Th>
  );
}

function NodesTable({ nodes, checks, drops }: { nodes: RemnawaveNode[]; checks: CheckRow[]; drops: Map<string, Incident> }) {
  const t = useT(L);
  const { lang } = useLang();
  const [sort, setSort] = useState<{ k: SortKey; desc: boolean } | null>({ k: 'users', desc: true });
  const onSort = (k: SortKey) => setSort((s) => (s?.k === k ? (s.desc ? { k, desc: false } : null) : { k, desc: k !== 'name' }));
  const rows = useMemo(() => {
    const r = [...nodes];
    if (!sort) return r;
    const v = (n: RemnawaveNode) => (sort.k === 'users' ? n.usersOnline : sort.k === 'traffic' ? n.trafficUsedBytes ?? 0 : n.name.toLowerCase());
    r.sort((a, b) => {
      const x = v(a), y = v(b);
      const c = x < y ? -1 : x > y ? 1 : 0;
      return sort.desc ? -c : c;
    });
    return r;
  }, [nodes, sort]);

  return (
    <Card padding={0}>
      <Table.ScrollContainer minWidth={1000}>
        <Table className="uf-table" verticalSpacing="sm" horizontalSpacing="sm" highlightOnHover style={{ whiteSpace: 'nowrap' }}>
          <Table.Thead>
            <Table.Tr>
              <Th label={t('node')} k="name" sort={sort} onSort={onSort} />
              <Table.Th>{t('st')}</Table.Th>
              <Table.Th>{t('ours')}</Table.Th>
              <Th label={t('users')} k="users" sort={sort} onSort={onSort} />
              <Th label={t('used')} k="traffic" sort={sort} onSort={onSort} />
              <Table.Th>{t('rate')}</Table.Th>
              <Table.Th>{t('load_avg')}</Table.Th>
              <Table.Th>{t('xray')}</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {rows.map((n) => {
              const iface = n.system?.stats.interface;
              const load = n.system?.stats.loadAvg;
              const ours = checksFor(n, checks);
              const worst = ours[0];
              return (
                <Table.Tr key={n.uuid} style={{ opacity: n.isDisabled ? 0.6 : 1 }}>
                  <Table.Td>
                    <Text size="sm" fw={600}><Box component="span" mr={6}>{flag(n.countryCode) || '🏳️'}</Box>{n.name}</Text>
                    <Text size="xs" ff="monospace" c="dimmed">{n.address}{n.port ? `:${n.port}` : ''}</Text>
                  </Table.Td>
                  <Table.Td>
                    <NodeState n={n} />
                    {n.lastStatusMessage && (
                      <Tooltip label={n.lastStatusMessage} multiline maw={360}>
                        <Text size="xs" c="dimmed" truncate="end" maw={150} mt={2} ml={17}>{n.lastStatusMessage}</Text>
                      </Tooltip>
                    )}
                  </Table.Td>
                  <Table.Td miw={130}>
                    {worst ? (
                      <Tooltip
                        label={<Stack gap={2}><Text size="xs" fw={600}>{t('matched')}</Text>{ours.map((c) => <Text key={c.id} size="xs">{c.name} · {c.target}</Text>)}</Stack>}
                      >
                        <Anchor component={Link} to={`/checks/${enc(worst.id)}`} underline="never">
                          <VerdictBadge kind={worst.verdict as Kind} region={worst.region} size="sm" />
                        </Anchor>
                      </Tooltip>
                    ) : (
                      <Tooltip label={t('no_match')}><Text size="sm" c="dimmed">—</Text></Tooltip>
                    )}
                  </Table.Td>
                  <Table.Td>
                    {(() => {
                      const d = drops.get(n.uuid)?.drop;
                      return d ? (
                        <Anchor component={Link} to="/incidents" underline="never">
                          <Group gap={6} wrap="nowrap">
                            <Text size="sm" className="uf-num" fw={700} c={status.down}>{num(n.usersOnline)}</Text>
                            <Group gap={3} wrap="nowrap" c="red"><IconTrendingDown size={13} /><Text size="xs" c="red" style={{ whiteSpace: 'nowrap' }}>{t('was', num(d.before))}</Text></Group>
                          </Group>
                        </Anchor>
                      ) : <Text size="sm" className="uf-num" fw={600}>{num(n.usersOnline)}</Text>;
                    })()}
                  </Table.Td>
                  <Table.Td>
                    <Text size="sm" className="uf-num">{bytes(n.trafficUsedBytes)}</Text>
                    {n.trafficLimitBytes ? <Text size="xs" c="dimmed" className="uf-num">{t('limit', bytes(n.trafficLimitBytes))}</Text> : null}
                  </Table.Td>
                  <Table.Td>
                    {iface ? (
                      <Stack gap={0}>
                        <Text size="xs" className="uf-num">↓ {bitrate(iface.rxBytesPerSec)}</Text>
                        <Text size="xs" className="uf-num" c="dimmed">↑ {bitrate(iface.txBytesPerSec)}</Text>
                      </Stack>
                    ) : <Text size="sm" c="dimmed">—</Text>}
                  </Table.Td>
                  <Table.Td>
                    <Text size="xs" className="uf-num">{load?.length ? load.slice(0, 3).map((v) => v.toFixed(2)).join('  ') : '—'}</Text>
                  </Table.Td>
                  <Table.Td>
                    <Tooltip label={t('node_version', n.versions?.node ?? '—')} disabled={!n.versions}>
                      <Text size="sm" className="uf-num">{n.versions?.xray ?? '—'}</Text>
                    </Tooltip>
                    {n.xrayUptime > 0 && <Text size="xs" c="dimmed">{t('up_for', days(n.xrayUptime, lang))}</Text>}
                  </Table.Td>
                </Table.Tr>
              );
            })}
            {!rows.length && (
              <Table.Tr><Table.Td colSpan={8}><Text size="sm" c="dimmed" ta="center" py="md">{t('no_nodes')}</Text></Table.Td></Table.Tr>
            )}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
    </Card>
  );
}

function TopNodes({ items }: { items: { name: string; countryCode: string; total: number }[] }) {
  const pal = usePalette();
  const option = useMemo<EChartsCoreOption>(() => {
    const data = [...items].sort((a, b) => b.total - a.total);
    return {
      animation: false,
      grid: { left: 8, right: 84, top: 4, bottom: 4, containLabel: true },
      xAxis: { type: 'value', show: false, max: (v: { max: number }) => v.max * 1.02 },
      yAxis: {
        type: 'category',
        inverse: true,
        data: data.map((d) => `${flag(d.countryCode)} ${d.name}`.trim()),
        axisLine: { show: false },
        axisTick: { show: false },
        axisLabel: { color: pal.textSecondary, fontSize: 12, margin: 12 },
      },
      tooltip: {
        trigger: 'item',
        backgroundColor: pal.surface,
        borderColor: pal.grid,
        borderWidth: 1,
        padding: [8, 12],
        textStyle: { color: pal.text, fontSize: 12 },
        formatter: (p: { name: string; value: number }) =>
          `<div style="color:${pal.muted};margin-bottom:4px">${p.name.replace(/[&<>]/g, '')}</div><b style="font-family:var(--mantine-font-family-monospace)">${bytes(p.value)}</b>`,
      },
      series: [{
        type: 'bar',
        data: data.map((d) => d.total),
        barMaxWidth: 16,
        barCategoryGap: '38%',
        itemStyle: { color: pal.series[0], borderRadius: [0, 4, 4, 0] },
        emphasis: { itemStyle: { color: pal.series[0] } },
        label: {
          show: true,
          position: 'right',
          distance: 8,
          color: pal.text,
          fontFamily: '"JetBrains Mono Variable", ui-monospace, monospace',
          fontSize: 12,
          fontWeight: 600,
          formatter: (p: { value: number }) => bytes(p.value, 1),
        },
      }],
    };
  }, [items, pal]);
  return <Chart option={option} height={Math.max(120, items.length * 34 + 16)} />;
}

function Loading() {
  return (
    <Stack gap="xl">
      {[0, 1, 2].map((s) => (
        <Stack key={s} gap="sm">
          <Skeleton h={22} w={220} />
          <StatGrid>{[0, 1, 2, 3].map((i) => <Skeleton key={i} h={86} radius="lg" />)}</StatGrid>
        </Stack>
      ))}
    </Stack>
  );
}

export default function Remnawave() {
  const t = useT(L);
  const { lang } = useLang();
  const q = useRemnawave();
  const checksQ = useChecks();
  const v = q.data;
  const st = v?.stats;
  const checks = checksQ.data?.checks ?? [];
  const ov = useOverview();
  const drops = useMemo(() => new Map((ov.data?.incidents ?? []).filter((i) => i.key.startsWith('rw:')).map((i) => [i.key.slice(3), i])), [ov.data]);

  const runtime = (st?.runtime ?? []) as Runtime[];
  const procTypes = useMemo(() => {
    const m = new Map<string, Runtime[]>();
    for (const r of runtime) m.set(r.instanceType, [...(m.get(r.instanceType) ?? []), r]);
    return [...m.entries()];
  }, [runtime]);
  const sum = (f: (r: Runtime) => number, rs = runtime) => rs.reduce((s, r) => s + (f(r) || 0), 0);
  const avgLoop = runtime.length ? sum((r) => r.eventLoopDelayMs) / runtime.length : null;
  const p99 = runtime.length ? Math.max(...runtime.map((r) => r.eventLoopP99Ms ?? 0)) : 0;
  const heapTotal = sum((r) => r.heapTotal ?? 0);

  if (q.isLoading) {
    return (<><PageHeader title={t('title')} subtitle={t('subtitle_wait')} /><Loading /></>);
  }
  // The hub did not answer: say so instead of a skeleton that never resolves.
  if (!v && q.isError) {
    return (<><PageHeader title={t('title')} /><QueryError error={q.error} onRetry={() => q.refetch()} /></>);
  }
  if (v && !v.configured) {
    return (
      <>
        <PageHeader title={t('title')} subtitle={t('subtitle_wait')} />
        <Empty icon={IconAffiliate} title={t('not_configured')}>
          <Text c="dimmed" size="sm" ta="center" maw={520}>{t('not_configured_hint')}</Text>
          <Button component={Link} to="/settings/remnawave" mt="xs">{t('connect')}</Button>
        </Empty>
      </>
    );
  }

  const procIcon: Record<string, typeof IconCpu> = { api: IconServerBolt, scheduler: IconClock, processor: IconStack2 };
  const procLabel = (k: string) => (k === 'api' || k === 'scheduler' || k === 'processor' ? t(k) : k);
  const bw = st?.bandwidth;
  const sys = st?.system;
  const memPct = sys?.memory.total ? Math.round((sys.memory.used / sys.memory.total) * 100) : 0;

  return (
    <>
      <PageHeader title={t('title')} subtitle={v?.fetched ? t('subtitle', ago(v.fetched, lang)) : t('subtitle_wait')} />
      <Stack gap="xl">
        {v?.error && (
          <Alert color="orange" variant="light" icon={<IconAlertTriangle size={18} />} title={t('unreachable')}>
            <Text size="sm" style={{ wordBreak: 'break-word', whiteSpace: 'pre-line' }}>{v.error}</Text>
            {st && <Text size="sm" c="dimmed" mt={4}>{t('stale')}</Text>}
          </Alert>
        )}
        {!st ? (v?.error ? null : <Loading />) : (
          <>
            <Section title={t('load')}>
              <StatGrid>
                <StatCard icon={IconCpu} color="blue" label={t('processes')} value={runtime.length || '—'} sub={procTypes.map(([k]) => k).join(' · ')} />
                <StatCard icon={IconDatabase} color="violet" label={t('rss')} value={bytes(sum((r) => r.rss))} />
                <StatCard icon={IconStack2} color="cyan" label={t('heap')} value={bytes(sum((r) => r.heapUsed))} sub={heapTotal ? t('heap_of', bytes(heapTotal)) : undefined} />
                <StatCard icon={IconActivity} color="forge" label={t('loop')} value={ms(avgLoop)} sub={p99 ? t('loop_sub', ms(p99)) : undefined} />
              </StatGrid>
            </Section>

            {procTypes.length > 0 && (
              <Section title={t('process_details')}>
                <StatGrid cols={Math.min(4, Math.max(procTypes.length, 2))}>
                  {procTypes.map(([k, rs]) => (
                    <StatCard
                      key={k}
                      icon={procIcon[k] ?? IconCpu}
                      color="gray"
                      label={rs.length > 1 ? `${procLabel(k)} · ${t('proc_n', rs.length)}` : procLabel(k)}
                      value={bytes(sum((r) => r.rss, rs))}
                      sub={t('proc_sub', bytes(sum((r) => r.heapUsed, rs)), ms(sum((r) => r.eventLoopDelayMs, rs) / rs.length))}
                    />
                  ))}
                </StatGrid>
              </Section>
            )}

            {bw && (
              <Section title={t('traffic')}>
                <Grid gutter="md">
                  {([
                    ['today', bw.bandwidthLastTwoDays, 'vs_yesterday', IconCalendar],
                    ['d7', bw.bandwidthLastSevenDays, 'vs_prev7', IconCalendarWeek],
                    ['d30', bw.bandwidthLast30Days, 'vs_prev30', IconCalendarStats],
                    ['month', bw.bandwidthCalendarMonth, 'vs_month', IconCalendarMonth],
                    ['year', bw.bandwidthCurrentYear, 'vs_year', IconChartArea],
                  ] as const).map(([k, b, cap, I], i) => (
                    <Grid.Col key={k} span={{ base: 12, xs: 6, md: i < 3 ? 4 : 6 }}>
                      <StatCard icon={I} color="forge" label={t(k)} value={size(b.current)} delta={delta(b, t(cap))} sub={t('same')} />
                    </Grid.Col>
                  ))}
                </Grid>
              </Section>
            )}

            {sys && (
              <Section title={t('online')}>
                <StatGrid>
                  <StatCard icon={IconUsers} color="green" label={t('online_now')} value={num(sys.onlineStats.onlineNow)} sub={t('of_users', num(sys.users.totalUsers))} />
                  <StatCard icon={IconUserCheck} color="blue" label={t('online_day')} value={num(sys.onlineStats.lastDay)} />
                  <StatCard icon={IconCalendarWeek} color="violet" label={t('online_week')} value={num(sys.onlineStats.lastWeek)} />
                  <StatCard icon={IconUserOff} color="gray" label={t('never')} value={num(sys.onlineStats.neverOnline)} />
                </StatGrid>
              </Section>
            )}

            {sys && (
              <Section title={t('system')}>
                <StatGrid>
                  <StatCard icon={IconUsersGroup} color="cyan" label={t('users_on_nodes')} value={num(sys.nodes.totalOnline)} />
                  <StatCard icon={IconWorldUpload} color="forge" label={t('lifetime')} value={size(sys.nodes.totalBytesLifetime)} />
                  <StatCard
                    icon={IconDeviceDesktopAnalytics}
                    color={memPct >= 90 ? 'red' : memPct >= 75 ? 'orange' : 'blue'}
                    label={t('ram')}
                    value={bytes(sys.memory.used)}
                    sub={t('ram_sub', bytes(sys.memory.total), memPct)}
                  />
                  <StatCard
                    icon={IconServer}
                    color={st.nodesOnline < st.nodesTotal - st.nodesDisabled ? 'orange' : 'green'}
                    label={t('nodes_online')}
                    value={`${st.nodesOnline} / ${st.nodesTotal}`}
                    sub={t('nodes_sub', st.nodesDisabled)}
                  />
                </StatGrid>
              </Section>
            )}
          </>
        )}

        {v?.nodes && <NodeUsersChart nodes={v.nodes} />}

        {v?.nodes && (
          <Section title={t('nodes')}>
            <NodesTable nodes={v.nodes} checks={checks} drops={drops} />
          </Section>
        )}

        {st && (
          <Section title={t('top')}>
            <Card padding="lg">
              {v?.usage?.topNodes?.length ? <TopNodes items={v.usage.topNodes} /> : (
                <Group gap="xs" c="dimmed" justify="center" py="lg"><IconBrandSpeedtest size={18} /><Text size="sm">{t('top_empty')}</Text></Group>
              )}
            </Card>
          </Section>
        )}
      </Stack>
    </>
  );
}
