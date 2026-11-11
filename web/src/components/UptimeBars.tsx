import { Box, Group, Skeleton, Stack, Text, Tooltip } from '@mantine/core';
import dayjs from 'dayjs';
import type { UptimeDay } from '../api';
import { duration, pct } from '../format';
import { useLang, useT } from '../i18n';
import { status } from '../theme';

const L = {
  ru: { nodata: 'Нет данных', down: 'Простой', degraded: 'Деградация', incidents: 'Инцидентов', ago: '{0} дн. назад', today: 'Сегодня', avg: 'аптайм за {0} дн.' },
  en: { nodata: 'No data', down: 'Downtime', degraded: 'Degraded', incidents: 'Incidents', ago: '{0} days ago', today: 'Today', avg: 'uptime over {0} days' },
};

function barColor(d: UptimeDay) {
  if (d.up == null) return 'var(--uf-track)';
  switch (d.worst) {
    case 'down': return status.down;
    case 'blocked': return status.blocked;
    case 'partial': return status.partial;
  }
  return status.ok;
}

/** UptimeBars: one bar per day, coloured by the worst verdict of that day. */
export function UptimeBars({ days, loading, height = 30, showScale = true }: { days?: UptimeDay[]; loading?: boolean; height?: number; showScale?: boolean }) {
  const t = useT(L);
  const { lang } = useLang();
  if (loading || !days) return <Skeleton h={height} radius="sm" />;
  const known = days.filter((d) => d.up != null);
  const avg = known.length ? known.reduce((s, d) => s + (d.up ?? 0), 0) / known.length : null;
  return (
    <Stack gap={6}>
      <Box style={{ display: 'flex', gap: 2, height, alignItems: 'stretch' }}>
        {days.map((d) => (
          <Tooltip
            key={d.day}
            label={
              <Stack gap={2}>
                <Text size="xs" fw={600}>{dayjs(d.day).format('DD.MM.YYYY')}</Text>
                {d.up == null ? <Text size="xs">{t('nodata')}</Text> : (
                  <>
                    <Text size="xs">{pct(d.up, 3)}</Text>
                    {d.down_s > 0 && <Text size="xs">{t('down')}: {duration(d.down_s, lang)}</Text>}
                    {d.degraded_s > 0 && <Text size="xs">{t('degraded')}: {duration(d.degraded_s, lang)}</Text>}
                    {d.incidents > 0 && <Text size="xs">{t('incidents')}: {d.incidents}</Text>}
                  </>
                )}
              </Stack>
            }
          >
            <Box style={{ flex: 1, minWidth: 2, borderRadius: 3, background: barColor(d), opacity: d.up == null ? 1 : 0.92 }} />
          </Tooltip>
        ))}
      </Box>
      {showScale && (
        <Group justify="space-between">
          <Text size="xs" c="dimmed">{t('ago', days.length)}</Text>
          {avg != null && <Text size="xs" c="dimmed"><Text span ff="monospace" fw={600} c="var(--mantine-color-text)">{pct(avg, 3)}</Text> {t('avg', days.length)}</Text>}
          <Text size="xs" c="dimmed">{t('today')}</Text>
        </Group>
      )}
    </Stack>
  );
}
