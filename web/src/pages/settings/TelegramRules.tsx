import { ActionIcon, Box, Button, Group, MultiSelect, NumberInput, Paper, Select, Stack, Switch, Text, TextInput, Tooltip } from '@mantine/core';
import { IconArrowDown, IconArrowUp, IconPlus, IconTrash } from '@tabler/icons-react';
import type { TelegramRoute, TelegramRule } from '../../api';
import { useT } from '../../i18n';
import { parseTopicLink } from './Telegram';

const L = {
  ru: {
    empty: 'Правил нет: сообщения идут по маршрутам событий ниже.', add: 'Добавить правило',
    tags: 'Метки (любая из)', groups: 'Группы (любая из)', events: 'События (любое из)', any: 'любые',
    severity: 'Важность', sev_any: 'любая', sev_critical: 'только критичные', sev_warning: 'предупреждения и выше',
    chat: 'Чат', main_chat: 'основной чат', topic: 'Тема', topic_ph: 'номер или ссылка', on: 'Слать', silent: 'Без звука',
    up: 'Выше', down: 'Ниже', remove: 'Удалить', rule: 'Правило {0}',
  },
  en: {
    empty: 'No rules: messages follow the event routes below.', add: 'Add rule',
    tags: 'Tags (any of)', groups: 'Groups (any of)', events: 'Events (any of)', any: 'any',
    severity: 'Severity', sev_any: 'any', sev_critical: 'critical only', sev_warning: 'warning and up',
    chat: 'Chat', main_chat: 'main chat', topic: 'Topic', topic_ph: 'number or link', on: 'Send', silent: 'Silent',
    up: 'Up', down: 'Down', remove: 'Remove', rule: 'Rule {0}',
  },
};

const num = (v: string | number) => {
  const n = typeof v === 'number' ? v : parseInt(v, 10);
  return Number.isFinite(n) && n !== 0 ? n : undefined;
};

/**
 * TelegramRules edits rules that route incidents by tag, group, event or
 * severity. They are checked top to bottom before the event routes; the
 * first that matches decides where a message goes.
 */
export function TelegramRules({ rules, onChange, tags, groups, events, eventLabel, disabled }: {
  rules: TelegramRule[]; onChange: (r: TelegramRule[]) => void; tags: string[]; groups: string[];
  events: readonly string[]; eventLabel: (ev: string) => string; disabled?: boolean;
}) {
  const t = useT(L);
  const set = (i: number, patch: Partial<TelegramRule>) => onChange(rules.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const setRoute = (i: number, patch: Partial<TelegramRoute>) => set(i, { route: { ...rules[i].route, ...patch } });
  const move = (i: number, d: -1 | 1) => {
    const next = [...rules];
    [next[i], next[i + d]] = [next[i + d], next[i]];
    onChange(next);
  };
  const opt = (xs: string[], cur?: string[]) => [...new Set([...xs, ...(cur ?? [])])].map((x) => ({ value: x, label: x }));

  return (
    <Stack gap="sm">
      {rules.length === 0 && <Text size="sm" c="dimmed">{t('empty')}</Text>}
      {rules.map((r, i) => (
        <Paper key={i} withBorder p="sm" radius="md" style={r.route.off ? { opacity: 0.6 } : undefined}>
          <Group justify="space-between" mb="xs">
            <Text size="sm" fw={650}>{t('rule', i + 1)}</Text>
            <Group gap={4}>
              <Tooltip label={t('up')}><ActionIcon variant="subtle" color="gray" disabled={disabled || i === 0} onClick={() => move(i, -1)} aria-label={t('up')}><IconArrowUp size={15} /></ActionIcon></Tooltip>
              <Tooltip label={t('down')}><ActionIcon variant="subtle" color="gray" disabled={disabled || i === rules.length - 1} onClick={() => move(i, 1)} aria-label={t('down')}><IconArrowDown size={15} /></ActionIcon></Tooltip>
              <Tooltip label={t('remove')}><ActionIcon variant="subtle" color="red" disabled={disabled} onClick={() => onChange(rules.filter((_, j) => j !== i))} aria-label={t('remove')}><IconTrash size={15} /></ActionIcon></Tooltip>
            </Group>
          </Group>
          <Box style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: 10 }}>
            <MultiSelect size="xs" label={t('tags')} placeholder={t('any')} data={opt(tags, r.tags)} value={r.tags ?? []} searchable disabled={disabled}
              onChange={(v) => set(i, { tags: v.length ? v : undefined })} />
            <MultiSelect size="xs" label={t('groups')} placeholder={t('any')} data={opt(groups, r.groups)} value={r.groups ?? []} searchable disabled={disabled}
              onChange={(v) => set(i, { groups: v.length ? v : undefined })} />
            <MultiSelect size="xs" label={t('events')} placeholder={t('any')} data={events.map((e) => ({ value: e, label: eventLabel(e) }))} value={r.events ?? []} disabled={disabled}
              onChange={(v) => set(i, { events: v.length ? v : undefined })} />
            <Select size="xs" label={t('severity')} allowDeselect={false} disabled={disabled} value={r.min_severity ?? ''}
              data={[{ value: '', label: t('sev_any') }, { value: 'critical', label: t('sev_critical') }, { value: 'warning', label: t('sev_warning') }]}
              onChange={(v) => set(i, { min_severity: (v || undefined) as TelegramRule['min_severity'] })} />
          </Box>
          <Group gap="md" mt="xs" align="flex-end" wrap="wrap">
            <NumberInput size="xs" label={t('chat')} hideControls allowDecimal={false} placeholder={t('main_chat')} value={r.route.chat_id ?? ''} disabled={disabled}
              onChange={(v) => setRoute(i, { chat_id: num(v) })} w={200} />
            <TextInput size="xs" label={t('topic')} placeholder={t('topic_ph')} value={r.route.topic ? String(r.route.topic) : ''} disabled={disabled} w={200}
              onChange={(e) => {
                const v = e.currentTarget.value.trim();
                const link = parseTopicLink(v);
                if (link) setRoute(i, link.chat ? { chat_id: link.chat, topic: link.topic } : { topic: link.topic });
                else if (/^\d*$/.test(v)) setRoute(i, { topic: num(v) });
              }} />
            <Switch size="sm" label={t('on')} checked={!r.route.off} disabled={disabled} onChange={(e) => { const on = e.currentTarget.checked; setRoute(i, { off: !on || undefined }); }} />
            <Switch size="sm" label={t('silent')} checked={!!r.route.silent} disabled={disabled} onChange={(e) => { const s = e.currentTarget.checked; setRoute(i, { silent: s || undefined }); }} />
          </Group>
        </Paper>
      ))}
      <Group>
        <Button size="xs" variant="light" leftSection={<IconPlus size={14} />} disabled={disabled} onClick={() => onChange([...rules, { route: {} }])}>{t('add')}</Button>
      </Group>
    </Stack>
  );
}
