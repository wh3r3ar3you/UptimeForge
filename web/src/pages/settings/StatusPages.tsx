import {
  ActionIcon, Alert, Badge, Box, Button, Card, Group, Modal, MultiSelect, SegmentedControl, SimpleGrid, Skeleton, Stack, Text, Textarea, TextInput,
  Tooltip,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import {
  IconAlertTriangle, IconArrowDown, IconArrowUp, IconExternalLink, IconPencil, IconPlus, IconTrash, IconWorldWww,
} from '@tabler/icons-react';
import { useGuardedClose } from '../../components/guards';
import { useMemo, useState, type FormEvent } from 'react';
import { api, enc, queryClient, useApiMutation, useChecks, useMeta, useStatusPages, type CheckRow, type StatusPage } from '../../api';
import { Empty } from '../../components/Cards';
import { LocationsEditor } from '../../components/LocationsEditor';
import { CopyBlock, notifyError, notifyOk, PageHeader } from '../../components/Misc';
import { plural, useCommon, useLang, useT } from '../../i18n';

const L = {
  ru: {
    title: 'Статус-страницы', subtitle: 'Публичные страницы для клиентов: только названия и состояние, без адресов, проб и ошибок',
    create: 'Создать страницу', edit: 'Изменить страницу', none: 'Статус-страниц пока нет',
    none_hint: 'Сделайте страницу для клиентов — например, «Статус VPN» с группами по странам. Её можно открыть без входа в UptimeForge.',
    slug: 'Адрес', slug_hint: 'Латиница, цифры и дефис: страница будет на /status/<адрес>', bad_slug: 'Только a–z, 0–9 и дефис, начиная с буквы или цифры',
    page_title: 'Заголовок', page_title_ph: 'Статус сервиса', description: 'Описание', description_ph: 'Необязательно. Например: «Если что-то не работает — пишите в поддержку»',
    groups: 'Группы', group_name: 'Название группы', group_ph: 'Например, «Германия»', checks: 'Проверки', checks_ph: 'Выберите проверки',
    add_group: 'Добавить группу', public_link: 'Публичная ссылка', open: 'Открыть', saved: 'Страница сохранена',
    sub_all: '{0} — все конфиги подписки', missing: '{0} (проверка удалена)', no_group: 'Без группы', unnamed: 'Без названия',
    summary_groups: '{0} {1}', summary_checks: '{0} {1}', required: 'Обязательное поле', slug_taken: 'Страница с таким адресом уже есть',
    kind: 'Что показывать', k_groups: 'Проверки по группам', k_locations: 'Локации VPN (Remnawave)',
    loc_badge: 'Локации', m_country: 'по странам', m_node: 'по нодам', m_country_expand: 'страны с раскрытием', hidden_n: 'скрыто: {0}',
    slug_moved: 'Публичная ссылка изменится: {0} перестанет открываться, страница будет на {1}. Обновите ссылку везде, где вы её публиковали.',
    move_up: 'Выше', move_down: 'Ниже', remove_group: 'Удалить группу',
  },
  en: {
    title: 'Status pages', subtitle: 'Public pages for customers: names and state only, no addresses, probes or errors',
    create: 'Create page', edit: 'Edit page', none: 'No status pages yet',
    none_hint: 'Make a page for customers — say, “VPN status” with a group per country. It opens without signing in.',
    slug: 'Address', slug_hint: 'Latin letters, digits and dashes: the page lives at /status/<address>', bad_slug: 'Only a–z, 0–9 and dashes, starting with a letter or digit',
    page_title: 'Title', page_title_ph: 'Service status', description: 'Description', description_ph: 'Optional. E.g. “If something is broken, contact support”',
    groups: 'Groups', group_name: 'Group name', group_ph: 'E.g. “Germany”', checks: 'Checks', checks_ph: 'Pick checks',
    add_group: 'Add group', public_link: 'Public link', open: 'Open', saved: 'Page saved',
    sub_all: '{0} — every config of the subscription', missing: '{0} (check deleted)', no_group: 'No group', unnamed: 'Untitled',
    summary_groups: '{0} {1}', summary_checks: '{0} {1}', required: 'Required', slug_taken: 'A page with this address already exists',
    kind: 'Shows', k_groups: 'Checks in groups', k_locations: 'VPN locations (Remnawave)',
    loc_badge: 'Locations', m_country: 'by country', m_node: 'by node', m_country_expand: 'countries, expandable', hidden_n: 'hidden: {0}',
    slug_moved: 'The public link changes: {0} stops working, the page moves to {1}. Update the link wherever you published it.',
    move_up: 'Move up', move_down: 'Move down', remove_group: 'Remove group',
  },
};

const SLUG = /^[a-z0-9][a-z0-9-]{0,62}$/;

type Draft = StatusPage & { original?: string };

function useCheckOptions(checks: CheckRow[], selected: string[]) {
  const t = useT(L);
  return useMemo(() => {
    const byGroup = new Map<string, { value: string; label: string }[]>();
    const known = new Set<string>();
    const push = (g: string, value: string, label: string) => {
      if (known.has(value)) return;
      known.add(value);
      byGroup.set(g, [...(byGroup.get(g) ?? []), { value, label }]);
    };
    const parents = new Map(checks.filter((c) => c.subscription).map((c) => [c.id, c]));
    for (const c of checks) {
      if (c.subscription) {
        push(c.group || t('no_group'), `${c.id}/`, t('sub_all', c.name));
        continue;
      }
      const parent = c.parent ? parents.get(c.parent) : undefined;
      push(parent ? parent.name : c.group || t('no_group'), c.id, c.name);
    }
    for (const id of selected) if (!known.has(id)) push('—', id, t('missing', id));
    return [...byGroup.entries()].map(([group, items]) => ({ group, items }));
  }, [checks, selected, t]);
}

function GroupEditor({
  g, i, n, checks, onChange, onMove, onRemove,
}: {
  g: StatusPage['groups'][number]; i: number; n: number; checks: CheckRow[];
  onChange: (g: StatusPage['groups'][number]) => void; onMove: (d: -1 | 1) => void; onRemove: () => void;
}) {
  const t = useT(L);
  const data = useCheckOptions(checks, g.checks ?? []);
  return (
    <Card padding="sm" withBorder style={{ background: 'light-dark(var(--mantine-color-gray-0), var(--mantine-color-dark-8))' }}>
      <Stack gap="xs">
        <Group gap="xs" wrap="nowrap" align="flex-end">
          <TextInput
            label={t('group_name')}
            placeholder={t('group_ph')}
            value={g.name}
            onChange={(e) => onChange({ ...g, name: e.currentTarget.value })}
            style={{ flex: 1 }}
          />
          <Group gap={2} wrap="nowrap" mb={2}>
            <ActionIcon variant="subtle" color="gray" size="lg" disabled={i === 0} onClick={() => onMove(-1)} aria-label={t('move_up')}><IconArrowUp size={16} /></ActionIcon>
            <ActionIcon variant="subtle" color="gray" size="lg" disabled={i === n - 1} onClick={() => onMove(1)} aria-label={t('move_down')}><IconArrowDown size={16} /></ActionIcon>
            <ActionIcon variant="subtle" color="red" size="lg" onClick={onRemove} aria-label={t('remove_group')}><IconTrash size={16} /></ActionIcon>
          </Group>
        </Group>
        <MultiSelect
          label={t('checks')}
          placeholder={g.checks?.length ? '' : t('checks_ph')}
          data={data}
          value={g.checks ?? []}
          onChange={(v) => onChange({ ...g, checks: v })}
          searchable
          clearable
          hidePickedOptions
          maxDropdownHeight={280}
          comboboxProps={{ withinPortal: true }}
        />
      </Stack>
    </Card>
  );
}

function Editor({ draft, onClose, existing }: { draft: Draft | null; onClose: () => void; existing: string[] }) {
  const t = useT(L);
  const c = useCommon();
  const checks = useChecks({ live: false }).data?.checks ?? [];
  const [d, setD] = useState<Draft | null>(draft);
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState(false);
  // Re-seed when a different page is opened.
  const [seed, setSeed] = useState(draft);
  if (draft !== seed) {
    setSeed(draft);
    setD(draft);
    setTried(false);
  }
  const guard = useGuardedClose(JSON.stringify(d) !== JSON.stringify(draft), onClose);
  if (!d) return null;
  const slugMoved = !!d.original && !!d.slug && d.slug !== d.original;

  const slugErr = !d.slug ? (tried ? t('required') : undefined)
    : !SLUG.test(d.slug) ? t('bad_slug')
    : d.slug !== d.original && existing.includes(d.slug) ? t('slug_taken') : undefined;
  const titleErr = tried && !d.title.trim() ? t('required') : undefined;
  const setGroups = (groups: StatusPage['groups']) => setD({ ...d, groups });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setTried(true);
    if (!d.slug || slugErr || !d.title.trim()) return;
    setBusy(true);
    try {
      const loc = d.kind === 'locations';
      const body: StatusPage = {
        slug: d.slug, title: d.title.trim(), description: d.description?.trim() || undefined,
        groups: loc ? [] : d.groups.filter((g) => g.name.trim() || g.checks.length).map((g) => ({ name: g.name.trim(), checks: g.checks })),
        kind: loc ? 'locations' : undefined, locations: loc ? { ...d.locations, items: d.locations?.items?.map((i) => ({ ...i, name: i.name?.trim() || undefined })) } : undefined,
      };
      await api('PUT', `/api/v1/status-pages/${enc(d.slug)}`, body);
      if (d.original && d.original !== d.slug) await api('DELETE', `/api/v1/status-pages/${enc(d.original)}`);
      await queryClient.invalidateQueries({ queryKey: ['status-pages'] });
      notifyOk(t('saved'));
      onClose();
    } catch (err) {
      notifyError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal opened={!!draft} {...guard.modalProps} size={d.kind === 'locations' ? 'xl' : 'lg'} title={<Text fw={650}>{d.original ? t('edit') : t('create')}</Text>}>
      <form onSubmit={submit}>
        <Stack>
          <SimpleGrid cols={{ base: 1, sm: 2 }} spacing="md">
            <TextInput
              label={t('page_title')}
              placeholder={t('page_title_ph')}
              value={d.title}
              onChange={(e) => setD({ ...d, title: e.currentTarget.value })}
              error={titleErr}
              required
              data-autofocus
            />
            <TextInput
              label={t('slug')}
              placeholder="vpn"
              value={d.slug}
              onChange={(e) => setD({ ...d, slug: e.currentTarget.value.toLowerCase().replace(/\s+/g, '-') })}
              error={slugErr}
              required
              leftSection={<Text size="xs" c="dimmed" pl={6}>/status/</Text>}
              leftSectionWidth={64}
              styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)' } }}
            />
          </SimpleGrid>
          <Text size="xs" c="dimmed" mt={-8}>{t('slug_hint')}</Text>
          {slugMoved && !slugErr && (
            <Alert color="yellow" variant="light" icon={<IconAlertTriangle size={16} />}>
              {t('slug_moved', `/status/${d.original}`, `/status/${d.slug}`)}
            </Alert>
          )}
          <Textarea
            label={t('description')}
            placeholder={t('description_ph')}
            value={d.description ?? ''}
            onChange={(e) => setD({ ...d, description: e.currentTarget.value })}
            autosize
            minRows={2}
            maxRows={5}
          />
          <Stack gap={4}>
            <Text size="sm" fw={500}>{t('kind')}</Text>
            <SegmentedControl value={d.kind ?? 'groups'} onChange={(k) => setD({ ...d, kind: k as StatusPage['kind'] })}
              data={[{ value: 'groups', label: t('k_groups') }, { value: 'locations', label: t('k_locations') }]} />
          </Stack>
          {d.kind === 'locations' ? (
            <LocationsEditor value={d.locations ?? {}} onChange={(locations) => setD({ ...d, locations })} />
          ) : (
          <Stack gap="xs">
            <Text size="sm" fw={500}>{t('groups')}</Text>
            {d.groups.map((g, i) => (
              <GroupEditor
                key={i}
                g={g}
                i={i}
                n={d.groups.length}
                checks={checks}
                onChange={(ng) => setGroups(d.groups.map((x, j) => (j === i ? ng : x)))}
                onMove={(dir) => {
                  const gs = [...d.groups];
                  [gs[i], gs[i + dir]] = [gs[i + dir], gs[i]];
                  setGroups(gs);
                }}
                onRemove={() => setGroups(d.groups.filter((_, j) => j !== i))}
              />
            ))}
            <Box>
              <Button variant="light" size="xs" leftSection={<IconPlus size={14} />} onClick={() => setGroups([...d.groups, { name: '', checks: [] }])}>
                {t('add_group')}
              </Button>
            </Box>
          </Stack>
          )}
          <Group justify="flex-end" mt="xs">
            <Button variant="default" onClick={guard.requestClose}>{c('cancel')}</Button>
            <Button type="submit" loading={busy}>{c('save')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

export default function StatusPages() {
  const t = useT(L);
  const c = useCommon();
  const { lang } = useLang();
  const q = useStatusPages();
  const meta = useMeta();
  const [editing, setEditing] = useState<Draft | null>(null);
  const del = useApiMutation<string>('DELETE', (slug) => `/api/v1/status-pages/${enc(slug)}`);
  const origin = (meta.data?.public_url || window.location.origin).replace(/\/+$/, '');
  const pages = q.data ?? [];

  const confirmDelete = (p: StatusPage) =>
    modals.openConfirmModal({
      title: <Text fw={650}>{c('delete')}</Text>,
      children: <Text size="sm">{c('confirm_delete', p.title)}</Text>,
      labels: { confirm: c('delete'), cancel: c('cancel') },
      confirmProps: { color: 'red' },
      onConfirm: () => del.mutate(p.slug, { onSuccess: () => notifyOk(c('deleted')), onError: notifyError }),
    });

  const create = () => setEditing({ slug: '', title: '', description: '', groups: [{ name: '', checks: [] }] });
  const word = (n: number, ru: [string, string, string], en: [string, string]) =>
    lang === 'ru' ? plural(n, ...ru) : n === 1 ? en[0] : en[1];

  return (
    <>
      <PageHeader
        title={t('title')}
        subtitle={t('subtitle')}
        right={pages.length > 0 && <Button leftSection={<IconPlus size={16} />} onClick={create}>{t('create')}</Button>}
      />
      {q.isError && <Alert color="red" mb="md">{q.error.message}</Alert>}
      {q.isLoading ? (
        <SimpleGrid cols={{ base: 1, lg: 2 }} spacing="md">{[0, 1].map((i) => <Skeleton key={i} h={200} radius="lg" />)}</SimpleGrid>
      ) : pages.length === 0 ? (
        <Empty icon={IconWorldWww} title={t('none')}>
          <Text c="dimmed" size="sm" ta="center" maw={480}>{t('none_hint')}</Text>
          <Button leftSection={<IconPlus size={16} />} onClick={create} mt="xs">{t('create')}</Button>
        </Empty>
      ) : (
        <SimpleGrid cols={{ base: 1, lg: 2 }} spacing="md">
          {pages.map((p) => {
            const url = `${origin}/status/${p.slug}`;
            const nChecks = (p.groups ?? []).reduce((s, g) => s + (g.checks?.length ?? 0), 0);
            return (
              <Card key={p.slug} padding="lg" className="uf-card">
                <Stack gap="sm" h="100%">
                  <Group justify="space-between" align="flex-start" wrap="nowrap" gap="sm">
                    <Stack gap={2} style={{ minWidth: 0 }}>
                      <Text fw={650} fz="lg" truncate>{p.title || t('unnamed')}</Text>
                      {p.description && <Text size="sm" c="dimmed" lineClamp={2}>{p.description}</Text>}
                    </Stack>
                    <Group gap={2} wrap="nowrap">
                      <Tooltip label={t('open')}>
                        <ActionIcon component="a" href={`/status/${p.slug}`} target="_blank" rel="noopener" variant="subtle" color="gray" size="lg" aria-label={t('open')}>
                          <IconExternalLink size={17} />
                        </ActionIcon>
                      </Tooltip>
                      <Tooltip label={c('edit')}>
                        <ActionIcon variant="subtle" color="gray" size="lg" onClick={() => setEditing({ ...structuredClone(p), groups: (p.groups ?? []).map((g) => ({ ...g, checks: g.checks ?? [] })), original: p.slug })} aria-label={c('edit')}>
                          <IconPencil size={17} />
                        </ActionIcon>
                      </Tooltip>
                      <Tooltip label={c('delete')}>
                        <ActionIcon variant="subtle" color="red" size="lg" onClick={() => confirmDelete(p)} aria-label={c('delete')}>
                          <IconTrash size={17} />
                        </ActionIcon>
                      </Tooltip>
                    </Group>
                  </Group>
                  {p.kind === 'locations' ? (
                  <Group gap={6}>
                    <Badge variant="light" styles={{ root: { textTransform: 'none', fontWeight: 500 } }}>
                      {t('loc_badge')} · {t(`m_${p.locations?.mode ?? 'country'}`)}
                    </Badge>
                    {!!p.locations?.items?.filter((i) => i.hidden).length && (
                      <Badge variant="default" styles={{ root: { textTransform: 'none', fontWeight: 500 } }}>{t('hidden_n', (p.locations?.items ?? []).filter((i) => i.hidden).length)}</Badge>
                    )}
                  </Group>
                  ) : (
                  <Group gap={6}>
                    <Badge variant="default" styles={{ root: { textTransform: 'none', fontWeight: 500 } }}>
                      {t('summary_groups', (p.groups ?? []).length, word((p.groups ?? []).length, ['группа', 'группы', 'групп'], ['group', 'groups']))}
                    </Badge>
                    <Badge variant="default" styles={{ root: { textTransform: 'none', fontWeight: 500 } }}>
                      {t('summary_checks', nChecks, word(nChecks, ['проверка', 'проверки', 'проверок'], ['check', 'checks']))}
                    </Badge>
                    {(p.groups ?? []).slice(0, 4).map((g, i) => g.name && (
                      <Badge key={i} variant="light" color="gray" styles={{ root: { textTransform: 'none', fontWeight: 500 } }}>{g.name}</Badge>
                    ))}
                  </Group>
                  )}
                  <Box mt="auto">
                    <CopyBlock value={url} label={t('public_link')} />
                  </Box>
                </Stack>
              </Card>
            );
          })}
        </SimpleGrid>
      )}
      <Editor draft={editing} onClose={() => setEditing(null)} existing={pages.map((p) => p.slug)} />
    </>
  );
}
