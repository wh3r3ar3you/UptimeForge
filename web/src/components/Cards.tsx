import { Box, Card, Group, Progress, SimpleGrid, Stack, Text, Title, Tooltip, type MantineColor } from '@mantine/core';
import { IconTrendingDown, IconTrendingUp, type Icon } from '@tabler/icons-react';
import type { ReactNode } from 'react';
import { status } from '../theme';

const tints: Record<string, string> = {
  forge: '#20cfae', blue: '#3b8cf0', green: '#2fb344', orange: '#f08c3b', red: '#e5534b', violet: '#8b7cf6',
  cyan: '#22b8cf', yellow: '#e0a800', pink: '#e64980', gray: '#8693a3',
};

/** StatCard: the Remnawave-style tile — tinted icon, label, monospace value, optional delta. */
export function StatCard({
  icon: I, color = 'forge', label, value, sub, delta, deltaGood = 'up', onClick,
}: {
  icon: Icon; color?: MantineColor; label: ReactNode; value: ReactNode; sub?: ReactNode;
  delta?: { value: string; negative: boolean; caption?: string }; deltaGood?: 'up' | 'down'; onClick?: () => void;
}) {
  const tint = tints[color as string] ?? color;
  const good = delta && (delta.negative ? deltaGood === 'down' : deltaGood === 'up');
  return (
    <Card padding="md" className="uf-card" onClick={onClick} style={{ cursor: onClick ? 'pointer' : undefined }}>
      <Group gap="md" wrap="nowrap" align="center">
        <Box className="uf-icon-tile" style={{ ['--tint' as string]: tint }}>
          <I size={22} stroke={1.7} />
        </Box>
        <Stack gap={2} style={{ minWidth: 0 }}>
          <Text size="sm" c="dimmed" fw={500} lineClamp={2} title={typeof label === 'string' ? label : undefined}>{label}</Text>
          <Text ff="monospace" fw={700} fz={{ base: 16, sm: 20 }} lh={1.2} style={{ overflowWrap: 'anywhere' }}>{value}</Text>
          {delta && (
            <Group gap={4} wrap="nowrap">
              {delta.negative ? <IconTrendingDown size={14} color={good ? status.ok : status.down} /> : <IconTrendingUp size={14} color={good ? status.ok : status.down} />}
              <Text size="xs" fw={600} c={good ? 'green.5' : 'red.5'}>{delta.value}</Text>
              {delta.caption && <Text size="xs" c="dimmed" truncate>{delta.caption}</Text>}
            </Group>
          )}
          {sub && !delta && <Text size="xs" c="dimmed" lineClamp={2}>{sub}</Text>}
        </Stack>
      </Group>
    </Card>
  );
}

export function Section({ title, right, children }: { title: ReactNode; right?: ReactNode; children: ReactNode }) {
  return (
    <Stack gap="sm">
      <Group justify="space-between" align="flex-end">
        <Title order={4} fw={650}>{title}</Title>
        {right}
      </Group>
      {children}
    </Stack>
  );
}

export function Tiles({ children, cols = 4 }: { children: ReactNode; cols?: number }) {
  return <SimpleGrid cols={{ base: 2, md: Math.min(cols, 3), lg: cols }} spacing={{ base: 'xs', sm: 'md' }}>{children}</SimpleGrid>;
}

/** Meter: Beszel-style inline usage bar. The fill carries severity, the track is a faint step of the same hue. */
export function Meter({ value, label, tooltip, width = 56 }: { value: number | null | undefined; label?: string; tooltip?: ReactNode; width?: number }) {
  if (value == null || !isFinite(value)) return <Text size="sm" c="dimmed">—</Text>;
  const v = Math.max(0, Math.min(100, value));
  const color = v >= 90 ? status.down : v >= 70 ? status.partial : status.ok;
  const body = (
    <Group gap={8} wrap="nowrap">
      <Text size="sm" ff="monospace" fw={500} w={46} ta="right">{label ?? `${v.toFixed(v < 10 ? 1 : 0)}%`}</Text>
      <Progress value={v} w={width} size={7} radius="xl" color={color} styles={{ root: { background: `${color}26` } }} />
    </Group>
  );
  return tooltip ? <Tooltip label={tooltip}>{body}</Tooltip> : body;
}

export function Empty({ icon: I, title, children }: { icon: Icon; title: ReactNode; children?: ReactNode }) {
  return (
    <Card padding="xl">
      <Stack align="center" gap="sm" py="xl">
        <Box className="uf-icon-tile" style={{ ['--tint' as string]: tints.forge, width: 56, height: 56 }}>
          <I size={28} stroke={1.5} />
        </Box>
        <Title order={4}>{title}</Title>
        {children}
      </Stack>
    </Card>
  );
}
