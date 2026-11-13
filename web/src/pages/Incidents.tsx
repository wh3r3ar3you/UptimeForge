import {
  Anchor, Badge, Box, Button, Card, Code, Group, Menu, SegmentedControl, SimpleGrid, Skeleton, Stack, Table, Text, TextInput, Tooltip,
} from '@mantine/core';
import {
  IconAlertOctagon, IconAlertTriangle, IconBellOff, IconCheck, IconChevronDown, IconCircleCheck, IconClockHour4, IconHistory,
  IconSearch, IconUrgent, IconBulb,
} from '@tabler/icons-react';
import dayjs from 'dayjs';
import { useMemo, useState, type ReactNode } from 'react';
import { Link } from 'react-router';
import { modals } from '@mantine/modals';
import { api, enc, INCIDENT_PAGE, useApiMutation, useIncidents, useIsAdmin, type Incident, type IncidentList } from '../api';
import { Empty, Section, StatCard } from '../components/Cards';
import { useParentLabels } from '../components/checks/parents';
import { notifyError, notifyOk, PageHeader, QueryError } from '../components/Misc';
import { ProbeLines, useWhy } from '../components/ProbeLines';
import { downText, dropLow, dropText, hostPeak, hostText } from '../reason';
import { kindColor, VerdictBadge } from '../components/Status';
import { clock, duration, since } from '../format';
import { countWord, useCommon, useLang, useT } from '../i18n';

/**
 * StatGrid keeps one tile per row on phones: values here are long ("312.81 MiB",
 * "2 ч 25 мин") and would be cut off in a two-column phone grid.
 */
function StatGrid({ children, cols = 4 }: { children: ReactNode; cols?: number }) {
  return <SimpleGrid cols={{ base: 1, xs: 2, md: Math.min(cols, 3), lg: cols }} spacing="md">{children}</SimpleGrid>;
}

const L = {
  ru: {
    caused_by: 'Из-за: {0}. Отдельно не сообщалось', affected: 'Затронуто проверок: {0} — {1}',
    title: 'Инциденты', subtitle: 'Что сломано прямо сейчас и что ломалось раньше',
    open: 'Открытые', history: 'История', open_now: 'Открыто сейчас', critical_now: 'Критичных', week: 'За 7 дней',
    mttr: 'Восстановление за 7 дней', mttr_sub: 'в среднем по {0} закрытым', none_open: 'Открытых инцидентов нет',
    mttr_sub_partial: 'по {0} закрытым из загруженных', at_least: 'не меньше {0}', partial_hint: 'загружена не вся неделя: «Загрузить ещё» внизу',
    load_more: 'Загрузить ещё (раньше {0})', resolve_title: 'Закрыть инцидент вручную?', resolve_confirm: 'Закрыть',
    resolve_body: 'Инцидент закроется сразу. Если проблема на самом деле осталась, тревог и напоминаний по ней не будет, пока проверка не восстановится и не упадёт снова.',
    none_open_hint: 'Все проверки работают так, как ожидается. Как только что-то сломается, оно появится здесь и в Telegram.',
    started: 'Начало', for: 'идёт {0}', since_at: 'с {0}', duration: 'Длительность', peak: 'Пик сбоя', ack_by: 'Принял',
    name: 'Название', group: 'Группа', state: 'Худшее состояние', ongoing: 'идёт', no_results: 'Ничего не найдено',
    sev_all: 'Все', sev_critical: 'Критичные', sev_warning: 'Предупреждения', show_all: 'Показать все ({0})',
    acked: 'Принято', muted: 'Заглушено', mute_1h: 'на 1 час', mute_8h: 'на 8 часов', mute_24h: 'на 24 часа',
    resolve: 'Решено', resolved_ok: 'Закрыто. Пока не восстановится, тревог не будет', resolve_hint: 'Закрыть инцидент. Если проблема на самом деле осталась, тревог по ней не будет, пока она не восстановится',
    mute_for: 'Заглушить', ack_hint: 'Напоминания прекратятся, в Telegram появится отметка', by: '{0} · {1}',
    probes_of: '{0} из {1}', probes_word: 'проба|пробы|проб',
  },
  en: {
    caused_by: 'Caused by: {0}. Not announced on its own', affected: 'Checks affected: {0} — {1}',
    title: 'Incidents', subtitle: 'What is broken right now and what broke before',
    open: 'Open', history: 'History', open_now: 'Open now', critical_now: 'Critical', week: 'Last 7 days',
    mttr: 'Time to recover, 7 days', mttr_sub: 'mean over {0} resolved', none_open: 'No open incidents',
    mttr_sub_partial: 'over {0} resolved among those loaded', at_least: 'at least {0}', partial_hint: 'not the whole week is loaded: Load more is below',
    load_more: 'Load more (before {0})', resolve_title: 'Close the incident by hand?', resolve_confirm: 'Close',
    resolve_body: 'The incident closes now. If the problem is in fact still there, it raises no alerts or reminders until the check recovers and fails again.',
    none_open_hint: 'Every check works as expected. When something breaks it shows up here and in Telegram.',
    started: 'Started', for: 'for {0}', since_at: 'since {0}', duration: 'Duration', peak: 'Peak', ack_by: 'Acknowledged',
    name: 'Name', group: 'Group', state: 'Worst state', ongoing: 'ongoing', no_results: 'Nothing found',
    sev_all: 'All', sev_critical: 'Critical', sev_warning: 'Warnings', show_all: 'Show all ({0})',
    acked: 'Acknowledged', muted: 'Muted', mute_1h: 'for 1 hour', mute_8h: 'for 8 hours', mute_24h: 'for 24 hours',
    resolve: 'Resolved', resolved_ok: 'Closed. No alerts for it until it recovers', resolve_hint: 'Close the incident. If the problem is in fact still there, there are no alerts for it until it recovers',
    mute_for: 'Mute', ack_hint: 'Stops reminders and marks the Telegram alert', by: '{0} · {1}',
    probes_of: '{0} of {1}', probes_word: 'probe|probes',
  },
};

