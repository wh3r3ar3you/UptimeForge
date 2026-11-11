import { Button, Card, Chip, Group, SegmentedControl, Skeleton, Stack, Text, TextInput } from '@mantine/core';
import { modals } from '@mantine/modals';
import { IconLayoutGrid, IconList, IconPlus, IconSearch, IconShieldCheck } from '@tabler/icons-react';
import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router';
import { api, enc, queryClient, useCheckBars, useChecks, useIsAdmin, type Bar, type CheckConfig, type CheckRow, type ProbeStatus } from '../api';
import { Empty } from '../components/Cards';
import { CheckForm, newToken } from '../components/CheckForm';
import { checkText } from '../components/checkText';
import { BulkBar } from '../components/checks/BulkBar';
import { CheckGroupCard } from '../components/checks/CheckGroupCard';
import { CheckRowView, type RowActions } from '../components/checks/CheckRowView';
import { CheckTiles } from '../components/checks/CheckTiles';
import { groupChecks, isOK, isProblem, matches, type GroupBy, type StatusFilter } from '../components/checks/grouping';
import { useChecksT, useCount } from '../components/checks/text';
import { notifyError, notifyOk, PageHeader, QueryError } from '../components/Misc';
import { Dot } from '../components/Status';
import { ms } from '../format';
import { useCommon, useLang, useT } from '../i18n';
import { usePrefs } from '../prefs';
import { reasonText } from '../reason';
import { status } from '../theme';

const L = {
  ru: {
    title: 'Проверки', subtitle: '{0} проверок · {1} проб', add: 'Добавить проверку', all: 'Все', problems: 'Проблемы', paused: 'На паузе',
    group: 'Группа', all_groups: 'Все группы', status: 'Статус', check: 'Проверка', type: 'Тип', probes: 'Пробы', uptime: 'Аптайм 24ч',
    latency: 'Задержка 24ч', empty: 'Проверок пока нет', empty_hint: 'Добавьте VPN-конфиг, подписку, Telegram-прокси или любой сервис.',
    no_group: 'Без группы', configs: '{0} конфигов', nothing: 'Ничего не найдено',
    up: 'работает', down: 'не работает', failing: 'не работает (подтверждено)', nodata: 'нет данных',
  },
  en: {
    title: 'Checks', subtitle: '{0} checks · {1} probes', add: 'Add check', all: 'All', problems: 'Problems', paused: 'Paused',
    group: 'Group', all_groups: 'All groups', status: 'Status', check: 'Check', type: 'Type', probes: 'Probes', uptime: 'Uptime 24h',
    latency: 'Latency 24h', empty: 'No checks yet', empty_hint: 'Add a VPN config, a subscription, a Telegram proxy or any service.',
    no_group: 'No group', configs: '{0} configs', nothing: 'Nothing found',
    up: 'up', down: 'down', failing: 'failing (confirmed)', nodata: 'no data',
  },
};

const NO_BARS: Record<string, Bar[]> = {};

const shortLabel: Record<string, string> = { xray: 'Xray', mtproto: 'MTProxy', http: 'HTTP', tcp: 'TCP', tls: 'TLS', dns: 'DNS', subscription: 'Sub' };

/** useTypeLabel names a check type: short for the common ones, the catalog title for the rest. */
export function useTypeLabel() {
  const { lang } = useLang();
  const words = checkText[lang] as Record<string, string>;
  return useCallback((type: string) => shortLabel[type] ?? words[`t_${type}`] ?? type, [words]);
}

export function uptimeColor(u?: number) {
  if (u == null) return 'dimmed';
  if (u >= 0.999) return 'green.5';
  if (u >= 0.99) return 'yellow.5';
  return 'red.5';
}

