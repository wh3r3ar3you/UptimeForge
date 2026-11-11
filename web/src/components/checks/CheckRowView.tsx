import { ActionIcon, Badge, Box, Checkbox, Group, Menu, Text, Tooltip } from '@mantine/core';
import { IconChevronDown, IconChevronRight, IconCopy, IconDots, IconExternalLink, IconPencil, IconPlayerPause, IconPlayerPlay, IconTrash } from '@tabler/icons-react';
import { memo, type KeyboardEvent } from 'react';
import type { Bar, CheckRow, ProbeStatus } from '../../api';
import { ms, pct } from '../../format';
import { useCommon } from '../../i18n';
import { status } from '../../theme';
import { Dot } from '../Status';
import { isProblem } from './grouping';
import { PulseBar } from './PulseBar';
import type { ChecksT, Count } from './text';

export interface RowActions {
  open: (r: CheckRow, at?: number) => void;
  edit: (r: CheckRow) => void;
  duplicate: (r: CheckRow) => void;
  pause: (r: CheckRow, paused: boolean) => void;
  remove: (r: CheckRow) => void;
}

export function verdictColor(r: CheckRow): string {
  if (r.paused) return status.paused;
  if (r.verdict === 'down' || r.verdict === 'blocked') return status.down;
  if (r.verdict === 'partial') return status.partial;
  return r.verdict === 'ok' ? status.ok : status.unknown;
}

function probeSummary(r: CheckRow, probes: Map<string, ProbeStatus>): string {
  return r.probes.map((c) => `${probes.get(c.probe)?.name ?? c.probe}: ${!c.status ? '—' : c.failing ? '✗' : c.status === 'up' ? '✓' : '!'}`).join(' · ');
}

/** onKey opens a focused row with Enter or Space, like a link; keys inside its controls stay theirs. */
function onKey(open: () => void) {
  return (e: KeyboardEvent<HTMLElement>) => {
    if (e.target !== e.currentTarget || (e.key !== 'Enter' && e.key !== ' ')) return;
    e.preventDefault();
    open();
  };
}

/**
 * CheckRowView is one check: state, name and tags, target, pulse, uptime and
 * latency, and its menu. It is memoized: every prop is either the row as the
 * query cache shares it (unchanged rows keep their identity across a
 * refetch) or stable, so a poll re-renders only the rows that changed.
 */
export const CheckRowView = memo(function CheckRowView({
  row, bars, from, step, probes, t, count, typeLabel, selected, onSelect, actions, isAdmin, indent, expanded, onToggle,
}: {
  row: CheckRow; bars?: Bar[]; from: number; step: number; probes: Map<string, ProbeStatus>; t: ChecksT; count: Count; typeLabel: (s: string) => string;
  selected: boolean; onSelect: (id: string, on: boolean) => void; actions: RowActions; isAdmin: boolean; indent?: boolean;
  /** Set for a subscription: whether its configs are shown. */
  expanded?: boolean; onToggle?: (id: string) => void;
}) {
  const c = useCommon();
  const color = verdictColor(row);
  const open = () => actions.open(row);
  return (
    <Box className="uf-check-row" data-problem={isProblem(row) || undefined} data-paused={row.paused || undefined}
      onClick={open} onKeyDown={onKey(open)} tabIndex={0} aria-label={row.name}>
      <Box onClick={(e) => e.stopPropagation()} style={{ display: 'flex', alignItems: 'center' }}>
        {isAdmin && !row.managed && <Checkbox size="xs" checked={selected} onChange={(e) => onSelect(row.id, e.currentTarget.checked)} aria-label={row.name} />}
      </Box>
      <Tooltip label={probeSummary(row, probes) || typeLabel(row.type)} openDelay={300} multiline maw={360}>
        <Box style={{ display: 'flex', justifyContent: 'center' }}><Dot color={color} pulse={row.verdict === 'ok' && !row.paused} size={10} /></Box>
      </Tooltip>
      <Box style={{ minWidth: 0, paddingLeft: indent ? 22 : 0 }}>
        <Group gap={6} wrap="nowrap">
          {expanded != null && onToggle && (
            <ActionIcon size="xs" variant="subtle" color="gray" aria-expanded={expanded} aria-label={t(expanded ? 'collapse' : 'expand')}
              onClick={(e) => { e.stopPropagation(); onToggle(row.id); }}>
              {expanded ? <IconChevronDown size={14} /> : <IconChevronRight size={14} />}
            </ActionIcon>
          )}
          <Text fw={650} size="sm" truncate>{row.name}</Text>
          <Badge size="xs" variant="default" radius="sm" style={{ flex: 'none', textTransform: 'none' }}>{typeLabel(row.type)}</Badge>
          {(row.tags ?? []).slice(0, 3).map((tag) => <Badge key={tag} size="xs" variant="light" color="gray" radius="sm" style={{ flex: 'none', textTransform: 'none' }}>#{tag}</Badge>)}
          {row.managed && (
            <Tooltip label={c('managed_hint')}>
              <Badge size="xs" variant="outline" color="gray" style={{ flex: 'none' }}>{t('yaml')}</Badge>
            </Tooltip>
          )}
        </Group>
        <Text size="xs" c="dimmed" ff="monospace" truncate>{row.subscription ? count(row.children ?? 0, 'w_config') : row.target}</Text>
      </Box>
      <Box visibleFrom="md"><PulseBar bars={bars} from={from} step={step} t={t} onPick={(at) => actions.open(row, at)} /></Box>
      <Text size="sm" fw={650} ta="right" className="uf-num" c={row.uptime_24h == null ? 'dimmed' : row.uptime_24h >= 0.999 ? status.ok : row.uptime_24h >= 0.99 ? status.partial : status.down}>
        {pct(row.uptime_24h)}
      </Text>
      <Text size="sm" ta="right" c="dimmed" className="uf-num" visibleFrom="sm">{ms(row.latency_24h)}</Text>
      <Box onClick={(e) => e.stopPropagation()} onKeyDown={(e) => e.stopPropagation()}>
        <Menu position="bottom-end" withinPortal>
          <Menu.Target><ActionIcon variant="subtle" color="gray" aria-label={t('menu', row.name)}><IconDots size={16} /></ActionIcon></Menu.Target>
          <Menu.Dropdown>
            <Menu.Item leftSection={<IconExternalLink size={15} />} onClick={open}>{t('open')}</Menu.Item>
            {isAdmin && !row.managed && (
              <>
                <Menu.Item leftSection={<IconPencil size={15} />} onClick={() => actions.edit(row)}>{t('edit')}</Menu.Item>
                {!row.parent && <Menu.Item leftSection={<IconCopy size={15} />} onClick={() => actions.duplicate(row)}>{t('duplicate')}</Menu.Item>}
                <Menu.Item leftSection={row.paused ? <IconPlayerPlay size={15} /> : <IconPlayerPause size={15} />} onClick={() => actions.pause(row, !row.paused)}>
                  {row.paused ? t('resume') : t('pause')}
                </Menu.Item>
                <Menu.Divider />
                <Menu.Item color="red" leftSection={<IconTrash size={15} />} onClick={() => actions.remove(row)}>{t('delete')}</Menu.Item>
              </>
            )}
          </Menu.Dropdown>
        </Menu>
      </Box>
    </Box>
  );
});
