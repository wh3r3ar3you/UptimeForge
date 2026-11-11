import { Badge, Box, Group, Text, ThemeIcon, Tooltip } from '@mantine/core';
import {
  IconBan, IconCircleCheck, IconGauge, IconTrendingDown, IconCircleX, IconHelpCircle, IconPlayerPause, IconPlugConnectedX, IconAlertTriangle, IconWorldOff, IconServerOff,
} from '@tabler/icons-react';
import type { Kind } from '../api';
import { useCommon } from '../i18n';
import { status } from '../theme';

export function kindColor(k?: Kind | string): string {
  switch (k) {
    case 'ok': return status.ok;
    case 'partial': return status.partial;
    case 'blocked': return status.blocked;
    case 'down': case 'users_drop': case 'node_down': return status.down;
    case 'probe_offline': case 'probe_no_net': case 'host': return status.partial;
    default: return status.unknown;
  }
}

const icons: Record<string, typeof IconCircleCheck> = {
  ok: IconCircleCheck, partial: IconAlertTriangle, blocked: IconBan, down: IconCircleX,
  unknown: IconHelpCircle, paused: IconPlayerPause, probe_offline: IconPlugConnectedX, probe_no_net: IconWorldOff, host: IconGauge, users_drop: IconTrendingDown, node_down: IconServerOff,
};

export function KindIcon({ kind, size = 16 }: { kind?: Kind | string; size?: number }) {
  const I = icons[kind ?? 'unknown'] ?? IconHelpCircle;
  return <I size={size} color={kindColor(kind)} stroke={2} />;
}

/** VerdictBadge always pairs the status colour with an icon and a word. */
export function VerdictBadge({ kind, region, size = 'md' }: { kind?: Kind | string; region?: string; size?: 'sm' | 'md' | 'lg' }) {
  const c = useCommon();
  const label = kind === 'blocked' && region ? `${c('blocked')} · ${region.toUpperCase()}` : c((kind ?? 'unknown') as 'ok');
  const col = kindColor(kind);
  return (
    <Badge
      size={size}
      variant="light"
      leftSection={<KindIcon kind={kind} size={size === 'sm' ? 12 : 14} />}
      styles={{ root: { background: `${col}1f`, color: 'var(--mantine-color-text)', border: `1px solid ${col}40`, textTransform: 'none', fontWeight: 600 } }}
    >
      {label}
    </Badge>
  );
}

/** Dot is the compact state mark for dense tables; the tooltip carries the words. */
export function Dot({ color, pulse, label, size = 9 }: { color: string; pulse?: boolean; label?: string; size?: number }) {
  const dot = (
    <Box
      component="span"
      className={pulse ? 'uf-pulse' : undefined}
      style={{ display: 'inline-block', width: size, height: size, borderRadius: size, background: color, flex: 'none', ['--uf-pulse' as string]: color }}
    />
  );
  return label ? <Tooltip label={label}>{dot}</Tooltip> : dot;
}

export function OnlineDot({ online, internet = true }: { online: boolean; internet?: boolean }) {
  const c = useCommon();
  if (!online) return <Dot color={status.down} label={c('offline')} />;
  if (!internet) return <Dot color={status.partial} label={c('probe_no_net')} />;
  return <Dot color={status.ok} pulse label={c('online')} />;
}

export function IconTile({ icon: I, color }: { icon: typeof IconCircleCheck; color: string }) {
  return (
    <ThemeIcon size={44} radius="md" variant="light" color={color} style={{ border: `1px solid ${color}33`, background: `${color}14` }}>
      <I size={22} stroke={1.7} color={color} />
    </ThemeIcon>
  );
}

export function Legend({ items }: { items: { color: string; label: string }[] }) {
  return (
    <Group gap="md">
      {items.map((i) => (
        <Group key={i.label} gap={6} wrap="nowrap">
          <Box style={{ width: 10, height: 10, borderRadius: 3, background: i.color }} />
          <Text size="xs" c="dimmed">{i.label}</Text>
        </Group>
      ))}
    </Group>
  );
}
