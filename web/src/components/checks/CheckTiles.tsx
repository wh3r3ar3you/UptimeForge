import { Box, Group, SimpleGrid, Text, UnstyledButton } from '@mantine/core';
import type { Bar, CheckRow } from '../../api';
import { ms, pct } from '../../format';
import { Dot } from '../Status';
import { verdictColor } from './CheckRowView';
import { PulseBar } from './PulseBar';
import type { ChecksT } from './text';

/** CheckTiles is the dense view: one small card per check, for a wall screen. */
export function CheckTiles({ rows, bars, from, step, t, onOpen }: {
  rows: CheckRow[]; bars: Record<string, Bar[]>; from: number; step: number; t: ChecksT; onOpen: (r: CheckRow) => void;
}) {
  return (
    <SimpleGrid cols={{ base: 2, sm: 3, md: 4, xl: 6 }} spacing="sm" p="md" pt={4}>
      {rows.map((r) => (
        <UnstyledButton key={r.id} className="uf-check-tile" data-problem={['down', 'blocked', 'partial'].includes(r.verdict) || undefined} onClick={() => onOpen(r)}>
          <Group gap={8} wrap="nowrap">
            <Dot color={verdictColor(r)} size={10} pulse={r.verdict === 'ok' && !r.paused} />
            <Text fw={650} size="sm" truncate>{r.name}</Text>
          </Group>
          <Text size="xs" c="dimmed" mt={4} className="uf-num">{pct(r.uptime_24h)} · {ms(r.latency_24h)}</Text>
          <Box mt={6}><PulseBar bars={bars[r.id]} from={from} step={step} t={t} compact /></Box>
        </UnstyledButton>
      ))}
    </SimpleGrid>
  );
}