/** incidentLink: probe incidents open the probe, everything else the check. */
function incidentLink(key: string) {
  if (key.startsWith('probe:') || key.startsWith('probe-net:')) return `/probes/${enc(key.slice(key.indexOf(':') + 1))}`;
  if (key.startsWith('host:')) return `/probes/${enc(key.split(':')[1])}`;
  if (key.startsWith('rw:')) return '/remnawave';
  if (key.startsWith('update:')) return '/incidents';
  if (key.startsWith('sub:')) return `/checks/${enc(key.slice(4))}`;
  return `/checks/${enc(key)}`;
}

function SeverityBadge({ s }: { s: Incident['severity'] }) {
  const c = useCommon();
  const crit = s === 'critical';
  return (
    <Badge
      variant="default"
      size="sm"
      leftSection={crit ? <IconAlertOctagon size={12} color={kindColor('down')} /> : <IconAlertTriangle size={12} color={kindColor('partial')} />}
      styles={{ root: { textTransform: 'none', fontWeight: 600 } }}
    >
      {crit ? c('critical') : c('warning')}
    </Badge>
  );
}

function OpenCard({ inc, names }: { inc: Incident; names: Map<string, string> }) {
  const t = useT(L);
  const c = useCommon();
  const { lang } = useLang();
  const why = useWhy();
  const ack = useApiMutation<string>('POST', (id) => `/api/v1/incidents/${enc(id)}/ack`);
  const resolve = useApiMutation<string>('POST', (id) => `/api/v1/incidents/${enc(id)}/resolve`);
  // Ack, mute and resolve change alerting for everyone: admins only, as on the hub.
  const isAdmin = useIsAdmin();
  const label = useParentLabels();
  // The cause is an incident key (a check id, probe:<id>, rw:<uuid>…): name it
  // as its own incident does, else as the dependency labels do.
  const causeName = (key: string) =>
    names.get(key) ?? label(key.startsWith('probe-net:') ? `probe:${key.slice(10)}` : key.startsWith('sub:') ? key.slice(4) : key);
  const mute = useApiMutation<{ id: string; minutes: number }>('POST', (b) => `/api/v1/incidents/${enc(b.id)}/mute`, (b) => ({ minutes: b.minutes }));
  const muted = inc.muted_until && dayjs(inc.muted_until).isAfter(dayjs());
  const isProbe = inc.type === 'probe' || inc.type === 'subscription';
  const explain = why(inc.kind, inc.lines ?? [], { region: inc.region, type: inc.type, note: inc.note, connected: inc.drop?.connected });

  const doMute = (minutes: number) =>
    mute.mutate({ id: inc.id, minutes }, { onSuccess: () => notifyOk(t('muted')), onError: notifyError });
  // Closing by hand silences the problem until it recovers: say so before doing it.
  const confirmResolve = () => modals.openConfirmModal({
    title: <Text fw={650}>{t('resolve_title')}</Text>,
    children: <Text size="sm">{t('resolve_body')}</Text>,
    labels: { confirm: t('resolve_confirm'), cancel: c('cancel') },
    confirmProps: { color: 'green' },
    onConfirm: () => resolve.mutate(inc.id, { onSuccess: () => notifyOk(t('resolved_ok')), onError: notifyError }),
  });

  return (
    <Card padding="lg" className="uf-card" style={{ borderLeft: `3px solid ${kindColor(inc.kind)}` }}>
      <Stack gap="sm">
        <Group justify="space-between" align="flex-start" gap="sm">
          <Stack gap={6} style={{ minWidth: 0, flex: '1 1 260px' }}>
            <Group gap={8}>
              <VerdictBadge kind={inc.kind} region={inc.region} />
              <SeverityBadge s={inc.severity} />
            </Group>
            <Anchor component={Link} to={incidentLink(inc.key)} fw={650} fz="lg" c="var(--mantine-color-text)" style={{ wordBreak: 'break-word' }}>
              {inc.name}
            </Anchor>
            {inc.cause && <Text size="sm" c="dimmed">{t('caused_by', causeName(inc.cause))}</Text>}
            {!!inc.affected?.length && <Text size="sm" c="dimmed">{t('affected', inc.affected.length, inc.affected.slice(0, 10).join(', '))}</Text>}
            {(inc.group || inc.target) && (
              <Group gap={8} wrap="wrap">
                {inc.group && <Text size="sm" c="dimmed">{inc.group}</Text>}
                {inc.group && inc.target && <Text size="sm" c="dimmed">·</Text>}
                {inc.target && <Code fz="xs" style={{ wordBreak: 'break-all' }}>{inc.target}</Code>}
              </Group>
            )}
          </Stack>
          <Group gap="xs" wrap="nowrap">
            {inc.ack_by ? (
              <Badge size="lg" variant="light" color="forge" leftSection={<IconCheck size={14} />} styles={{ root: { textTransform: 'none' } }}>
                {c('acked_by', inc.ack_by)}
              </Badge>
            ) : isAdmin && (
              <Tooltip label={t('ack_hint')}>
                <Button
                  size="xs"
                  variant="light"
                  leftSection={<IconCheck size={15} />}
                  loading={ack.isPending}
                  onClick={() => ack.mutate(inc.id, { onSuccess: () => notifyOk(t('acked')), onError: notifyError })}
                >
                  {c('ack')}
                </Button>
              </Tooltip>
            )}
            {isAdmin && (
              <Tooltip label={t('resolve_hint')} multiline w={260}>
                <Button size="xs" variant="light" color="green" leftSection={<IconCircleCheck size={15} />} loading={resolve.isPending} onClick={confirmResolve}>
                  {t('resolve')}
                </Button>
              </Tooltip>
            )}
            {isAdmin && <Menu position="bottom-end" withinPortal>
              <Menu.Target>
                <Button size="xs" variant="default" leftSection={<IconBellOff size={15} />} rightSection={<IconChevronDown size={14} />} loading={mute.isPending}>
                  {c('mute')}
                </Button>
              </Menu.Target>
              <Menu.Dropdown>
                <Menu.Label>{t('mute_for')}</Menu.Label>
                <Menu.Item onClick={() => doMute(60)}>{t('mute_1h')}</Menu.Item>
                <Menu.Item onClick={() => doMute(480)}>{t('mute_8h')}</Menu.Item>
                <Menu.Item onClick={() => doMute(1440)}>{t('mute_24h')}</Menu.Item>
              </Menu.Dropdown>
            </Menu>}
          </Group>
        </Group>

        {inc.drop ? (
          <Group gap="xs">
            <Badge variant="light" color="red" radius="sm" size="lg" styles={{ root: { textTransform: 'none' } }}>
              <Text span size="sm" ff="monospace">{inc.kind === 'node_down' ? downText(inc, lang) || '—' : dropText(inc.drop, lang)}</Text>
            </Badge>
            {inc.kind !== 'node_down' && inc.drop.low < inc.drop.now && <Text size="sm" c="dimmed">{dropLow(inc.drop, lang)}</Text>}
          </Group>
        ) : inc.host ? (
          <Group gap="xs">
            <Badge variant="light" color="orange" radius="sm" size="lg" styles={{ root: { textTransform: 'none' } }}>
              <Text span size="sm" ff="monospace">{hostText(inc.host, lang)}</Text>
            </Badge>
            {inc.host.peak > inc.host.value && <Text size="sm" c="dimmed">{hostPeak(inc.host, lang)}</Text>}
          </Group>
        ) : isProbe && inc.lines?.[0]?.error ? (
          <Code block fz="xs" style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>{inc.lines[0].error}</Code>
        ) : (
          <ProbeLines inc={inc} />
        )}

        {explain && (
          <Group gap={8} wrap="nowrap" align="flex-start">
            <IconBulb size={16} style={{ flex: 'none', marginTop: 2, opacity: 0.7 }} />
            <Text size="sm" c="dimmed">{explain}</Text>
          </Group>
        )}

        <Group gap="lg" wrap="wrap">
          <Group gap={6} wrap="nowrap">
            <IconClockHour4 size={15} style={{ opacity: 0.6 }} />
            <Text size="sm">
              {t('since_at', clock(inc.started))} · <Text span fw={600} className="uf-num">{t('for', since(inc.started, lang))}</Text>
            </Text>
          </Group>
          {!isProbe && !inc.host && !inc.drop && inc.peak_total ? (
            <Text size="sm" c="dimmed">
              {t('peak')}: <Text span className="uf-num" c="var(--mantine-color-text)">{inc.peak_failing}/{inc.peak_total}</Text> {countWord(lang, inc.peak_total, t('probes_word'))}
            </Text>
          ) : null}
          {muted && (
            <Group gap={6} wrap="nowrap">
              <IconBellOff size={15} style={{ opacity: 0.6 }} />
              <Text size="sm" c="dimmed">
                {c('muted_until', clock(inc.muted_until!))}{inc.muted_by ? ` · ${inc.muted_by}` : ''}
              </Text>
            </Group>
          )}
        </Group>
      </Stack>
    </Card>
  );
}

