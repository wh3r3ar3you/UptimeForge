import { Box, NumberInput, Select, Switch, Text } from '@mantine/core';
import { IconGauge } from '@tabler/icons-react';
import { useMeta, type HostMetric, type HostRule, type HubConfig } from '../api';
import { useT } from '../i18n';
import { compactDuration, SettingsCard } from './SettingsKit';

const L = {
  ru: {
    title: 'Пороги серверов', desc: 'Тревога, если метрика держится выше порога дольше указанного времени. Короткий всплеск тревогой не считается, как у триггеров Zabbix.',
    metric: 'Метрика', above: 'Порог', for: 'Дольше чем', now: 'сразу', per_core: 'на ядро',
    cpu: 'Загрузка ЦП', cpu_d: 'всё время, кроме простоя и ожидания диска',
    mem: 'Память', mem_d: 'занято без кэша',
    disk: 'Диск', disk_d: 'корневой раздел',
    swap: 'Swap', swap_d: 'доля занятого swap',
    steal: 'Steal', steal_d: 'гипервизор отдаёт процессор соседям',
    iowait: 'I/O wait', iowait_d: 'процессор ждёт диск',
    load: 'Load average', load_d: 'за 1 минуту, в пересчёте на ядро',
    old: 'Steal, iowait и swap присылают агенты начиная с этой версии хаба.',
  },
  en: {
    title: 'Server thresholds', desc: 'Alert when a metric stays above the threshold for longer than the window. A short spike is not an alert, as with Zabbix triggers.',
    metric: 'Metric', above: 'Threshold', for: 'For longer than', now: 'at once', per_core: 'per core',
    cpu: 'CPU usage', cpu_d: 'all time except idle and disk wait',
    mem: 'Memory', mem_d: 'used, cache excluded',
    disk: 'Disk', disk_d: 'the root filesystem',
    swap: 'Swap', swap_d: 'share of swap in use',
    steal: 'Steal', steal_d: 'the hypervisor gives the CPU to neighbours',
    iowait: 'I/O wait', iowait_d: 'the CPU waits on the disk',
    load: 'Load average', load_d: '1-minute, per core',
    old: 'Steal, iowait and swap come from agents of this hub version on.',
  },
};

const WINDOWS = ['0s', '1m', '2m', '5m', '10m', '15m', '30m', '1h'];

/**
 * HostRulesCard edits the server thresholds. The hub ships defaults
 * (served in meta); the draft keeps the full list once anything changes, so
 * what is saved is exactly what is shown.
 */
export function HostRulesCard({ draft, update, managed }: { draft: HubConfig; update: (fn: (d: HubConfig) => void) => void; managed?: boolean }) {
  const t = useT(L);
  const meta = useMeta();
  const defaults = meta.data?.host_rules ?? [];
  const rules: HostRule[] = defaults.map((d) => draft.alerts.host?.find((r) => r.metric === d.metric) ?? d);
  if (!rules.length) return null;

  const set = (metric: HostMetric, patch: Partial<HostRule>) => update((d) => {
    d.alerts.host = rules.map((r) => (r.metric === metric ? { ...r, ...patch } : r));
  });
  const windows = (v: string) => {
    const opts = WINDOWS.map((w) => ({ value: w, label: w === '0s' ? t('now') : w }));
    if (!WINDOWS.includes(v)) opts.push({ value: v, label: v });
    return opts;
  };

  return (
    <SettingsCard icon={IconGauge} title={t('title')} description={t('desc')} managed={managed}>
      <Box className="uf-rules">
        <Text size="xs" c="dimmed" fw={600} className="uf-rules-head">{t('metric')}</Text>
        <Text size="xs" c="dimmed" fw={600} className="uf-rules-head">{t('above')}</Text>
        <Text size="xs" c="dimmed" fw={600} className="uf-rules-head">{t('for')}</Text>
        {rules.map((r) => {
          const load = r.metric === 'load';
          const win = compactDuration(r.for ?? '0s') || '0s';
          return (
            <Box key={r.metric} className="uf-rules-row" data-off={r.off || undefined}>
              <Switch
                checked={!r.off}
                disabled={managed}
                onChange={(e) => set(r.metric, { off: !e.currentTarget.checked })}
                label={<Text size="sm" fw={600}>{t(r.metric)}</Text>}
                description={t(`${r.metric}_d` as 'cpu_d')}
              />
              <NumberInput
                size="sm"
                value={r.above}
                disabled={managed || r.off}
                min={load ? 0.1 : 1}
                max={load ? 100 : 99}
                step={load ? 0.1 : 5}
                decimalScale={load ? 2 : 0}
                rightSection={<Text size="xs" c="dimmed" pr={6}>{load ? t('per_core') : '%'}</Text>}
                rightSectionWidth={load ? 70 : 28}
                onChange={(v) => typeof v === 'number' && v > 0 && set(r.metric, { above: v })}
                styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)' } }}
              />
              <Select
                size="sm"
                value={win}
                disabled={managed || r.off}
                data={windows(win)}
                allowDeselect={false}
                onChange={(v) => v && set(r.metric, { for: v })}
              />
            </Box>
          );
        })}
      </Box>
      <Text size="xs" c="dimmed">{t('old')}</Text>
    </SettingsCard>
  );
}
