import {
  Alert, Anchor, Badge, Box, Breadcrumbs, Button, Card, Chip, Group, Menu, Skeleton, Stack, Table, Text, Title, Tooltip,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import {
  IconActivity, IconBellOff, IconCheck, IconClockHour4, IconDots, IconGauge, IconPencil, IconPlayerPause, IconPlayerPlay, IconServer2,
  IconTrash, IconTrendingUp,
} from '@tabler/icons-react';
import { useMemo, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router';
import {
  api, ApiError, enc, queryClient, useCheck, useCheckSeries, useMeta, useUptime, type CheckRow, type Incident, type Point, type ProbeStatus, type ProbeCell } from '../api';
import { Section, StatCard, Tiles } from '../components/Cards';
import { useWindow, usePalette, type LineSeries } from '../components/Chart';
import { MetricCard, WindowControl } from '../components/MetricCard';
import { CheckForm } from '../components/CheckForm';
import { useParentLabels } from '../components/checks/parents';
import { clickableRow, notifyError, notifyOk, PageHeader, ProbeName, QueryError } from '../components/Misc';
import { kindColor, KindIcon, VerdictBadge } from '../components/Status';
import { UptimeBars } from '../components/UptimeBars';
import { ago, bitrate, clock, duration, ms, parseGoDuration, pct } from '../format';
import { useCommon, useLang, useT } from '../i18n';
import { reasonText } from '../reason';
import { status } from '../theme';
import { ProbeDots, uptimeColor, useTypeLabel } from './Checks';

const L = {
  ru: {
    depends: 'зависит от {0}', depends_auto: 'по адресу',
    checks: 'Проверки', edit: 'Изменить', edit_sub: 'Изменить подписку', pause: 'Пауза', resume: 'Возобновить', delete: 'Удалить',
    uptime24: 'Аптайм за 24ч', latency24: 'Средняя задержка', failing: 'Пробы со сбоем', speed: 'Скорость через туннель', interval: 'Интервал',
    every: 'каждые {0}', uptime90: 'Аптайм за 90 дней', latency: 'Задержка по пробам', latency_by_probe: 'Средняя задержка', timeline: 'Хронология по пробам', timeline_hint: 'Каждая клетка — доля успешных проверок в интервале',
    throughput: 'Скорость загрузки через туннель', probes_now: 'Пробы сейчас', probe: 'Проба', state: 'Состояние', ttfb: 'TTFB',
    checked: 'Проверено', history: 'История инцидентов', no_incidents: 'Инцидентов не было', started: 'Начало', lasted: 'Длительность',
    ongoing: 'идёт', open_incident: 'Открытый инцидент', ack: 'Принять', mute: 'Заглушить', h1: 'на 1 час', h8: 'на 8 часов', h24: 'на сутки',
    acked: 'Принял {0}', muted: 'Заглушено до {0}', children: 'Конфиги подписки', no_data: 'Пока нет данных за этот период',
    deleted: 'Проверка удалена', paused_note: 'Проверка на паузе', not_found: 'Проверка не найдена', confirm: 'Удалить проверку «{0}»? История останется в базе.',
    up: 'работает', down: 'не работает', pending: 'сбой, перепроверяем', nodata: 'нет данных', mbps: 'Мбит/с', all_ok: 'все работают',
    dcs: 'DC Telegram', dc_silent: 'DC {0} не ответил через этот прокси',
    only: 'Показаны первые 8 проб — выберите, какие сравнить', more: 'Ещё действия',
  },
  en: {
    depends: 'depends on {0}', depends_auto: 'by address',
    checks: 'Checks', edit: 'Edit', edit_sub: 'Edit subscription', pause: 'Pause', resume: 'Resume', delete: 'Delete',
    uptime24: 'Uptime 24h', latency24: 'Average latency', failing: 'Failing probes', speed: 'Tunnel speed', interval: 'Interval',
    every: 'every {0}', uptime90: 'Uptime, 90 days', latency: 'Latency by probe', latency_by_probe: 'Average latency', timeline: 'Timeline by probe', timeline_hint: 'Each cell is the share of successful runs in that slot',
    throughput: 'Download speed through the tunnel', dcs: 'Telegram DCs', dc_silent: 'DC {0} did not answer through this proxy', probes_now: 'Probes now', probe: 'Probe', state: 'State', ttfb: 'TTFB',
    checked: 'Checked', history: 'Incident history', no_incidents: 'No incidents', started: 'Started', lasted: 'Duration',
    ongoing: 'ongoing', open_incident: 'Open incident', ack: 'Acknowledge', mute: 'Mute', h1: 'for 1 hour', h8: 'for 8 hours', h24: 'for a day',
    acked: 'Acknowledged by {0}', muted: 'Muted until {0}', children: 'Subscription configs', no_data: 'No data for this period yet',
    deleted: 'Check deleted', paused_note: 'This check is paused', not_found: 'Check not found', confirm: 'Delete check “{0}”? Its history stays in the database.',
    up: 'up', down: 'down', pending: 'failed, rechecking', nodata: 'no data', mbps: 'Mbit/s', all_ok: 'all up',
    only: 'Showing the first 8 probes — pick which to compare', more: 'More actions',
  },
};


function shortDuration(sec: number, lang: 'ru' | 'en') {
  if (!sec) return '—';
  if (sec >= 3600) return lang === 'ru' ? `${sec / 3600} ч` : `${sec / 3600}h`;
  if (sec >= 60) return lang === 'ru' ? `${sec / 60} мин` : `${sec / 60}m`;
  return lang === 'ru' ? `${sec} с` : `${sec}s`;
}

function useProbeColors(probes: ProbeStatus[]) {
  const pal = usePalette();
  return useMemo(() => new Map(probes.map((p, i) => [p.id, pal.series[i % pal.series.length]])), [probes, pal]);
}

function Timeline({ series, probes, from, to }: { series: Record<string, Point[]>; probes: ProbeStatus[]; from: number; to: number }) {
  const t = useT(L);
  const slots = 72;
  const step = (to - from) / slots;
  const rows = probes.filter((p) => series[p.id]?.length);
  if (!rows.length) return <Text c="dimmed" size="sm">{t('no_data')}</Text>;
  return (
    <Stack gap={6}>
      {rows.map((p) => {
        const cells: { n: number; up: number }[] = Array.from({ length: slots }, () => ({ n: 0, up: 0 }));
        for (const pt of series[p.id]) {
          const i = Math.min(slots - 1, Math.max(0, Math.floor((pt.t - from) / step)));
          cells[i].n += pt.n;
          cells[i].up += pt.up * pt.n;
        }
        return (
          <Group key={p.id} gap="sm" wrap="nowrap">
            <Box w={170} style={{ flex: 'none' }}><ProbeName name={p.name} region={p.region} size="xs" /></Box>
            <Box style={{ display: 'flex', gap: 2, flex: 1, height: 18 }}>
              {cells.map((c, i) => {
                const r = c.n ? c.up / c.n : null;
                const color = r == null ? 'var(--uf-track)' : r >= 0.999 ? status.ok : r > 0 ? status.partial : status.down;
                const at = new Date((from + i * step) * 1000);
                return (
                  <Tooltip key={i} label={`${clock(at.toISOString(), true)} · ${r == null ? t('nodata') : pct(r, 1)}`}>
                    <Box style={{ flex: 1, borderRadius: 3, background: color, opacity: r == null ? 1 : 0.9 }} />
                  </Tooltip>
                );
              })}
            </Box>
          </Group>
        );
      })}
    </Stack>
  );
}

/** IncidentCard: acknowledging and muting change alerting for everyone, so viewers see the state, not the buttons. */
function IncidentCard({ inc, canAct }: { inc: Incident; canAct: boolean }) {
  const t = useT(L);
  const { lang } = useLang();
  const act = async (path: string, body?: unknown) => {
    try {
      await api('POST', `/api/v1/incidents/${inc.id}/${path}`, body);
      await queryClient.invalidateQueries();
    } catch (e) {
      notifyError(e);
    }
  };
  return (
    <Alert
      variant="light"
      color={inc.severity === 'critical' ? 'red' : 'yellow'}
      title={<Group gap="sm"><Text fw={700}>{t('open_incident')}</Text><VerdictBadge kind={inc.kind} region={inc.region} size="sm" /><Text size="sm" c="dimmed">{clock(inc.started)} · {duration((Date.now() - Date.parse(inc.started)) / 1000, lang)}</Text></Group>}
    >
      <Stack gap={6}>
        {(inc.lines ?? []).map((l) => (
          <Group key={l.probe} gap="xs" wrap="nowrap">
            <Text size="sm">{l.ok ? '✅' : '❌'}</Text>
            <ProbeName name={l.name} region={l.region} />
            <Text size="sm" c="dimmed" truncate>— {l.ok ? ms(l.latency_ms) : reasonText(l.reason, l.error, lang)}</Text>
          </Group>
        ))}
        <Group gap="xs" mt={4}>
          {inc.ack_by ? <Badge color="green" variant="light" leftSection={<IconCheck size={12} />}>{t('acked', inc.ack_by)}</Badge>
            : canAct && <Button size="xs" variant="white" color="dark" leftSection={<IconCheck size={14} />} onClick={() => act('ack')}>{t('ack')}</Button>}
          {canAct && (
            <Menu>
              <Menu.Target><Button size="xs" variant="default" leftSection={<IconBellOff size={14} />}>{t('mute')}</Button></Menu.Target>
              <Menu.Dropdown>
                <Menu.Item onClick={() => act('mute', { minutes: 60 })}>{t('h1')}</Menu.Item>
                <Menu.Item onClick={() => act('mute', { minutes: 480 })}>{t('h8')}</Menu.Item>
                <Menu.Item onClick={() => act('mute', { minutes: 1440 })}>{t('h24')}</Menu.Item>
              </Menu.Dropdown>
            </Menu>
          )}
          {inc.muted_until && Date.parse(inc.muted_until) > Date.now() && <Text size="xs" c="dimmed">{t('muted', clock(inc.muted_until))}</Text>}
        </Group>
      </Stack>
    </Alert>
  );
}

function History({ incidents }: { incidents: Incident[] }) {
  const t = useT(L);
  const c = useCommon();
  const { lang } = useLang();
  if (!incidents.length) return <Text c="dimmed" size="sm" p="lg">{t('no_incidents')}</Text>;
  return (
    <Box className="uf-scroll-x">
      <Table className="uf-table" verticalSpacing="sm" miw={560}>
        <Table.Thead><Table.Tr><Table.Th>{c('status')}</Table.Th><Table.Th>{t('started')}</Table.Th><Table.Th>{t('lasted')}</Table.Th><Table.Th>{c('probes')}</Table.Th></Table.Tr></Table.Thead>
        <Table.Tbody>
          {incidents.map((i) => (
            <Table.Tr key={i.id}>
              <Table.Td><Group gap={6} wrap="nowrap"><KindIcon kind={i.worst} /><Text size="sm" fw={500}>{c(i.worst as 'ok')}{i.region ? ` · ${i.region.toUpperCase()}` : ''}</Text>{i.key.includes('/') && <Text size="xs" c="dimmed" truncate>{i.name}</Text>}</Group></Table.Td>
              <Table.Td><Text size="sm" className="uf-num">{clock(i.started, true)}</Text></Table.Td>
              <Table.Td><Text size="sm">{i.resolved ? duration((Date.parse(i.resolved) - Date.parse(i.started)) / 1000, lang) : <Badge color="red" variant="light" size="sm">{t('ongoing')}</Badge>}</Text></Table.Td>
              <Table.Td><Text size="sm" className="uf-num">{i.peak_failing ?? '—'}/{i.peak_total ?? '—'}</Text></Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Box>
  );
}

/** DCCells shows every Telegram DC asked through the proxy: milliseconds, or a red mark for silence. */
function DCCells({ cell }: { cell: ProbeCell }) {
  const t = useT(L);
  const up = (cell.dc_ms ?? '').split(' ').filter(Boolean).map((s) => s.split(':'));
  const down = (cell.dc_down ?? '').split(',').filter(Boolean).map((dc) => [dc]);
  if (!up.length && !down.length) return <Text size="sm" c="dimmed">—</Text>;
  const all = [...up, ...down].sort((a, b) => a[0].localeCompare(b[0]));
  return (
    <Group gap={8} wrap="nowrap">
      {all.map(([dc, v]) => (v ? (
        <Text key={dc} size="xs" className="uf-num" style={{ whiteSpace: 'nowrap' }}><Text span c="dimmed" inherit>{dc}:</Text>{v}</Text>
      ) : (
        <Tooltip key={dc} label={t('dc_silent', dc)}><Text size="xs" className="uf-num" c={status.down} fw={700} style={{ whiteSpace: 'nowrap' }}>{dc}:✕</Text></Tooltip>
      )))}
    </Group>
  );
}

function ChildrenTable({ rows, probes }: { rows: CheckRow[]; probes: ProbeStatus[] }) {
  const nav = useNavigate();
  return (
    <Box className="uf-scroll-x">
      <Table className="uf-table" verticalSpacing="sm" miw={700}>
        <Table.Tbody>
          {rows.map((r) => (
            <Table.Tr key={r.id} {...clickableRow(() => nav(`/checks/${enc(r.id)}`))}>
              <Table.Td w={170}><VerdictBadge kind={r.verdict} region={r.region} size="sm" /></Table.Td>
              <Table.Td><Text size="sm" fw={600}>{r.name}</Text><Text size="xs" c="dimmed" ff="monospace">{r.target}</Text></Table.Td>
              <Table.Td><ProbeDots row={r} probes={probes} /></Table.Td>
              <Table.Td ta="right"><Text className="uf-num" size="sm" c={uptimeColor(r.uptime_24h)} fw={600}>{pct(r.uptime_24h)}</Text></Table.Td>
              <Table.Td ta="right"><Text className="uf-num" size="sm">{ms(r.latency_24h)}</Text></Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Box>
  );
}

export default function CheckDetail() {
  const typeLabel = useTypeLabel();
  const { id = '' } = useParams();
  const t = useT(L);
  const c = useCommon();
  const { lang } = useLang();
  const nav = useNavigate();
  const pal = usePalette();
  const meta = useMeta();
  const q = useCheck(id);
  // A click on a pulse bar opens the check at that time: ?at=<unix seconds>.
  const [params] = useSearchParams();
  const at = Number(params.get('at')) || 0;
  const w = useWindow('24h', at ? { from: at - 3600, to: Math.min(at + 5400, Math.floor(Date.now() / 1000)) } : null);
  const series = useCheckSeries(id, w.q);
  const uptime = useUptime(id);
  const [editing, setEditing] = useState(false);
  const [picked, setPicked] = useState<string[] | null>(null);

  const row = q.data?.check;
  const parentLabel = useParentLabels();
  const allProbes = q.data?.probes ?? [];
  const probes = useMemo(() => allProbes.filter((p) => row?.probes.some((c) => c.probe === p.id)), [allProbes, row]);
  const colors = useProbeColors(allProbes);
  const shown = picked ?? probes.slice(0, 8).map((p) => p.id);

  const latency = useMemo(() => {
    const data = series.data;
    if (!data) return null;
    const lines: LineSeries[] = probes
      .filter((p) => shown.includes(p.id) && data.probes[p.id]?.length)
      .map((p) => {
        const pts = data.probes[p.id];
        const single = shown.length === 1;
        return {
          name: p.name,
          color: colors.get(p.id)!,
          data: pts.map((x) => [x.t * 1000, x.lat ?? null] as [number, number | null]),
          band: single ? pts.map((x) => [x.t * 1000, x.lat_min ?? null, x.lat_max ?? null] as [number, number | null, number | null]) : undefined,
          peak: pts.map((x) => [x.t * 1000, x.lat_max ?? null] as [number, number | null]),
        };
      });
    return lines.length ? lines : null;
  }, [series.data, probes, shown, colors]);

  // Incident periods shade the charts, the way Zabbix draws problems on a graph.
  const marks = useMemo(() => (q.data?.incidents ?? []).map((i) => ({
    from: Date.parse(i.started), to: i.resolved ? Date.parse(i.resolved) : Date.now(), color: kindColor(i.resolved ? i.worst : i.kind), label: i.kind,
  })), [q.data?.incidents]);

  // Last measured download speed per probe (heavy runs only; a stalled run isn't a speed).
  const lastSpeed = useMemo(() => {
    const out = new Map<string, number>();
    for (const [pid, pts] of Object.entries(series.data?.probes ?? {})) {
      for (let i = pts.length - 1; i >= 0; i--) {
        if (pts[i].bps && pts[i].up > 0.5) {
          out.set(pid, pts[i].bps!);
          break;
        }
      }
    }
    return out;
  }, [series.data]);

  const speed = useMemo(() => {
    const data = series.data;
    if (!data || row?.type !== 'xray') return null;
    // Speed tests are sparse and noisy: average them into ~60 slots.
    const slots = 60, step = (data.to - data.from) / slots;
    const smooth = (pts: Point[]) => {
      const acc = Array.from({ length: slots }, () => ({ s: 0, n: 0 }));
      for (const x of pts) {
        if (!x.bps || x.up < 0.5) continue;
        const i = Math.min(slots - 1, Math.max(0, Math.floor((x.t - data.from) / step)));
        acc[i].s += x.bps;
        acc[i].n++;
      }
      return acc.map((a, i) => [(data.from + (i + 0.5) * step) * 1000, a.n ? (a.s / a.n * 8) / 1e6 : null] as [number, number | null]);
    };
    const lines: LineSeries[] = probes
      .filter((p) => shown.includes(p.id))
      .map((p) => ({ name: p.name, color: colors.get(p.id)!, data: smooth(data.probes[p.id] ?? []) }))
      .filter((l) => l.data.some((x) => x[1] != null));
    return lines.length ? lines : null;
  }, [series.data, probes, shown, colors, row?.type]);

  if (q.isLoading) return <Stack><Skeleton h={60} /><Skeleton h={100} /><Skeleton h={300} /></Stack>;
  if (!q.data) {
    // Only a 404 means the check is gone; anything else is a failed request.
    if (q.error instanceof ApiError && q.error.status === 404) return <PageHeader title={t('not_found')} />;
    return <Stack><PageHeader title={id} /><QueryError error={q.error} onRetry={() => q.refetch()} /></Stack>;
  }
  const { config, managed, incidents, children } = q.data;
  const isAdmin = meta.data?.user.role === 'admin';
  const isChild = !!row?.parent;
  const configId = isChild ? row!.parent! : id;
  const failing = row?.probes.filter((p) => p.failing).length ?? 0;
  const maxBps = Math.max(0, ...lastSpeed.values());

  const setPaused = async (paused: boolean) => {
    try {
      await api('POST', `/api/v1/checks/${enc(configId)}/${paused ? 'pause' : 'resume'}`);
      await queryClient.invalidateQueries();
    } catch (e) {
      notifyError(e);
    }
  };
  const remove = () =>
    modals.openConfirmModal({
      title: t('delete'),
      children: <Text size="sm">{t('confirm', config.name || configId)}</Text>,
      labels: { confirm: t('delete'), cancel: c('cancel') },
      confirmProps: { color: 'red' },
      onConfirm: async () => {
        try {
          await api('DELETE', `/api/v1/checks/${enc(configId)}`);
          await queryClient.invalidateQueries();
          notifyOk(t('deleted'));
          nav('/checks');
        } catch (e) {
          notifyError(e);
        }
      },
    });

  const actions = isAdmin && !managed ? (
    <>
      <Button variant="default" leftSection={<IconPencil size={16} />} onClick={() => setEditing(true)}>{isChild ? t('edit_sub') : t('edit')}</Button>
      <Menu position="bottom-end">
        <Menu.Target><Button variant="default" px="xs" aria-label={t('more')}><IconDots size={18} /></Button></Menu.Target>
        <Menu.Dropdown>
          {config.paused
            ? <Menu.Item leftSection={<IconPlayerPlay size={16} />} onClick={() => setPaused(false)}>{t('resume')}</Menu.Item>
            : <Menu.Item leftSection={<IconPlayerPause size={16} />} onClick={() => setPaused(true)}>{t('pause')}</Menu.Item>}
          <Menu.Item color="red" leftSection={<IconTrash size={16} />} onClick={remove}>{t('delete')}</Menu.Item>
        </Menu.Dropdown>
      </Menu>
    </>
  ) : managed ? <Badge variant="outline" color="gray">{c('managed')}</Badge> : undefined;

  const subtitle = (
    <Group gap="xs" component="span">
      {row && <VerdictBadge kind={row.verdict} region={row.region} />}
      <Badge variant="default" radius="sm">{typeLabel(row?.type ?? config.type)}</Badge>
      {row?.target && <Text span ff="monospace" size="sm" c="dimmed">{row.target}</Text>}
      {!!row?.parents?.length && <Text span size="sm" c="dimmed">· {t('depends', row.parents.map(parentLabel).join(', '))}{row.parents_auto ? ` (${t('depends_auto')})` : ''}</Text>}
      {(row?.group || config.group) && <Text span size="sm" c="dimmed">· {row?.group || config.group}</Text>}
    </Group>
  );

  return (
    <Stack gap="xl">
      <Box>
        <Breadcrumbs mb="xs" separator="›">
          <Anchor component={Link} to="/checks" size="sm">{t('checks')}</Anchor>
          {isChild && <Anchor component={Link} to={`/checks/${enc(row!.parent!)}`} size="sm">{config.name || row!.parent}</Anchor>}
          <Text size="sm" c="dimmed">{row?.name ?? config.name ?? id}</Text>
        </Breadcrumbs>
        <PageHeader title={row?.name ?? config.name ?? id} subtitle={subtitle} right={actions} />
      </Box>

      {config.paused && <Alert color="gray" icon={<IconPlayerPause size={16} />}>{t('paused_note')}</Alert>}
      {row?.incident && <IncidentCard inc={row.incident} canAct={isAdmin} />}

      {row?.subscription ? (
        <Section title={`${t('children')} · ${children.length}`}>
          <Card padding={0}><ChildrenTable rows={children} probes={allProbes} /></Card>
        </Section>
      ) : row && (
        <>
          <Tiles cols={4}>
            <StatCard icon={IconActivity} color="forge" label={t('uptime24')} value={pct(row.uptime_24h)} />
            <StatCard icon={IconClockHour4} color="blue" label={t('latency24')} value={ms(row.latency_24h)} sub={t('every', shortDuration(parseGoDuration(row.interval), lang))} />
            <StatCard icon={IconServer2} color={failing ? 'red' : 'green'} label={t('failing')} value={`${failing} / ${row.probes.length}`} sub={failing ? undefined : t('all_ok')} />
            {row.type === 'xray'
              ? <StatCard icon={IconGauge} color="violet" label={t('speed')} value={maxBps ? bitrate(maxBps) : '—'} />
              : <StatCard icon={IconTrendingUp} color="violet" label={t('interval')} value={shortDuration(parseGoDuration(row.interval), lang)} />}
          </Tiles>

          <Section title={t('uptime90')}>
            <Card><UptimeBars days={uptime.data} loading={uptime.isLoading} /></Card>
          </Section>

          <Section title={t('latency')} right={<WindowControl range={w.range} setRange={w.setRange} zoom={w.zoom} reset={w.reset} />}>
              <Stack gap="sm">
                {probes.length > 8 && (
                  <Stack gap={6}>
                    <Text size="xs" c="dimmed">{t('only')}</Text>
                    <Chip.Group multiple value={shown} onChange={(v) => setPicked(v.slice(0, 8))}>
                      <Group gap={6}>{probes.map((p) => <Chip key={p.id} value={p.id} size="xs">{p.name}</Chip>)}</Group>
                    </Chip.Group>
                  </Stack>
                )}
                {series.isLoading ? <Skeleton h={300} radius="lg" /> : latency && series.data ? (
                  <MetricCard title={t('latency_by_probe')} series={latency} pal={pal} format={ms} axisFormat={(v) => `${Math.round(v)}`} height={280}
                    from={series.data.from * 1000} to={series.data.to * 1000} group={`check-${id}`} onZoom={w.onZoom} marks={marks} />
                ) : <Card><Text c="dimmed" size="sm">{t('no_data')}</Text></Card>}
              </Stack>
          </Section>

          <Section title={t('timeline')} right={<Text size="xs" c="dimmed">{t('timeline_hint')}</Text>}>
            <Card>{series.data ? <Timeline series={series.data.probes} probes={probes} from={series.data.from} to={series.data.to} /> : <Skeleton h={120} />}</Card>
          </Section>

          {speed && series.data && (
            <MetricCard title={t('throughput')} series={speed} pal={pal} format={(v) => `${v.toFixed(1)} ${t('mbps')}`} axisFormat={(v) => `${Math.round(v)}`}
              height={240} from={series.data.from * 1000} to={series.data.to * 1000} group={`check-${id}`} onZoom={w.onZoom} />
          )}

          <Section title={t('probes_now')}>
            <Card padding={0}>
              <Box className="uf-scroll-x">
                <Table className="uf-table" verticalSpacing="sm" miw={760}>
                  <Table.Thead>
                    <Table.Tr>
                      <Table.Th>{t('probe')}</Table.Th><Table.Th>{t('state')}</Table.Th><Table.Th ta="right">{c('latency')}</Table.Th>
                      {row.type === 'mtproto' ? <Table.Th>{t('dcs')}</Table.Th> : <Table.Th ta="right">{t('ttfb')}</Table.Th>}{row.type === 'xray' && <Table.Th ta="right">{t('speed')}</Table.Th>}
                      <Table.Th ta="right">{t('uptime24')}</Table.Th><Table.Th>{t('checked')}</Table.Th>
                    </Table.Tr>
                  </Table.Thead>
                  <Table.Tbody>
                    {row.probes.map((cell) => {
                      const p = allProbes.find((x) => x.id === cell.probe);
                      const state = !cell.status ? t('nodata') : cell.failing ? t('down') : cell.status === 'down' ? t('pending') : t('up');
                      const col = !cell.status ? status.unknown : cell.failing ? status.down : cell.status === 'down' ? status.partial : status.ok;
                      return (
                        <Table.Tr key={cell.probe}>
                          <Table.Td>
                            <Group gap={8} wrap="nowrap">
                              <Box w={10} h={10} style={{ borderRadius: 3, background: colors.get(cell.probe), flex: 'none' }} />
                              <Anchor component={Link} to={`/probes/${enc(cell.probe)}`} c="var(--mantine-color-text)"><ProbeName name={p?.name ?? cell.probe} region={p?.region} /></Anchor>
                            </Group>
                          </Table.Td>
                          <Table.Td>
                            <Group gap={6} wrap="nowrap">
                              <Box w={8} h={8} style={{ borderRadius: 8, background: col, flex: 'none' }} />
                              <Text size="sm" style={{ whiteSpace: 'nowrap' }}>{state}</Text>
                              {cell.status === 'down' && <Tooltip label={cell.error ?? cell.reason} multiline maw={360}><Text size="xs" c="dimmed" truncate maw={240}>{reasonText(cell.reason, cell.error, lang)}</Text></Tooltip>}
                            </Group>
                          </Table.Td>
                          <Table.Td ta="right"><Text size="sm" className="uf-num" style={{ whiteSpace: 'nowrap' }}>{cell.status === 'up' ? ms(cell.latency) : '—'}</Text></Table.Td>
                          {row.type === 'mtproto' ? <Table.Td><DCCells cell={cell} /></Table.Td> : <Table.Td ta="right"><Text size="sm" className="uf-num" style={{ whiteSpace: 'nowrap' }}>{cell.ttfb ? ms(cell.ttfb) : '—'}</Text></Table.Td>}
                          {row.type === 'xray' && <Table.Td ta="right"><Text size="sm" className="uf-num" style={{ whiteSpace: 'nowrap' }}>{lastSpeed.get(cell.probe) ? bitrate(lastSpeed.get(cell.probe)) : '—'}</Text></Table.Td>}
                          <Table.Td ta="right"><Text size="sm" className="uf-num" c={uptimeColor(cell.uptime_24h)} fw={600}>{pct(cell.uptime_24h)}</Text></Table.Td>
                          <Table.Td><Text size="xs" c="dimmed" style={{ whiteSpace: 'nowrap' }}>{ago(cell.at, lang)}</Text></Table.Td>
                        </Table.Tr>
                      );
                    })}
                  </Table.Tbody>
                </Table>
              </Box>
            </Card>
          </Section>
        </>
      )}

      <Section title={t('history')}>
        <Card padding={0}><History incidents={incidents} /></Card>
      </Section>

      {editing && <CheckForm opened={editing} onClose={() => setEditing(false)} initial={config} />}
      <Title order={6} c="dimmed" fw={400} ff="monospace" fz={11}>{configId}</Title>
    </Stack>
  );
}
