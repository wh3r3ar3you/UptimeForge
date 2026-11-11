import { ActionIcon, Alert, Badge, Box, Group, ScrollArea, SegmentedControl, Select, Stack, Switch, Text, TextInput, Tooltip } from '@mantine/core';
import { IconArrowDown, IconArrowUp, IconEye, IconEyeOff } from '@tabler/icons-react';
import { useMemo } from 'react';
import { useRemnawave, type Location, type Locations, type RemnawaveNode } from '../api';
import { countryOptions, flag } from '../format';
import { useLang, useT } from '../i18n';

const L = {
  ru: {
    mode: 'Как показывать', m_country: 'По странам', m_node: 'По нодам', m_expand: 'Страны с раскрытием',
    m_country_d: 'Одна строка на страну: работает, если работают все её ноды.',
    m_node_d: 'Строка на каждую ноду под названием, которое вы зададите.',
    m_expand_d: 'Строка на страну; по клику раскрываются её ноды.',
    uptime: 'Полоса аптайма за 90 дней', nodes: 'Ноды из панели', nodes_hint: 'Клиенты видят только флаг, название для клиентов (или страну) и статус. Имена из панели, адреса и UUID на страницу не попадают.',
    client_name: 'Название для клиентов', country: 'Страна', hide: 'Скрыть со страницы', show: 'Показывать', hidden: 'скрыта',
    no_panel: 'Remnawave не подключена: подключите панель в настройках, и здесь появятся её ноды.',
    no_nodes: 'Панель пока не прислала ни одной ноды.', panel_error: 'Панель не отвечает: {0}', disabled: 'отключена в панели',
    move_up: 'Выше', move_down: 'Ниже',
  },
  en: {
    mode: 'Show as', m_country: 'By country', m_node: 'By node', m_expand: 'Countries, expandable',
    m_country_d: 'A row per country: up when all its nodes are.',
    m_node_d: 'A row per node, under the name you give it.',
    m_expand_d: 'A row per country; a click shows its nodes.',
    uptime: '90-day uptime bar', nodes: 'Panel nodes', nodes_hint: 'Clients see only the flag, the client name (or the country) and the status. Panel names, addresses and UUIDs never reach the page.',
    client_name: 'Name for clients', country: 'Country', hide: 'Hide from the page', show: 'Show', hidden: 'hidden',
    no_panel: 'Remnawave is not connected: connect the panel in settings and its nodes show up here.',
    no_nodes: 'The panel has not sent any nodes yet.', panel_error: 'The panel does not answer: {0}', disabled: 'disabled in the panel',
    move_up: 'Move up', move_down: 'Move down',
  },
};

/** ordered lists the panel's nodes in the page's order: the admin's first,
 *  then the rest in the panel's order, as the public page does. */
export function ordered(items: Location[], nodes: RemnawaveNode[]): { node: RemnawaveNode; item: Location }[] {
  const byId = new Map(nodes.map((n) => [n.uuid, n]));
  const seen = new Set<string>();
  const out: { node: RemnawaveNode; item: Location }[] = [];
  for (const it of [...items, ...nodes.map((n) => ({ node: n.uuid }))]) {
    const n = byId.get(it.node);
    if (!n || seen.has(it.node)) continue;
    seen.add(it.node);
    out.push({ node: n, item: it });
  }
  return out;
}

