import { Box, Checkbox, Group, Text, UnstyledButton } from '@mantine/core';
import {
  IconActivity, IconChevronDown, IconChevronRight, IconDatabase, IconLock, IconPlug, IconServer2, IconShieldLock, IconWorld, type Icon,
} from '@tabler/icons-react';
import type { ReactNode } from 'react';
import { pct } from '../../format';
import { status } from '../../theme';
import { TelegramLogo } from '../TelegramLogo';
import type { CheckGroup } from './grouping';
import type { ChecksT, Count } from './text';

const icons: Record<string, Icon> = {
  http: IconWorld, keyword: IconWorld, tcp: IconPlug, ping: IconServer2, tls: IconLock, dns: IconDatabase,
  xray: IconShieldLock, subscription: IconShieldLock,
};

export function TypeIcon({ type, size = 18 }: { type: string; size?: number }) {
  if (type === 'mtproto') return <TelegramLogo size={size} />;
  const I = icons[type] ?? IconActivity;
  return <I size={size} stroke={1.7} />;
}

/**
 * CheckGroupCard is one section of the checks page: a summary header that
 * folds the rows away, and for admins a checkbox that selects the whole group.
 */
export function CheckGroupCard({ group, open, onToggle, t, count, select, children }: {
  group: CheckGroup; open: boolean; onToggle: () => void; t: ChecksT; count: Count;
  /** How many of the group's selectable checks are selected; absent for viewers. */
  select?: { selected: number; total: number; set: (on: boolean) => void };
  children: ReactNode;
}) {
  const real = group.rows.filter((r) => !r.subscription).length + group.rows.filter((r) => r.subscription).reduce((n, r) => n + (r.children ?? 0), 0);
  return (
    <Box className="uf-check-group" data-problems={group.problems > 0 || undefined}>
      <Box className="uf-check-group-bar">
        {select && select.total > 0 && (
          <Checkbox size="xs" pl={16} aria-label={t('select_group', group.title)}
            checked={select.selected === select.total} indeterminate={select.selected > 0 && select.selected < select.total}
            onChange={() => select.set(select.selected < select.total)} />
        )}
        <UnstyledButton className="uf-check-group-head" onClick={onToggle} aria-expanded={open}>
          {open ? <IconChevronDown size={16} /> : <IconChevronRight size={16} />}
          <TypeIcon type={group.type} />
          <Text fw={700} fz={15} truncate>{group.title}</Text>
          <Text size="sm" c="dimmed" style={{ flex: 'none' }}>{count(real, 'w_check')}</Text>
          <Text size="sm" fw={group.problems ? 650 : 500} c={group.problems ? status.down : status.ok} style={{ flex: 'none' }}>
            · {group.problems ? t('n_problems', group.problems) : t('all_ok')}
          </Text>
          <Group gap={6} ml="auto" wrap="nowrap" visibleFrom="sm">
            <Text size="sm" c="dimmed">{t('uptime')}</Text>
            <Text size="sm" fw={700} className="uf-num">{pct(group.uptime)}</Text>
          </Group>
        </UnstyledButton>
      </Box>
      {open && <Box>{children}</Box>}
    </Box>
  );
}