function incDuration(i: Incident) {
  const end = i.resolved ? dayjs(i.resolved) : dayjs();
  return Math.max(0, end.diff(dayjs(i.started), 'second'));
}

export default function Incidents() {
  const t = useT(L);
  const c = useCommon();
  const { lang } = useLang();
  const q = useIncidents();
  const [sev, setSev] = useState('all');
  const [search, setSearch] = useState('');
  const [all, setAll] = useState(false);
  // Pages older than the first, fetched with ?before= on demand; the first page keeps polling.
  const [older, setOlder] = useState<Incident[]>([]);
  const [exhausted, setExhausted] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);

  const open = useMemo(
    () => [...(q.data?.open ?? [])].sort((a, b) =>
      (a.severity === 'critical' ? 0 : 1) - (b.severity === 'critical' ? 0 : 1) || dayjs(b.started).valueOf() - dayjs(a.started).valueOf()),
    [q.data],
  );
  const openIds = useMemo(() => new Set(open.map((i) => i.id)), [open]);
  const openNames = useMemo(() => new Map(open.map((i) => [i.key, i.name])), [open]);
  const firstPage = q.data?.history ?? [];
  const history = useMemo(() => {
    const seen = new Set<string>();
    return [...(q.data?.history ?? []), ...older].filter((i) => !openIds.has(i.id) && !seen.has(i.id) && !!seen.add(i.id));
  }, [q.data, older, openIds]);
  const filtered = useMemo(() => {
    const s = search.trim().toLowerCase();
    return history.filter((i) =>
      (sev === 'all' || i.severity === sev) &&
      (!s || i.name.toLowerCase().includes(s) || (i.group ?? '').toLowerCase().includes(s) || i.key.toLowerCase().includes(s)));
  }, [history, sev, search]);

  // Everything the hub has may already be here: a first page shorter than a full one is all of it.
  const complete = exhausted || (!!q.data && firstPage.length < INCIDENT_PAGE);
  const oldest = history.length ? history.reduce((m, i) => (i.started < m ? i.started : m), history[0].started) : undefined;
  const loadMore = async () => {
    if (!oldest) return;
    setLoadingMore(true);
    try {
      const page = await api<IncidentList>('GET', `/api/v1/incidents?before=${enc(oldest)}`);
      const got = page.history ?? [];
      setOlder((o) => [...o, ...got]);
      if (got.length < INCIDENT_PAGE) setExhausted(true);
      setAll(true);
    } catch (e) {
      notifyError(e);
    } finally {
      setLoadingMore(false);
    }
  };

  // The week's numbers are honest only once the loaded history reaches back a
  // week (or holds everything); until then they say they are a lower bound.
  const weekAgo = dayjs().subtract(7, 'day');
  const weekCovered = complete || (!!oldest && dayjs(oldest).isBefore(weekAgo));
  const lastWeek = [...open, ...history].filter((i) => dayjs(i.started).isAfter(weekAgo)).length;
  const resolved = history.filter((i) => i.resolved && dayjs(i.started).isAfter(weekAgo));
  const mttr = resolved.length ? resolved.reduce((s, i) => s + incDuration(i), 0) / resolved.length : null;
  const critical = open.filter((i) => i.severity === 'critical').length;
  const shown = all ? filtered : filtered.slice(0, 30);

  // No data is not "no incidents": zeros here would read as all clear.
  if (!q.data && q.isError) {
    return <><PageHeader title={t('title')} subtitle={t('subtitle')} /><QueryError error={q.error} onRetry={() => q.refetch()} /></>;
  }

  return (
    <>
      <PageHeader title={t('title')} subtitle={t('subtitle')} />
      <Stack gap="xl">
        <StatGrid>
          <StatCard icon={IconUrgent} color={open.length ? 'orange' : 'forge'} label={t('open_now')} value={q.isLoading ? '…' : open.length} />
          <StatCard icon={IconAlertOctagon} color={critical ? 'red' : 'gray'} label={t('critical_now')} value={q.isLoading ? '…' : critical} />
          <StatCard icon={IconHistory} color="blue" label={t('week')} value={q.isLoading ? '…' : weekCovered ? lastWeek : t('at_least', lastWeek)}
            sub={!q.isLoading && !weekCovered ? t('partial_hint') : undefined} />
          <StatCard
            icon={IconClockHour4}
            color="violet"
            label={t('mttr')}
            value={mttr == null ? '—' : duration(mttr, lang)}
            sub={resolved.length ? t(weekCovered ? 'mttr_sub' : 'mttr_sub_partial', resolved.length) : undefined}
          />
        </StatGrid>

        <Section title={<Group gap={8}>{t('open')}{open.length > 0 && <Badge variant="light" color="gray" size="lg" circle>{open.length}</Badge>}</Group>}>
          {q.isLoading ? (
            <Stack gap="md">{[0, 1].map((i) => <Skeleton key={i} h={180} radius="lg" />)}</Stack>
          ) : open.length === 0 ? (
            <Empty icon={IconCircleCheck} title={t('none_open')}>
              <Text c="dimmed" size="sm" ta="center" maw={460}>{t('none_open_hint')}</Text>
            </Empty>
          ) : (
            <Stack gap="md">{open.map((i) => <OpenCard key={i.id} inc={i} names={openNames} />)}</Stack>
          )}
        </Section>

        <Section
          title={t('history')}
          right={
            <Group gap="sm" wrap="wrap">
              <SegmentedControl
                size="xs"
                value={sev}
                onChange={setSev}
                data={[
                  { value: 'all', label: t('sev_all') },
                  { value: 'critical', label: t('sev_critical') },
                  { value: 'warning', label: t('sev_warning') },
                ]}
              />
              <TextInput
                size="xs"
                w={200}
                placeholder={c('search')}
                leftSection={<IconSearch size={14} />}
                value={search}
                onChange={(e) => setSearch(e.currentTarget.value)}
              />
            </Group>
          }
        >
          <Card padding={0}>
            <Table.ScrollContainer minWidth={860}>
              <Table className="uf-table" verticalSpacing="sm" horizontalSpacing="md" highlightOnHover style={{ whiteSpace: 'nowrap' }}>
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>{t('state')}</Table.Th>
                    <Table.Th>{t('name')}</Table.Th>
                    <Table.Th>{t('group')}</Table.Th>
                    <Table.Th>{t('started')}</Table.Th>
                    <Table.Th>{t('duration')}</Table.Th>
                    <Table.Th>{t('peak')}</Table.Th>
                    <Table.Th>{t('ack_by')}</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {q.isLoading && [0, 1, 2].map((i) => (
                    <Table.Tr key={i}><Table.Td colSpan={7}><Skeleton h={20} /></Table.Td></Table.Tr>
                  ))}
                  {shown.map((i) => {
                    const probeLike = i.type === 'probe' || i.type === 'subscription' || i.type === 'host' || i.type === 'users';
                    return (
                      <Table.Tr key={i.id}>
                        <Table.Td><VerdictBadge kind={i.worst || i.kind} region={i.region} size="sm" /></Table.Td>
                        <Table.Td maw={340}>
                          <Anchor component={Link} to={incidentLink(i.key)} size="sm" fw={600} c="var(--mantine-color-text)" truncate="end" display="block">
                            {i.name}
                          </Anchor>
                        </Table.Td>
                        <Table.Td><Text size="sm" c="dimmed">{i.group || '—'}</Text></Table.Td>
                        <Table.Td className="uf-num"><Text size="sm" className="uf-num">{clock(i.started, true)}</Text></Table.Td>
                        <Table.Td>
                          <Text size="sm" className="uf-num">
                            {duration(incDuration(i), lang)}
                            {!i.resolved && <Text span size="xs" c="dimmed"> · {t('ongoing')}</Text>}
                          </Text>
                        </Table.Td>
                        <Table.Td>
                          {probeLike || !i.peak_total ? <Text size="sm" c="dimmed">—</Text> : (
                            <Text size="sm" className="uf-num">
                              {i.peak_failing}/{i.peak_total}{' '}
                              <Text span size="xs" c="dimmed">{countWord(lang, i.peak_total, t('probes_word'))}</Text>
                            </Text>
                          )}
                        </Table.Td>
                        <Table.Td><Text size="sm" c={i.ack_by ? undefined : 'dimmed'}>{i.ack_by || '—'}</Text></Table.Td>
                      </Table.Tr>
                    );
                  })}
                  {!q.isLoading && shown.length === 0 && (
                    <Table.Tr><Table.Td colSpan={7}><Text c="dimmed" size="sm" ta="center" py="md">{t('no_results')}</Text></Table.Td></Table.Tr>
                  )}
                </Table.Tbody>
              </Table>
            </Table.ScrollContainer>
          </Card>
          {!all && filtered.length > shown.length ? (
            <Box ta="center"><Button variant="subtle" onClick={() => setAll(true)}>{t('show_all', filtered.length)}</Button></Box>
          ) : !complete && !!oldest && (
            <Box ta="center">
              <Button variant="subtle" loading={loadingMore} onClick={loadMore}>{t('load_more', clock(oldest, true))}</Button>
            </Box>
          )}
        </Section>
      </Stack>
    </>
  );
}
