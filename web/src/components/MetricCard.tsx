import { Badge, Box, Card, CloseButton, Group, SegmentedControl, Text, Tooltip, UnstyledButton } from '@mantine/core';
import { IconZoomIn } from '@tabler/icons-react';
import { useMemo, useState, type ReactNode } from 'react';
import { useT } from '../i18n';
import { Chart, LIVE_RANGE, RANGES, seriesStats, timeOption, type LineSeries, type Pal } from './Chart';

const L = {
  ru: {
    last: 'сейчас', min: 'мин', avg: 'сред', max: 'макс', no_data: 'Нет данных за этот период',
    zoomed: 'Окно', reset: 'Сбросить', hint: 'Выделите участок графика мышью, чтобы приблизить', hide: 'Скрыть или показать',
    m1: '1 мин', h1: '1ч', h6: '6ч', h24: '24ч', d7: '7д', d30: '30д', m1_hint: 'Последняя минута в реальном времени, точка в секунду',
  },
  en: {
    last: 'last', min: 'min', avg: 'avg', max: 'max', no_data: 'No data for this period',
    zoomed: 'Window', reset: 'Reset', hint: 'Drag across a chart to zoom in', hide: 'Hide or show',
    m1: '1 min', h1: '1h', h6: '6h', h24: '24h', d7: '7d', d30: '30d', m1_hint: 'The last minute in real time, a point a second',
  },
};

const rangeKey: Record<string, 'm1' | 'h1' | 'h6' | 'h24' | 'd7' | 'd30'> = { [LIVE_RANGE]: 'm1', '1h': 'h1', '6h': 'h6', '24h': 'h24', '7d': 'd7', '30d': 'd30' };

function fmtWindow(from: number, to: number) {
  const p = (n: number) => String(n).padStart(2, '0');
  const f = new Date(from * 1000), t = new Date(to * 1000);
  const day = (d: Date) => `${p(d.getDate())}.${p(d.getMonth() + 1)}`;
  const hm = (d: Date) => `${p(d.getHours())}:${p(d.getMinutes())}`;
  return day(f) === day(t) ? `${day(f)} ${hm(f)} – ${hm(t)}` : `${day(f)} ${hm(f)} – ${day(t)} ${hm(t)}`;
}

/** WindowControl picks the chart window: presets, or shows the dragged one with a way back. */
export function WindowControl({ range, setRange, zoom, reset, extra, ranges = RANGES }: {
  range: string; setRange: (r: string) => void; zoom: { from: number; to: number } | null; reset: () => void; extra?: ReactNode;
  /** The presets offered; servers put the live LIVE_RANGE first. */
  ranges?: string[];
}) {
  const t = useT(L);
  const live = range === LIVE_RANGE && !zoom;
  return (
    <Group gap="xs" wrap="wrap" justify="flex-end">
      {extra}
      {live ? null : zoom ? (
        <Badge size="lg" radius="md" variant="light" color="blue" leftSection={<IconZoomIn size={14} />}
          rightSection={<CloseButton size="xs" variant="transparent" aria-label={t('reset')} onClick={reset} />}
          styles={{ label: { textTransform: 'none', fontFamily: 'var(--mantine-font-family-monospace)', fontWeight: 500 } }}>
          {fmtWindow(zoom.from, zoom.to)}
        </Badge>
      ) : (
        <Tooltip label={t('hint')} openDelay={500}>
          <Box c="dimmed" style={{ display: 'flex' }}><IconZoomIn size={16} /></Box>
        </Tooltip>
      )}
      <SegmentedControl size="xs" radius="md" value={zoom ? '' : range} onChange={setRange}
        data={ranges.map((r) => ({ value: r, label: r === LIVE_RANGE ? <Tooltip label={t('m1_hint')} openDelay={500}><span>{t('m1')}</span></Tooltip> : t(rangeKey[r]) }))} />
    </Group>
  );
}

/**
 * MetricCard is one chart with a Zabbix-style legend under it: last, min,
 * average and max of every series over the window. A click on a row hides
 * the series, and the stats follow what is visible.
 */
export function MetricCard({
  title, sub, series, pal, format, axisFormat, from, to, group, onZoom, min = 0, max, interval, lines, marks, height = 200, right, empty,
}: {
  title: ReactNode; sub?: ReactNode; series: LineSeries[]; pal: Pal; format: (v: number) => string; axisFormat?: (v: number) => string;
  from: number; to: number; group?: string; onZoom?: (from: number, to: number) => void; min?: number;
  max?: number | ((v: { max: number }) => number); interval?: number; marks?: { from: number; to: number; color: string; label: string }[]; lines?: { value: number; label: string }[]; height?: number; right?: ReactNode; empty?: boolean;
}) {
  const t = useT(L);
  const [hidden, setHidden] = useState<Set<string>>(new Set());
  const shown = useMemo(() => series.filter((s) => !hidden.has(s.name)), [series, hidden]);
  const option = useMemo(
    () => timeOption({ series: shown, pal, format, axisFormat, from, to, min, max, interval, lines, marks, legend: false, zoom: !!onZoom }),
    [shown, pal, format, axisFormat, from, to, min, max, interval, lines, marks, onZoom],
  );
  const stats = useMemo(() => series.map((s) => ({ s, st: seriesStats(s) })), [series]);
  const hasData = stats.some((x) => x.st.last != null);
  const toggle = (name: string) => setHidden((h) => {
    const n = new Set(h);
    if (n.has(name)) n.delete(name);
    else if (series.length - n.size > 1) n.add(name); // never hide the last one
    return n;
  });
  const cell = (v: number | null) => <Text size="xs" ff="monospace" ta="right" style={{ whiteSpace: 'nowrap' }}>{v == null ? '—' : format(v)}</Text>;

  return (
    <Card padding="md" className="uf-metric">
      <Group justify="space-between" gap="xs" mb={6} wrap="nowrap" align="flex-start">
        <Box style={{ minWidth: 0 }}>
          <Text fw={650} size="sm" truncate>{title}</Text>
          {sub && <Text size="xs" c="dimmed" truncate>{sub}</Text>}
        </Box>
        {right}
      </Group>
      {empty || !hasData ? (
        <Box h={height} style={{ display: 'grid', placeItems: 'center' }}><Text size="sm" c="dimmed">{t('no_data')}</Text></Box>
      ) : (
        <>
          <Chart option={option} height={height} group={group} onZoom={onZoom} />
          <Box className="uf-legend" mt={6}>
            <div className="uf-legend-head">
              <span />
              <Text size="xs" c="dimmed" ta="right">{t('last')}</Text>
              <Text size="xs" c="dimmed" ta="right">{t('min')}</Text>
              <Text size="xs" c="dimmed" ta="right">{t('avg')}</Text>
              <Text size="xs" c="dimmed" ta="right">{t('max')}</Text>
            </div>
            {stats.map(({ s, st }) => (
              <UnstyledButton key={s.name} className="uf-legend-row" data-off={hidden.has(s.name) || undefined} onClick={() => toggle(s.name)} title={t('hide')}>
                <Group gap={8} wrap="nowrap" style={{ minWidth: 0 }}>
                  <span className="uf-swatch" style={{ background: s.color }} />
                  <Text size="xs" truncate>{s.name}</Text>
                </Group>
                {cell(st.last)}{cell(st.min)}{cell(st.avg)}{cell(st.max)}
              </UnstyledButton>
            ))}
          </Box>
        </>
      )}
    </Card>
  );
}
