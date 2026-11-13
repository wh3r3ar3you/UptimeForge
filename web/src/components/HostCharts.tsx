import { Button, Card, Group, SegmentedControl, SimpleGrid, Skeleton, Stack, Text, Tooltip } from '@mantine/core';
import { useMemo, useState } from 'react';
import { useLiveMetrics, useProbeMetrics, type SysPoint } from '../api';
import { bitrate, bytes, compact } from '../format';
import { useT } from '../i18n';
import { Section } from './Cards';
import { LIVE_RANGE, RANGES, useWindow, usePalette, type LineSeries } from './Chart';
import { MetricCard, WindowControl } from './MetricCard';

const L = {
  ru: {
    charts: 'Графики', avg: 'Средние', peaks: 'Пики', peaks_hint: 'Пики показывают максимум внутри каждой точки: короткие всплески, которые среднее сглаживает',
    no_data: 'Нет данных за этот период',
    cpu: 'Процессор', user: 'user', system: 'system', irq: 'другое', idle: 'простой', iowait: 'iowait', steal: 'steal', total: 'всего', peak: 'пик',
    cpu_sub: 'доля времени в каждом состоянии · steal: время, отданное соседям по гипервизору', load: 'Load average', cores: 'ядер: {0}',
    mem: 'Память', used: 'занято', cache: 'кэш', swap: 'swap', of: 'из {0}',
    disk: 'Место на диске', disk_used: 'занято',
    io: 'Дисковый I/O', read: 'чтение', write: 'запись',
    busy: 'Занятость диска', util: 'диск занят', ops: 'Операции диска', rops: 'чтение', wops: 'запись',
    net: 'Трафик', rx: 'вход', tx: 'выход',
    pkts: 'Пакеты', rx_pkts: 'вход', tx_pkts: 'выход', errs: 'ошибки и сбросы',
    conn: 'Соединения и процессы', tcp: 'TCP-сокеты', procs: 'процессы', blocked: 'ждут диск',
    new_agent: 'Нужен агент новее',
    live_wait: 'Ждём первую точку…', live_slow: 'Агент обновляется раз в 15 с — обновите агента для живого режима',
    live_offline: 'Агент не на связи: живых точек нет', live_lost: 'Связь с хабом прервалась, переподключаемся…',
    live_failed: 'Живой поток недоступен: хаб отказал или проба больше не отдаёт метрики.', live_fallback: 'Показать последний час',
  },
  en: {
    charts: 'Charts', avg: 'Average', peaks: 'Peaks', peaks_hint: 'Peaks show the maximum inside each point: short spikes that the average smooths away',
    no_data: 'No data for this period',
    cpu: 'CPU', user: 'user', system: 'system', irq: 'other', idle: 'idle', iowait: 'iowait', steal: 'steal', total: 'total', peak: 'peak',
    cpu_sub: 'share of time in each state · steal: time the hypervisor gave to neighbours', load: 'Load average', cores: '{0} cores',
    mem: 'Memory', used: 'used', cache: 'cache', swap: 'swap', of: 'of {0}',
    disk: 'Disk space', disk_used: 'used',
    io: 'Disk I/O', read: 'read', write: 'write',
    busy: 'Disk busy', util: 'disk busy', ops: 'Disk operations', rops: 'read', wops: 'write',
    net: 'Traffic', rx: 'in', tx: 'out',
    pkts: 'Packets', rx_pkts: 'in', tx_pkts: 'out', errs: 'errors and drops',
    conn: 'Connections and processes', tcp: 'TCP sockets', procs: 'processes', blocked: 'waiting on disk',
    new_agent: 'Needs a newer agent',
    live_wait: 'Waiting for the first point…', live_slow: 'The agent updates every 15 s — update the agent for live mode',
    live_offline: 'The agent is offline: no live points', live_lost: 'Lost the connection to the hub, reconnecting…',
    live_failed: 'The live stream is unavailable: the hub refused it or the probe no longer serves metrics.', live_fallback: 'Show the last hour',
  },
};