export function LocationsEditor({ value, onChange }: { value: Locations; onChange: (v: Locations) => void }) {
  const t = useT(L);
  const { lang } = useLang();
  const rw = useRemnawave();
  const nodes = rw.data?.nodes ?? [];
  const rows = ordered(value.items ?? [], nodes);
  const countries = useMemo(() => countryOptions(lang), [lang]);
  const regions = useMemo(() => new Intl.DisplayNames([lang], { type: 'region' }), [lang]);
  // Intl.DisplayNames throws on an empty or malformed code: a node without a country must not break the editor.
  const regionName = (cc: string) => {
    if (!/^[a-z]{2}$/i.test(cc)) return '';
    try {
      return regions.of(cc.toUpperCase()) ?? '';
    } catch {
      return '';
    }
  };
  const mode = value.mode ?? 'country';
  // Every edit stores the whole order, so what the admin sees is what clients get.
  const setRows = (next: { node: RemnawaveNode; item: Location }[]) =>
    onChange({ ...value, items: next.map(({ node, item }) => {
      const it: Location = { node: node.uuid };
      if (item.name?.trim()) it.name = item.name;
      if (item.country && item.country.toUpperCase() !== (node.countryCode ?? '').toUpperCase()) it.country = item.country.toUpperCase();
      if (item.hidden) it.hidden = true;
      return it;
    }) });
  const patch = (i: number, p: Partial<Location>) => setRows(rows.map((r, j) => (j === i ? { ...r, item: { ...r.item, ...p } } : r)));
  const move = (i: number, d: -1 | 1) => {
    const next = [...rows];
    [next[i], next[i + d]] = [next[i + d], next[i]];
    setRows(next);
  };

  return (
    <Stack gap="md">
      <Stack gap={4}>
        <Text size="sm" fw={500}>{t('mode')}</Text>
        <SegmentedControl fullWidth value={mode} onChange={(m) => onChange({ ...value, mode: m as Locations['mode'] })}
          data={[{ value: 'country', label: t('m_country') }, { value: 'node', label: t('m_node') }, { value: 'country_expand', label: t('m_expand') }]} />
        <Text size="xs" c="dimmed">{t(mode === 'node' ? 'm_node_d' : mode === 'country_expand' ? 'm_expand_d' : 'm_country_d')}</Text>
      </Stack>
      <Switch label={t('uptime')} checked={!!value.uptime} onChange={(e) => onChange({ ...value, uptime: e.currentTarget.checked || undefined })} />
      <Stack gap={6}>
        <Text size="sm" fw={500}>{t('nodes')}</Text>
        <Text size="xs" c="dimmed">{t('nodes_hint')}</Text>
        {rw.data && !rw.data.configured && <Alert color="yellow">{t('no_panel')}</Alert>}
        {rw.data?.error && <Alert color="red">{t('panel_error', rw.data.error)}</Alert>}
        {rw.data?.configured && !rw.data.error && rows.length === 0 && <Text size="sm" c="dimmed">{t('no_nodes')}</Text>}
        {rows.length > 0 && (
          <ScrollArea.Autosize mah={420} type="auto" offsetScrollbars>
            <Stack gap={6}>
              {rows.map(({ node, item }, i) => {
                const country = (item.country || node.countryCode || '').toLowerCase();
                const off = item.hidden || node.isDisabled;
                return (
                  <Box key={node.uuid} className="uf-loc-row" data-off={off || undefined}>
                    <Group gap="xs" wrap="nowrap" align="center">
                      <Text fz={26} lh={1} style={{ flex: 'none', width: 34, textAlign: 'center' }}>{flag(country) || '🏳️'}</Text>
                      <Stack gap={2} style={{ flex: '0 1 150px', minWidth: 0 }}>
                        <Text size="sm" fw={600} truncate>{node.name}</Text>
                        <Group gap={4}>
                          {item.hidden && <Badge size="xs" variant="light" color="gray">{t('hidden')}</Badge>}
                          {node.isDisabled && <Badge size="xs" variant="light" color="gray">{t('disabled')}</Badge>}
                        </Group>
                      </Stack>
                      <TextInput size="xs" style={{ flex: 1, minWidth: 120 }} aria-label={t('client_name')} placeholder={regionName(country) || t('client_name')}
                        value={item.name ?? ''} onChange={(e) => patch(i, { name: e.currentTarget.value })} />
                      <Select size="xs" w={170} aria-label={t('country')} data={countries} searchable value={country || null} allowDeselect={false}
                        onChange={(v) => v && patch(i, { country: v })} comboboxProps={{ withinPortal: true }} />
                      <Group gap={0} wrap="nowrap">
                        <Tooltip label={item.hidden ? t('show') : t('hide')}>
                          <ActionIcon variant="subtle" color="gray" onClick={() => patch(i, { hidden: !item.hidden || undefined })} aria-label={t('hide')}>
                            {item.hidden ? <IconEyeOff size={16} /> : <IconEye size={16} />}
                          </ActionIcon>
                        </Tooltip>
                        <ActionIcon variant="subtle" color="gray" disabled={i === 0} onClick={() => move(i, -1)} aria-label={t('move_up')}><IconArrowUp size={16} /></ActionIcon>
                        <ActionIcon variant="subtle" color="gray" disabled={i === rows.length - 1} onClick={() => move(i, 1)} aria-label={t('move_down')}><IconArrowDown size={16} /></ActionIcon>
                      </Group>
                    </Group>
                  </Box>
                );
              })}
            </Stack>
          </ScrollArea.Autosize>
        )}
      </Stack>
    </Stack>
  );
}