export function ProbeDots({ row, probes }: { row: CheckRow; probes: ProbeStatus[] }) {
  const t = useT(L);
  const { lang } = useLang();
  const byId = new Map(probes.map((p) => [p.id, p]));
  return (
    <Group gap={5} wrap="nowrap">
      {row.probes.map((c) => {
        const p = byId.get(c.probe);
        const color = !c.status ? 'var(--uf-track)' : c.failing ? status.down : c.status === 'down' ? status.partial : status.ok;
        const state = !c.status ? t('nodata') : c.failing ? t('failing') : c.status === 'down' ? t('down') : t('up');
        const extra = c.status === 'up' && c.latency != null ? ` · ${ms(c.latency)}` : c.reason ? ` · ${reasonText(c.reason, c.error, lang)}` : '';
        return <Dot key={c.probe} color={color} size={10} label={`${p?.name ?? c.probe}: ${state}${extra}`} />;
      })}
    </Group>
  );
}

export default function Checks() {
  const t = useChecksT();
  const count = useCount();
  const c = useCommon();
  const { lang } = useLang();
  const nav = useNavigate();
  const typeLabel = useTypeLabel();
  const { data, isLoading, isError, error, refetch } = useChecks();
  const barsQ = useCheckBars();
  const isAdmin = useIsAdmin();
  const { prefs, setPrefs } = usePrefs();
  const [q, setQ] = useState('');
  const [st, setSt] = useState<StatusFilter>('all');
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [openSubs, setOpenSubs] = useState<Set<string>>(new Set());
  const [form, setForm] = useState<{ initial?: CheckConfig } | null>(null);
  const by: GroupBy = prefs.checks?.groupBy ?? 'group';
  const view = prefs.checks?.view ?? 'list';
  const collapsed = new Set(prefs.checks?.collapsed ?? []);
  const setChecksPrefs = (patch: Partial<NonNullable<typeof prefs.checks>>) => setPrefs({ checks: { ...prefs.checks, ...patch } });

  const rows = useMemo(() => data?.checks ?? [], [data?.checks]);
  const probes = useMemo(() => new Map((data?.probes ?? []).map((p) => [p.id, p])), [data?.probes]);
  const regions = useMemo(() => new Intl.DisplayNames([lang], { type: 'region' }), [lang]);
  const needle = q.trim().toLowerCase();
  const visible = useMemo(
    () => rows.filter((r) => matches(r, needle, st) || (!!r.parent && rows.some((p) => p.id === r.parent && matches(p, needle, st)))),
    [rows, needle, st],
  );
  const groups = useMemo(() => groupChecks(visible, by, data?.probes ?? [], {
    noGroup: t('no_group'), noTags: t('no_tags'), noCountry: t('no_country'), multiCountry: t('multi_country'),
    status: { problems: t('problems'), ok: t('ok'), paused: t('paused'), nodata: t('nodata') },
    type: typeLabel,
    country: (cc) => { try { return regions.of(cc.toUpperCase()) ?? cc; } catch { return cc; } },
  }), [visible, by, data?.probes, t, typeLabel, regions]);
  const kids = useMemo(() => {
    const m = new Map<string, CheckRow[]>();
    for (const r of visible) if (r.parent) m.set(r.parent, [...(m.get(r.parent) ?? []), r]);
    return m;
  }, [visible]);
  const top = rows.filter((r) => !r.parent);
  const counts = { all: top.length, problems: top.filter(isProblem).length, ok: top.filter(isOK).length, paused: top.filter((r) => r.paused).length };
  const leaf = rows.filter((r) => !r.subscription);
  const allGroups = [...new Set(rows.map((r) => r.group ?? '').filter(Boolean))].sort();
  const allTags = [...new Set(rows.flatMap((r) => r.tags ?? []))].sort();
  const bars = barsQ.data?.bars ?? NO_BARS;
  const from = barsQ.data?.from ?? 0, step = barsQ.data?.step_s ?? 1800;

  // A selection is about what is on screen: a new filter or grouping starts afresh.
  useEffect(() => setSelected(new Set()), [needle, st, by]);
  // Checks that are gone (deleted here or elsewhere) leave the selection.
  useEffect(() => {
    setSelected((s) => {
      if (!s.size) return s;
      const ids = new Set(rows.map((r) => r.id));
      const next = new Set([...s].filter((id) => ids.has(id)));
      return next.size === s.size ? s : next;
    });
  }, [rows]);

  // Stable callbacks: the memoized rows re-render only when their own data changes.
  const rowsRef = useRef(rows);
  rowsRef.current = rows;
  const actions = useMemo<RowActions>(() => ({
    open: (r, at) => nav(`/checks/${enc(r.id)}${at ? `?at=${at}` : ''}`),
    edit: async (r) => {
      try {
        const res = await api<{ config: CheckConfig }>('GET', `/api/v1/checks/${enc(r.id)}`);
        setForm({ initial: res.config });
      } catch (e) {
        notifyError(e);
      }
    },
    duplicate: async (r) => {
      try {
        const res = await api<{ config: CheckConfig }>('GET', `/api/v1/checks/${enc(r.id)}`);
        const taken = new Set(rowsRef.current.map((x) => x.id));
        let id = `${r.id}-copy`;
        for (let i = 2; taken.has(id); i++) id = `${r.id}-copy${i}`;
        const copy: CheckConfig = { ...res.config, id, name: t('copy', res.config.name || r.name) };
        // A push check is found by its token: a copy with the same one would answer for the original.
        if (copy.type === 'push') copy.params = { ...copy.params, token: newToken() };
        await api('POST', '/api/v1/checks', copy);
        await queryClient.invalidateQueries();
        notifyOk(t('duplicated'));
      } catch (e) {
        notifyError(e);
      }
    },
    pause: async (r, paused) => {
      try {
        await api('POST', `/api/v1/checks/${enc(r.id)}/${paused ? 'pause' : 'resume'}`);
        await queryClient.invalidateQueries();
      } catch (e) {
        notifyError(e);
      }
    },
    remove: (r) => modals.openConfirmModal({
      title: t('delete'), children: <Text size="sm">{t('confirm_delete', 1)}</Text>, labels: { confirm: t('delete'), cancel: c('cancel') },
      confirmProps: { color: 'red' },
      onConfirm: async () => {
        try {
          await api('DELETE', `/api/v1/checks/${enc(r.id)}`);
          setSelected((s) => { if (!s.has(r.id)) return s; const n = new Set(s); n.delete(r.id); return n; });
          await queryClient.invalidateQueries();
        } catch (e) {
          notifyError(e);
        }
      },
    }),
  }), [nav, t, c]);
  const select = useCallback((id: string, on: boolean) => setSelected((s) => { const n = new Set(s); if (on) n.add(id); else n.delete(id); return n; }), []);
  const selectMany = useCallback((ids: string[], on: boolean) => setSelected((s) => {
    const n = new Set(s);
    for (const id of ids) if (on) n.add(id); else n.delete(id);
    return n;
  }), []);
  const toggleSub = useCallback((id: string) => setOpenSubs((s) => { const n = new Set(s); if (n.has(id)) n.delete(id); else n.add(id); return n; }), []);
  const clearSelection = useCallback(() => setSelected(new Set()), []);
  const toggleGroup = (key: string) => {
    const next = new Set(collapsed);
    if (next.has(key)) next.delete(key); else next.add(key);
    setChecksPrefs({ collapsed: [...next] });
  };

  const add = isAdmin ? <Button leftSection={<IconPlus size={16} />} onClick={() => setForm({})}>{t('add')}</Button> : undefined;
  if (isLoading) return <Stack><PageHeader title={t('title')} /><Skeleton h={400} radius="lg" /></Stack>;
  // Without data an error is not "no checks yet".
  if (isError && !data) return <Stack><PageHeader title={t('title')} /><QueryError error={error} onRetry={() => refetch()} /></Stack>;

  const row = (r: CheckRow, indent = false) => (
    <CheckRowView key={r.id} row={r} bars={bars[r.id]} from={from} step={step} probes={probes} t={t} count={count} typeLabel={typeLabel}
      selected={selected.has(r.id)} onSelect={select} actions={actions} isAdmin={isAdmin} indent={indent}
      expanded={r.subscription ? openSubs.has(r.id) : undefined} onToggle={r.subscription ? toggleSub : undefined} />
  );

  return (
    <>
      <PageHeader title={t('title')} subtitle={t('subtitle', count(leaf.length, 'w_check'), count(data?.probes.length ?? 0, 'w_probe'))} right={add} />
      {isError && <QueryError error={error} onRetry={() => refetch()} />}
      {rows.length === 0 ? (
        <Empty icon={IconShieldCheck} title={t('empty')}>
          <Text c="dimmed" ta="center">{t('empty_hint')}</Text>
          {add}
        </Empty>
      ) : (
        <Stack gap="md">
          <Card padding="md">
            <Stack gap="sm">
              <Group gap="sm" wrap="wrap">
                <TextInput placeholder={t('search')} aria-label={t('search')} leftSection={<IconSearch size={16} />} value={q} onChange={(e) => setQ(e.currentTarget.value)} style={{ flex: '1 1 240px' }} />
                <Group gap={6} wrap="nowrap">
                  <Text size="sm" c="dimmed">{t('group_by')}</Text>
                  <SegmentedControl size="xs" value={by} onChange={(v) => setChecksPrefs({ groupBy: v as GroupBy })}
                    data={(['group', 'type', 'status', 'country', 'tag'] as const).map((k) => ({ value: k, label: t(`by_${k}`) }))} />
                </Group>
                <SegmentedControl size="xs" value={view} onChange={(v) => setChecksPrefs({ view: v as 'list' | 'tiles' })}
                  data={[
                    { value: 'list', label: <Group gap={4} wrap="nowrap"><IconList size={14} />{t('list')}</Group> },
                    { value: 'tiles', label: <Group gap={4} wrap="nowrap"><IconLayoutGrid size={14} />{t('tiles')}</Group> },
                  ]} />
              </Group>
              <Chip.Group value={st} onChange={(v) => setSt(v as StatusFilter)}>
                <Group gap={6}>
                  <Chip size="xs" value="all">{t('all')} {counts.all}</Chip>
                  <Chip size="xs" value="problems" color="red">{t('problems')} {counts.problems}</Chip>
                  <Chip size="xs" value="ok" color="green">{t('ok')} {counts.ok}</Chip>
                  <Chip size="xs" value="paused" color="gray">{t('paused')} {counts.paused}</Chip>
                </Group>
              </Chip.Group>
            </Stack>
          </Card>
          {groups.length === 0 && <Text c="dimmed" ta="center" py="lg">{t('nothing')}</Text>}
          {groups.map((g) => {
            const open = g.problems > 0 || !collapsed.has(g.key);
            const selectable = g.rows.filter((r) => !r.managed).map((r) => r.id);
            const picked = selectable.filter((id) => selected.has(id)).length;
            return (
              <CheckGroupCard key={g.key} group={g} open={open} onToggle={() => toggleGroup(g.key)} t={t} count={count}
                select={isAdmin && view === 'list' ? { selected: picked, total: selectable.length, set: (on) => selectMany(selectable, on) } : undefined}>
                {view === 'tiles' ? (
                  <CheckTiles rows={g.rows.flatMap((r) => (r.subscription ? kids.get(r.id) ?? [] : [r]))} bars={bars} from={from} step={step} t={t} onOpen={(r) => actions.open(r)} />
                ) : (
                  g.rows.map((r) => (
                    <Fragment key={r.id}>
                      {row(r)}
                      {r.subscription && openSubs.has(r.id) && (kids.get(r.id) ?? []).map((k) => row(k, true))}
                    </Fragment>
                  ))
                )}
              </CheckGroupCard>
            );
          })}
        </Stack>
      )}
      {selected.size > 0 && <BulkBar ids={[...selected]} groups={allGroups} tags={allTags} t={t} onDone={clearSelection} />}
      {form && <CheckForm key={form.initial?.id ?? 'new'} opened onClose={() => setForm(null)} initial={form.initial} />}
    </>
  );
}