type Pt = [number, number | null];

const pctFmt = (v: number) => `${v.toFixed(v < 10 ? 1 : 0)}%`;
const rate = (v: number) => `${bytes(v, v >= 1024 * 1024 ? 1 : 0)}/s`;
const perSec = (v: number) => `${compact(v)}/s`;
const count = (v: number) => compact(v);
const axisBits = (v: number) => bitrate(v).replace(/.0+ /, " ");

/** niceBytes rounds a byte ceiling up to a quarter-power of two (32 GiB, 6 GiB), so axis steps read cleanly. */
function niceBytes(v: number) {
  if (v <= 0) return 1;
  const step = 2 ** Math.max(0, Math.floor(Math.log2(v)) - 2);
  return Math.ceil(v / step) * step;
}

/**
 * HostCharts is the full picture of one server over a window. The live range
 * streams the last minute instead of reading storage; only servers (agents
 * and the hub itself) have it, never checkers or via probes.
 */
export function HostCharts({ id, cores, online }: { id: string; cores?: number; online: boolean }) {
  const t = useT(L);
  const pal = usePalette();
  const w = useWindow('24h');
  const live = w.range === LIVE_RANGE && !w.zoom;
  const metrics = useProbeMetrics(id, w.q, !live);
  const stream = useLiveMetrics(id, live);
  const [mode, setMode] = useState<'avg' | 'peak'>('avg');
  const peaks = mode === 'peak' && !live;
  const stored = metrics.data?.points;
  const pts = useMemo(() => (live ? stream.points : stored ?? []), [live, stream.points, stored]);
  const from = live ? stream.now - 60_000 : (metrics.data?.from ?? 0) * 1000;
  const to = live ? stream.now : (metrics.data?.to ?? 0) * 1000;
  const group = `host-${id}`;

  const c = useMemo(() => {
    const s = pal.series;
    const line = (f: (p: SysPoint) => number | null | undefined): Pt[] => pts.map((p) => [p.t * 1000, f(p) ?? null]);
    const x = (key: string, peak = true) => (p: SysPoint) => (peaks && peak && p.x?.[`${key}_max`] != null ? p.x[`${key}_max`] : p.x?.[key]);
    const peakOf = (key: string): Pt[] => line((p) => p.x?.[`${key}_max`]);
    const hasExt = pts.some((p) => p.x);
    const ser = (name: string, color: string, data: Pt[], more: Partial<LineSeries> = {}): LineSeries => ({ name, color, data, ...more });

    const cpu: LineSeries[] = hasExt
      ? [
          // As in Beszel: every state stacked to 100%, idle on top, so steal and
          // iowait read as a share of the whole CPU, not of the busy part.
          ser(t('user'), s[0], line(x('cpu_user', false)), { stack: 'cpu' }),
          ser(t('system'), s[1], line(x('cpu_system', false)), { stack: 'cpu' }),
          ser(t('iowait'), s[3], line(x('cpu_iowait', false)), { stack: 'cpu', peak: peakOf('cpu_iowait') }),
          ser(t('steal'), s[4], line(x('cpu_steal', false)), { stack: 'cpu', peak: peakOf('cpu_steal') }),
          ser(t('irq'), s[2], line(x('cpu_irq', false)), { stack: 'cpu' }),
          ser(t('idle'), pal.muted, line((p) => {
            const b = p.x;
            if (!b || b.cpu_user == null) return null;
            return Math.max(0, 100 - b.cpu_user - b.cpu_system - (b.cpu_iowait ?? 0) - (b.cpu_steal ?? 0) - (b.cpu_irq ?? 0));
          }), { stack: 'cpu', faint: true }),
        ]
      : [
          ser(t('total'), s[0], line((p) => (peaks ? p.cpu_max : p.cpu)), { area: true, peak: line((p) => p.cpu_max) }),
        ];

    const memTotal = pts.length ? pts[pts.length - 1].mem_total : 0;
    const diskTotal = pts.length ? pts[pts.length - 1].disk_total : 0;
    const mem: LineSeries[] = [
      ser(t('used'), s[0], line((p) => p.mem), { stack: hasExt ? 'mem' : undefined, area: true }),
      ...(hasExt ? [
        ser(t('cache'), s[2], line(x('mem_cache', false)), { stack: 'mem' }),
        ser(t('swap'), s[1], line(x('swap_used')), { area: false, peak: peakOf('swap_used') }),
      ] : []),
    ];
    const maxLoad = Math.max(0, ...pts.map((p) => p.load1));
    return {
      hasExt, maxLoad,
      memTotal, diskTotal,
      cpu,
      load: [
        ser('1m', s[0], line((p) => p.load1)), ser('5m', s[1], line((p) => p.load5)), ser('15m', s[2], line((p) => p.load15)),
      ],
      mem,
      disk: [ser(t('disk_used'), s[6], line((p) => p.disk), { area: true })],
      io: [
        ser(t('read'), s[0], line(x('disk_read')), { peak: peakOf('disk_read') }),
        ser(t('write'), s[1], line(x('disk_write')), { peak: peakOf('disk_write') }),
      ],
      busy: [
        ser(t('util'), s[0], line(x('disk_util')), { peak: peakOf('disk_util') }),
        ser(t('iowait'), s[3], line(x('cpu_iowait')), { peak: peakOf('cpu_iowait') }),
      ],
      ops: [
        ser(t('rops'), s[0], line(x('disk_rops')), { peak: peakOf('disk_rops') }),
        ser(t('wops'), s[1], line(x('disk_wops')), { peak: peakOf('disk_wops') }),
      ],
      net: [
        ser(t('rx'), s[0], line((p) => (peaks ? p.rx_max : p.rx)), { peak: line((p) => p.rx_max) }),
        ser(t('tx'), s[1], line((p) => (peaks ? p.tx_max : p.tx)), { peak: line((p) => p.tx_max) }),
      ],
      pkts: [
        ser(t('rx_pkts'), s[0], line(x('rx_pkts')), { peak: peakOf('rx_pkts') }),
        ser(t('tx_pkts'), s[1], line(x('tx_pkts')), { peak: peakOf('tx_pkts') }),
        ser(t('errs'), s[4], line(x('net_err')), { peak: peakOf('net_err') }),
      ],
      conn: [
        ser(t('tcp'), s[0], line(x('tcp')), { peak: peakOf('tcp') }),
        ser(t('procs'), s[2], line(x('procs')), { peak: peakOf('procs') }),
        ser(t('blocked'), s[3], line(x('procs_blocked')), { peak: peakOf('procs_blocked') }),
      ],
    };
  }, [pts, pal, peaks, t]);

  // A drag-zoom inside a sliding minute has nothing to zoom into.
  const common = { pal, from, to, group, onZoom: live ? undefined : w.onZoom };
  const toggle = live ? undefined : (
    <Tooltip label={t('peaks_hint')} multiline w={280} openDelay={400}>
      <SegmentedControl size="xs" radius="md" value={mode} onChange={(v) => setMode(v as 'avg' | 'peak')}
        data={[{ value: 'avg', label: t('avg') }, { value: 'peak', label: t('peaks') }]} />
    </Tooltip>
  );
  const newAgent = !c.hasExt ? <Text size="xs" c="dimmed">{t('new_agent')}</Text> : undefined;
  const up100 = ({ max }: { max: number }) => Math.min(100, max <= 4 ? 5 : max <= 8 ? 10 : Math.ceil(max / 20) * 20);

  // What the live range says instead of, or above, the charts. An offline
  // agent sends nothing either, so it must not be read as an old agent.
  const liveNote = !live ? null
    : !online ? t('live_offline')
    : stream.state === 'lost' ? t('live_lost')
    : stream.slow ? t('live_slow')
    : !pts.length ? t('live_wait')
    : null;

  const empty = (text: string | null) => <Card><Text c="dimmed" size="sm">{text}</Text></Card>;
  let body: React.ReactNode = null;
  if (live && stream.state === 'failed') {
    body = (
      <Card>
        <Group justify="space-between" gap="sm">
          <Text size="sm">{t('live_failed')}</Text>
          <Button size="xs" variant="default" onClick={() => w.setRange('1h')}>{t('live_fallback')}</Button>
        </Group>
      </Card>
    );
  } else if (live && !pts.length) body = empty(liveNote);
  else if (!live && metrics.isLoading) body = <Skeleton h={300} radius="lg" />;
  else if (!pts.length) body = empty(t('no_data'));

  return (
    <Section title={t('charts')} right={<WindowControl range={w.range} setRange={w.setRange} zoom={w.zoom} reset={w.reset} extra={toggle} ranges={[LIVE_RANGE, ...RANGES]} />}>
      {body ?? (
        <Stack gap="xs">
          {liveNote && <Text c="dimmed" size="sm">{liveNote}</Text>}
          <SimpleGrid cols={{ base: 1, lg: 2 }} spacing="md">
            <MetricCard {...common} title={t('cpu')} sub={c.hasExt ? t('cpu_sub') : undefined} series={c.cpu} format={pctFmt} axisFormat={(v) => `${v}%`} max={c.hasExt ? 100 : up100} interval={c.hasExt ? 25 : undefined} />
            <MetricCard {...common} title={t('load')} series={c.load} format={(v) => v.toFixed(2)}
              lines={cores && c.maxLoad >= cores * 0.5 ? [{ value: cores, label: t('cores', cores) }] : undefined}
              max={cores && c.maxLoad >= cores * 0.5 ? ({ max }) => Math.max(max, cores * 1.1) : undefined} />
            <MetricCard {...common} title={t('mem')} sub={c.memTotal ? t('of', bytes(c.memTotal)) : undefined} series={c.mem}
              format={(v) => bytes(v)} axisFormat={(v) => bytes(v, 0)} max={c.memTotal ? niceBytes(c.memTotal) : undefined} interval={c.memTotal ? niceBytes(c.memTotal) / 4 : undefined}
              lines={c.memTotal ? [{ value: c.memTotal, label: bytes(c.memTotal, 1) }] : undefined} />
            <MetricCard {...common} title={t('net')} series={c.net} format={bitrate} axisFormat={axisBits} />
            <MetricCard {...common} title={t('io')} series={c.io} format={rate} axisFormat={(v) => `${bytes(v, 0)}/s`} right={newAgent} empty={!c.hasExt} />
            <MetricCard {...common} title={t('busy')} series={c.busy} format={pctFmt} axisFormat={(v) => `${v}%`} max={up100} right={newAgent} empty={!c.hasExt} />
            <MetricCard {...common} title={t('pkts')} series={c.pkts} format={perSec} right={newAgent} empty={!c.hasExt} />
            <MetricCard {...common} title={t('conn')} series={c.conn} format={count} right={newAgent} empty={!c.hasExt} />
            <MetricCard {...common} title={t('ops')} series={c.ops} format={perSec} right={newAgent} empty={!c.hasExt} />
            <MetricCard {...common} title={t('disk')} sub={c.diskTotal ? t('of', bytes(c.diskTotal, 0)) : undefined} series={c.disk}
              format={(v) => bytes(v)} axisFormat={(v) => bytes(v, 0)} max={c.diskTotal ? niceBytes(c.diskTotal) : undefined} interval={c.diskTotal ? niceBytes(c.diskTotal) / 4 : undefined}
              lines={c.diskTotal ? [{ value: c.diskTotal, label: bytes(c.diskTotal, 0) }] : undefined} />
          </SimpleGrid>
        </Stack>
      )}
    </Section>
  );
}
