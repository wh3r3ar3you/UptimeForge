import { Box, Text, UnstyledButton } from '@mantine/core';
import { IconAffiliate, IconArrowDownRight, IconArrowUpRight, IconChevronRight } from '@tabler/icons-react';
import { useMemo } from 'react';
import { Link } from 'react-router';
import { useRemnawaveUsers, type RemnawaveView } from '../api';
import { num } from '../format';
import { countWord, useLang, useT } from '../i18n';
import { status } from '../theme';

const L = {
  ru: {
    panel: 'Remnawave', online: 'Онлайн сейчас', of_users: 'из {0}', vs_usual: 'к среднему за сутки', usual: 'как обычно за сутки',
    nodes: 'Ноды на связи', nodes_ok: 'все на связи', nodes_down: '{0} не на связи', disabled: 'отключено {0}',
    today: 'Трафик сегодня', month: '30 дней: {0}', vs_yesterday: '{0} ко вчера',
    stale: 'Панель не отвечает, данные на {0}', no_data: 'Панель не отвечает', open: 'Открыть Remnawave',
    spark: 'Онлайн за последние сутки', node_word: 'нода|ноды|нод',
  },
  en: {
    panel: 'Remnawave', online: 'Online now', of_users: 'of {0}', vs_usual: 'vs the 24h average', usual: 'as usual for the day',
    nodes: 'Nodes connected', nodes_ok: 'all connected', nodes_down: '{0} down', disabled: '{0} disabled',
    today: 'Traffic today', month: '30 days: {0}', vs_yesterday: '{0} vs yesterday',
    stale: 'The panel does not answer, data as of {0}', no_data: 'The panel does not answer', open: 'Open Remnawave',
    spark: 'Users online over the last day', node_word: 'node|nodes',
  },
};

/** Total users online per poll over the last day: the per-node series summed bucket by bucket. */
function useOnlineSeries(enabled: boolean) {
  const q = useRemnawaveUsers('range=24h', enabled);
  return useMemo(() => {
    const sum = new Map<number, number>();
    for (const pts of Object.values(q.data?.nodes ?? {})) for (const p of pts) sum.set(p.t, (sum.get(p.t) ?? 0) + p.avg);
    return [...sum.entries()].sort((a, b) => a[0] - b[0]).map(([, v]) => v);
  }, [q.data]);
}

