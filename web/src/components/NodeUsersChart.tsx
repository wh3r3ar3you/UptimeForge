import { Card, Skeleton, Text } from '@mantine/core';
import { useMemo } from 'react';
import { useRemnawaveUsers, type RemnawaveNode } from '../api';
import { flag, num } from '../format';
import { useT } from '../i18n';
import { usePalette, useWindow, type LineSeries } from './Chart';
import { MetricCard, WindowControl } from './MetricCard';
import { Section } from './Cards';

const L = {
  ru: { title: 'Онлайн по нодам', sub: 'по данным панели, каждые 30 секунд', empty: 'История копится с момента подключения панели', top: 'топ {0} по онлайну сейчас' },
  en: { title: 'Users online by node', sub: 'from the panel, every 30 seconds', empty: 'History builds up from the moment the panel is connected', top: 'top {0} by users now' },
};

const MAX_SERIES = 8;

/**
 * NodeUsersChart is what the panel itself does not show: users online per
 * node over time. A ban shows here as one line falling while the others
 * hold; the evening as all of them falling together.
 */
export function NodeUsersChart({ nodes }: { nodes: RemnawaveNode[] }) {
  const t = useT(L);
  const pal = usePalette();
  const w = useWindow('24h');
  const q = useRemnawaveUsers(w.q);
  const series = useMemo(() => {
    const data = q.data?.nodes ?? {};
    const top = [...nodes].filter((n) => data[n.uuid]?.length).sort((a, b) => b.usersOnline - a.usersOnline).slice(0, MAX_SERIES);
    return top.map((n, i): LineSeries => ({
      name: `${flag(n.countryCode)} ${n.name}`.trim(),
      color: pal.series[i % pal.series.length],
      data: data[n.uuid].map((p) => [p.t * 1000, p.avg] as [number, number | null]),
      peak: data[n.uuid].map((p) => [p.t * 1000, p.max] as [number, number | null]),
    }));
  }, [q.data, nodes, pal]);

  return (
    <Section title={t('title')} right={<WindowControl range={w.range} setRange={w.setRange} zoom={w.zoom} reset={w.reset} />}>
      {q.isLoading ? <Skeleton h={300} radius="lg" /> : !series.length || !q.data ? (
        <Card><Text c="dimmed" size="sm">{t('empty')}</Text></Card>
      ) : (
        <MetricCard
          title={t('title')} sub={`${t('sub')} · ${t('top', series.length)}`} series={series} pal={pal} height={280}
          format={(v) => num(Math.round(v))} from={q.data.from * 1000} to={q.data.to * 1000}
          group="rw-users" onZoom={w.onZoom}
        />
      )}
    </Section>
  );
}