/** Sparkline: one 2px line in the primary colour, the last point marked; no axes, the number beside it says the value. */
function Sparkline({ values, label }: { values: number[]; label: string }) {
  if (values.length < 2) return null;
  const w = 132, h = 40, pad = 3;
  const min = Math.min(...values), max = Math.max(...values);
  const span = max - min || 1;
  const x = (i: number) => pad + (i / (values.length - 1)) * (w - pad * 2);
  const y = (v: number) => h - pad - ((v - min) / span) * (h - pad * 2);
  const line = values.map((v, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)},${y(v).toFixed(1)}`).join('');
  const area = `${line}L${x(values.length - 1).toFixed(1)},${h}L${x(0).toFixed(1)},${h}Z`;
  const last = values[values.length - 1];
  return (
    <svg className="uf-rw-spark" viewBox={`0 0 ${w} ${h}`} width={w} height={h} role="img" aria-label={label}>
      <path d={area} fill="var(--mantine-primary-color-filled)" opacity={0.1} />
      <path d={line} fill="none" stroke="var(--mantine-primary-color-filled)" strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" vectorEffect="non-scaling-stroke" />
      <circle cx={x(values.length - 1)} cy={y(last)} r={3} fill="var(--mantine-primary-color-filled)" />
    </svg>
  );
}

/**
 * RemnawaveHero opens the dashboard with the panel's numbers: users online
 * (with the last day as a sparkline and how now compares with it), nodes
 * connected, today's traffic. One quiet card, a link to the full page; the
 * only colour is a warning when nodes are down.
 */
export function RemnawaveHero({ rw }: { rw: RemnawaveView }) {
  const t = useT(L);
  const { lang } = useLang();
  const st = rw.stats;
  const series = useOnlineSeries(!!st);

  if (!st) {
    // Configured, but the panel has not answered yet: say so only when it failed.
    if (!rw.error) return null;
    return (
      <UnstyledButton component={Link} to="/remnawave" className="uf-rw-hero uf-rw-hero-empty">
        <Text size="sm" c="dimmed"><IconAffiliate size={15} style={{ verticalAlign: -2, marginRight: 6 }} />{t('panel')} · {t('no_data')}</Text>
      </UnstyledButton>
    );
  }

  const online = st.system.onlineStats.onlineNow;
  const usual = series.length > 1 ? series.reduce((s, v) => s + v, 0) / series.length : 0;
  const delta = usual > 0 ? (online - usual) / usual : null;
  const active = Math.max(0, st.nodesTotal - st.nodesDisabled);
  const down = Math.max(0, active - st.nodesOnline);
  const today = st.bandwidth.bandwidthLastTwoDays;
  const month = st.bandwidth.bandwidthLast30Days;
  const diff = today.difference?.trim();

  return (
    <UnstyledButton component={Link} to="/remnawave" className="uf-rw-hero" title={t('open')}>
      <Box className="uf-rw-cell uf-rw-main">
        <div className="uf-rw-label">
          <IconAffiliate size={14} stroke={1.8} />
          <span>{t('panel')}</span>
          <span className="uf-rw-dot" aria-hidden>·</span>
          <span>{t('online')}</span>
          <IconChevronRight size={14} className="uf-rw-go" aria-hidden />
        </div>
        <div className="uf-rw-main-row">
          <div style={{ minWidth: 0 }}>
            <div className="uf-rw-big">{num(online)}</div>
            <div className="uf-rw-sub">
              {delta != null && Math.abs(delta) >= 0.01 ? (
                <span className="uf-rw-delta">
                  {delta > 0 ? <IconArrowUpRight size={13} /> : <IconArrowDownRight size={13} />}
                  {`${delta > 0 ? '+' : '−'}${Math.round(Math.abs(delta) * 100)}%`} {t('vs_usual')}
                </span>
              ) : delta != null ? <span>{t('usual')}</span> : null}
              {delta != null && <span className="uf-rw-sep" aria-hidden>·</span>}
              <span>{t('of_users', num(st.system.users.totalUsers))}</span>
            </div>
          </div>
          <Sparkline values={series} label={t('spark')} />
        </div>
      </Box>

      <Box className="uf-rw-cell">
        <div className="uf-rw-label"><span>{t('nodes')}</span></div>
        <div className="uf-rw-value" style={down ? { color: status.partial } : undefined}>
          {num(st.nodesOnline)}<span className="uf-rw-of">/{num(active)}</span>
        </div>
        <div className="uf-rw-sub">
          {down
            ? <span style={{ color: status.partial, fontWeight: 600 }}>{t('nodes_down', `${down} ${countWord(lang, down, t('node_word'))}`)}</span>
            : <span>{t('nodes_ok')}</span>}
          {st.nodesDisabled > 0 && <><span className="uf-rw-sep" aria-hidden>·</span><span>{t('disabled', st.nodesDisabled)}</span></>}
        </div>
      </Box>

      <Box className="uf-rw-cell">
        <div className="uf-rw-label"><span>{t('today')}</span></div>
        <div className="uf-rw-value">{today.current || '—'}</div>
        <div className="uf-rw-sub">
          <span>{t('month', month.current || '—')}</span>
          {diff && <><span className="uf-rw-sep" aria-hidden>·</span><span>{t('vs_yesterday', diff.startsWith('-') ? diff.replace(/^-/, '−') : `+${diff}`)}</span></>}
        </div>
      </Box>
      {rw.error && rw.fetched && <div className="uf-rw-stale">{t('stale', new Date(rw.fetched).toLocaleTimeString(lang, { hour: '2-digit', minute: '2-digit' }))}</div>}
    </UnstyledButton>
  );
}
