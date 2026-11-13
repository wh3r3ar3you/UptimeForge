import {
  ActionIcon, Badge, Box, Button, Checkbox, Divider, Group, Menu, Popover, ScrollArea, Stack, Text, TextInput, Tooltip,
  UnstyledButton,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import {
  IconActivity, IconAdjustmentsHorizontal, IconArrowDown, IconArrowUp, IconArrowsSort, IconClock, IconCpu, IconDatabase, IconDots,
  IconExternalLink, IconGauge, IconKey, IconNetwork, IconPencil, IconPlugConnected, IconPlus, IconSearch, IconServer2, IconShieldCheck,
  IconSortAscending, IconSortDescending, IconStack2, IconTransfer, IconTrash, IconUsers, IconWifi, type Icon,
} from '@tabler/icons-react';
import { useMemo, useRef, useState, type ReactNode } from 'react';
import { useNavigate } from 'react-router';
import {
  api, enc, isChecker, queryClient, useMeta, useRemnawave, useRemnawaveUsers, type Incident, type Install, type ProbeRow, type RemnawaveNode,
} from '../api';
import { bitrate, bytes, compact, days, duration, flag, num } from '../format';
import { useCommon, useLang, useT } from '../i18n';
import { usePrefs, type BarStyle, type Prefs } from '../prefs';
import { status } from '../theme';
import { clickableRow, notifyError, notifyOk } from './Misc';
import { ProbeForm, TokenModal } from './ProbeForm';
import { Dot } from './Status';

const L = {
  ru: {
    title: 'Все системы', sub: 'Нажмите на систему, чтобы открыть графики. Заголовки перетаскиваются, их края тянутся мышью.',
    filter: 'Фильтр…', view: 'Вид', add: 'Добавить сервер',
    density: 'Плотность', d_compact: 'Компактно', d_normal: 'Обычно', d_comfortable: 'Просторно',
    status: 'Статус', s_all: 'Все системы', s_online: 'В сети ({0})', s_offline: 'Не в сети ({0})', s_problems: 'С проблемами ({0})',
    sort: 'Сортировать по', columns: 'Видимые столбцы', reset: 'Сбросить вид',
    system: 'Система', cpu: 'ЦП', steal: 'Steal', iowait: 'I/O wait', mem: 'Память', disk: 'Диск', load: 'Ср. загрузка', net: 'Сеть',
    rx: 'Вход', tx: 'Выход', traffic: 'Трафик 24ч', users: 'Онлайн', tcp: 'TCP', diskio: 'Диск I/O', uptime: 'Время работы',
    agent: 'Агент', checks: 'Проверки',
    steal_hint: 'Время, отданное гипервизором соседям', iowait_hint: 'Процессор ждёт диск', users_hint: 'Пользователи онлайн в Remnawave и изменение за час',
    load_hint: 'Load average 1, 5, 15 мин · ядер: {0}', agent_old: 'Версия отличается от хаба ({0})', agent_ok: 'Версия как у хаба',
    offline: 'офлайн {0}', never: 'ещё не подключался', no_net: 'нет интернета', via: 'проверки через прокси', hub: 'хаб',
    metrics_only: 'только метрики', failing: 'со сбоем: {0}', empty: 'Ничего не найдено',
    open: 'Открыть', edit: 'Изменить', rotate: 'Перевыпустить токен', delete: 'Удалить',
    confirm: 'Удалить «{0}»? Проверки с него прекратятся, история метрик удалится вместе с ним.', deleted: 'Сервер удалён',
    rotate_confirm: 'Текущий агент отключится, его нужно будет запустить с новым токеном.', new_token: 'Новый токен',
    read: 'чт', write: 'зп',
    bars: 'Полоски', b_pill: 'Капсула', b_line: 'Линия', b_thin: 'Тонкая', b_segments: 'Сегменты', b_gradient: 'Градиент', b_ring: 'Кольцо', b_none: 'Только число',
    move: 'Перетащите заголовок, чтобы поменять колонки местами', menu: 'Действия: {0}', vcpu: '{0} vCPU',
  },
  en: {
    title: 'All systems', sub: 'Click a system for its charts. Drag a header to move it, its edge to resize.',
    filter: 'Filter…', view: 'View', add: 'Add server',
    density: 'Density', d_compact: 'Compact', d_normal: 'Normal', d_comfortable: 'Comfortable',
    status: 'Status', s_all: 'All systems', s_online: 'Online ({0})', s_offline: 'Offline ({0})', s_problems: 'With problems ({0})',
    sort: 'Sort by', columns: 'Visible columns', reset: 'Reset view',
    system: 'System', cpu: 'CPU', steal: 'Steal', iowait: 'I/O wait', mem: 'Memory', disk: 'Disk', load: 'Load avg', net: 'Network',
    rx: 'In', tx: 'Out', traffic: 'Traffic 24h', users: 'Online', tcp: 'TCP', diskio: 'Disk I/O', uptime: 'Uptime',
    agent: 'Agent', checks: 'Checks',
    steal_hint: 'Time the hypervisor gave to neighbours', iowait_hint: 'The CPU waits on the disk', users_hint: 'Users online in Remnawave and the change over an hour',
    load_hint: 'Load average 1, 5, 15 min · {0} cores', agent_old: 'Differs from the hub ({0})', agent_ok: 'Same as the hub',
    offline: 'offline {0}', never: 'never connected', no_net: 'no internet', via: 'checks through a proxy', hub: 'hub',
    metrics_only: 'metrics only', failing: '{0} failing', empty: 'Nothing found',
    open: 'Open', edit: 'Edit', rotate: 'Rotate token', delete: 'Delete',
    confirm: 'Delete “{0}”? Checks from it stop and its metrics history goes with it.', deleted: 'Server deleted',
    rotate_confirm: 'The running agent will disconnect and must restart with the new token.', new_token: 'New token',
    read: 'r', write: 'w',
    bars: 'Bars', b_pill: 'Pill', b_line: 'Line', b_thin: 'Thin', b_segments: 'Segments', b_gradient: 'Gradient', b_ring: 'Ring', b_none: 'Number only',
    move: 'Drag a header to move the column', menu: 'Actions: {0}', vcpu: '{0} vCPU',
  },
};

type T = ReturnType<typeof useT<typeof L.ru>>;

interface Ctx {
  t: T;
  lang: 'ru' | 'en';
  hubVersion?: string;
  rw: (p: ProbeRow) => RemnawaveNode | undefined;
  usersHourAgo: Map<string, number>;
  alerts: Map<string, Incident[]>;
  bar: BarStyle;
}

interface Col {
  id: string;
  icon: Icon;
  width: number;
  min: number;
  hint?: string; // dictionary key
  value?: (p: ProbeRow, c: Ctx) => number | string; // for sorting
  cell: (p: ProbeRow, c: Ctx, m: Metrics | undefined) => ReactNode;
}

type Metrics = NonNullable<ProbeRow['metrics']>;

const fresh = (p: ProbeRow) => (p.metrics && p.metrics_at && Date.now() - Date.parse(p.metrics_at) < 120_000 ? p.metrics : undefined);

export const BAR_STYLES: BarStyle[] = ['pill', 'line', 'thin', 'segments', 'gradient', 'ring', 'none'];

/** Bar draws a usage share in one of the styles a user can pick. */
export function Bar({ v, kind }: { v: number; kind: BarStyle }) {
  const x = Math.max(0, Math.min(100, v));
  const color = x >= 90 ? status.down : x >= 75 ? status.partial : status.ok;
  const style = { ['--c' as string]: color, ['--v' as string]: `${x}%`, ['--pct' as string]: Math.max(1, x) };
  switch (kind) {
    case 'none':
      return null;
    case 'ring': {
      const r = 7, len = 2 * Math.PI * r;
      return (
        <svg width="20" height="20" viewBox="0 0 20 20" style={{ flex: 'none' }} aria-hidden>
          <circle cx="10" cy="10" r={r} fill="none" stroke={`color-mix(in srgb, ${color} 20%, transparent)`} strokeWidth="3.2" />
          <circle cx="10" cy="10" r={r} fill="none" stroke={color} strokeWidth="3.2" strokeLinecap="round"
            strokeDasharray={`${(len * x) / 100} ${len}`} transform="rotate(-90 10 10)" />
        </svg>
      );
    }
    case 'segments':
      return (
        <div className="uf-bar-seg" style={style}>
          {Array.from({ length: 10 }, (_, i) => <span key={i} data-on={x > i * 10 + 2 || (i === 0 && x > 0) || undefined} />)}
        </div>
      );
    default:
      return <div className="uf-bar" data-kind={kind} style={style}><span /></div>;
  }
}

function Meter({ v, bar }: { v: number; bar: BarStyle }) {
  const x = Math.max(0, Math.min(100, v));
  return (
    <Group gap={8} wrap="nowrap" style={{ minWidth: 0 }}>
      <Text size="sm" ff="monospace" w={48} ta="right" style={{ flex: "none" }}>{x.toFixed(x < 10 ? 2 : 1)}%</Text>
      <Bar v={x} kind={bar} />
    </Group>
  );
}

const pct = (v: number | undefined, warn: number, bad: number) => {
  if (v == null) return <Text size="sm" c="dimmed">—</Text>;
  const c = v >= bad ? status.down : v >= warn ? status.partial : undefined;
  return <Text size="sm" ff="monospace" c={c ?? 'dimmed'} fw={c ? 650 : undefined}>{v.toFixed(v < 10 ? 2 : 1)}%</Text>;
};

const mono = (s: ReactNode, dim = false) => <Text size="sm" ff="monospace" c={dim ? 'dimmed' : undefined} truncate>{s}</Text>;

export const COLUMNS: Col[] = [
  { id: 'cpu', icon: IconCpu, width: 128, min: 100, value: (p) => fresh(p)?.cpu ?? -1, cell: (_, c, m) => (m ? <Meter v={m.cpu} bar={c.bar} /> : null) },
  { id: 'steal', icon: IconActivity, width: 80, min: 64, hint: 'steal_hint', value: (p) => fresh(p)?.ext?.cpu_steal ?? -1, cell: (_, __, m) => (m ? pct(m.ext?.cpu_steal, 5, 20) : null) },
  { id: 'iowait', icon: IconDatabase, width: 84, min: 64, hint: 'iowait_hint', value: (p) => fresh(p)?.ext?.cpu_iowait ?? -1, cell: (_, __, m) => (m ? pct(m.ext?.cpu_iowait, 10, 30) : null) },
  { id: 'mem', icon: IconStack2, width: 128, min: 100, value: (p) => { const m = fresh(p); return m && m.mem_total ? m.mem_used / m.mem_total : -1; },
    cell: (_, c, m) => (m && m.mem_total ? <Tooltip label={`${bytes(m.mem_used)} / ${bytes(m.mem_total)}`}><Box><Meter v={(m.mem_used / m.mem_total) * 100} bar={c.bar} /></Box></Tooltip> : null) },
  { id: 'disk', icon: IconServer2, width: 128, min: 100, value: (p) => { const m = fresh(p); return m && m.disk_total ? m.disk_used / m.disk_total : -1; },
    cell: (_, c, m) => (m && m.disk_total ? <Tooltip label={`${bytes(m.disk_used, 1)} / ${bytes(m.disk_total, 0)}`}><Box><Meter v={(m.disk_used / m.disk_total) * 100} bar={c.bar} /></Box></Tooltip> : null) },
  { id: 'load', icon: IconGauge, width: 140, min: 110, value: (p) => fresh(p)?.load1 ?? -1,
    cell: (p, c, m) => {
      if (!m) return null;
      const cores = p.sysinfo?.cores, r = cores ? m.load1 / cores : 0;
      return (
        <Tooltip label={c.t('load_hint', cores ?? '?')}>
          <Group gap={8} wrap="nowrap"><Dot color={r >= 1 ? status.down : r >= 0.7 ? status.partial : status.ok} size={8} />
            {mono(`${m.load1.toFixed(2)} ${m.load5.toFixed(2)} ${m.load15.toFixed(2)}`)}</Group>
        </Tooltip>
      );
    } },
  { id: 'net', icon: IconNetwork, width: 104, min: 80, value: (p) => { const m = fresh(p); return m ? m.rx + m.tx : -1; },
    cell: (_, __, m) => (m ? <Tooltip label={`↓ ${bitrate(m.rx)} · ↑ ${bitrate(m.tx)}`}><Box>{mono(bitrate(m.rx + m.tx))}</Box></Tooltip> : null) },
  { id: 'rx', icon: IconArrowDown, width: 100, min: 76, value: (p) => fresh(p)?.rx ?? -1, cell: (_, __, m) => (m ? mono(bitrate(m.rx)) : null) },
  { id: 'tx', icon: IconArrowUp, width: 100, min: 76, value: (p) => fresh(p)?.tx ?? -1, cell: (_, __, m) => (m ? mono(bitrate(m.tx)) : null) },
  { id: 'traffic', icon: IconTransfer, width: 104, min: 80, value: (p) => (p.traffic_24h ? p.traffic_24h.rx + p.traffic_24h.tx : -1),
    cell: (p) => (p.traffic_24h ? <Tooltip label={`↓ ${bytes(p.traffic_24h.rx, 1)} · ↑ ${bytes(p.traffic_24h.tx, 1)}`}><Box>{mono(bytes(p.traffic_24h.rx + p.traffic_24h.tx, 1))}</Box></Tooltip> : null) },
  { id: 'users', icon: IconUsers, width: 92, min: 72, hint: 'users_hint', value: (p, c) => c.rw(p)?.usersOnline ?? -1,
    cell: (p, c) => {
      const n = c.rw(p);
      if (!n) return null;
      const was = c.usersHourAgo.get(n.uuid);
      const d = was == null ? 0 : n.usersOnline - was;
      return (
        <Group gap={6} wrap="nowrap">
          {mono(num(n.usersOnline))}
          {d !== 0 && Math.abs(d) >= 5 && (
            <Text size="xs" ff="monospace" c={d < 0 && was && n.usersOnline < was * 0.5 ? status.down : 'dimmed'}>{d > 0 ? '▲' : '▼'}{Math.abs(d)}</Text>
          )}
        </Group>
      );
    } },
  { id: 'tcp', icon: IconPlugConnected, width: 80, min: 64, value: (p) => fresh(p)?.tcp ?? -1, cell: (_, __, m) => (m?.tcp ? mono(compact(m.tcp)) : null) },
  { id: 'diskio', icon: IconDatabase, width: 170, min: 120, value: (p) => { const e = fresh(p)?.ext; return e ? e.disk_read + e.disk_write : -1; },
    cell: (_, c, m) => (m?.ext ? mono(`${c.t('read')} ${bytes(m.ext.disk_read, 0)}/s · ${c.t('write')} ${bytes(m.ext.disk_write, 0)}/s`) : null) },
  { id: 'uptime', icon: IconClock, width: 104, min: 80, value: (p) => fresh(p)?.uptime ?? -1, cell: (_, c, m) => (m ? <Text size="sm" truncate>{days(m.uptime, c.lang)}</Text> : null) },
  { id: 'agent', icon: IconWifi, width: 96, min: 72, value: (p) => p.version ?? '',
    cell: (p, c) => {
      if (!p.version) return null;
      const same = !c.hubVersion || p.version === c.hubVersion;
      return (
        <Tooltip label={same ? c.t('agent_ok') : c.t('agent_old', c.hubVersion ?? '')}>
          <Group gap={6} wrap="nowrap"><IconWifi size={14} color={!p.online ? status.down : same ? status.ok : status.partial} />{mono(p.version)}</Group>
        </Tooltip>
      );
    } },
  { id: 'checks', icon: IconShieldCheck, width: 88, min: 70, value: (p) => p.failing * 1000 + p.checks,
    cell: (p, c) => {
      if (p.mode === 'agent' && !p.config.probing && !p.checks) return <Text size="xs" c="dimmed" truncate>{c.t('metrics_only')}</Text>;
      return (
        <Group gap={6} wrap="nowrap">
          {mono(String(p.checks), !p.checks)}
          {p.failing > 0 && <Tooltip label={c.t('failing', p.failing)}><Badge color="red" variant="filled" size="sm" circle>{p.failing}</Badge></Tooltip>}
        </Group>
      );
    } },
];

export const DEFAULT_COLUMNS = ['cpu', 'steal', 'mem', 'disk', 'load', 'net', 'traffic', 'users', 'uptime', 'agent'];
const SYSTEM_WIDTH = 270;
const ROW = { compact: 46, normal: 64, comfortable: 76 } as const;

function hostOf(s?: string) {
  return (s ?? '').toLowerCase().replace(/^\[|\]$/g, '').replace(/:\d+$/, '');
}

/** useRemnawaveIndex finds the panel node of a server by name, id or address. */
export function useRemnawaveIndex(nodes?: RemnawaveNode[]) {
  return useMemo(() => {
    const by = new Map<string, RemnawaveNode>();
    for (const n of nodes ?? []) {
      by.set(n.name.toLowerCase(), n);
      if (n.address) by.set(hostOf(n.address), n);
    }
    return (p: ProbeRow) => by.get(p.name.toLowerCase()) ?? by.get(p.id.toLowerCase()) ?? (p.host ? by.get(hostOf(p.host)) : undefined);
  }, [nodes]);
}

type State = 'ok' | 'warn' | 'problem' | 'offline' | 'never';

function stateOf(p: ProbeRow, alerts: Incident[]): State {
  if (!p.online) return p.version ? 'offline' : 'never';
  if (p.failing > 0) return 'problem';
  if (!p.internet || alerts.length) return 'warn';
  return 'ok';
}

const stateColor: Record<State, string> = { ok: status.ok, warn: status.partial, problem: status.down, offline: status.down, never: status.unknown };

/** ViewMenu is Beszel's "View": density, status filter, sort and visible columns. */
function ViewMenu({ prefs, setPrefs, counts, t, sort, setSort }: {
  prefs: Prefs; setPrefs: (p: Partial<Prefs>) => void; counts: Record<string, number>; t: T;
  sort: [string, boolean]; setSort: (s: [string, boolean]) => void;
}) {
  const cols = prefs.columns ?? DEFAULT_COLUMNS;
  const dens = prefs.density ?? 'normal';
  const st = prefs.status ?? 'all';
  const Item = ({ active, onClick, children, mark, wide }: { active: boolean; onClick: () => void; children: ReactNode; mark?: ReactNode; wide?: boolean }) => (
    <UnstyledButton onClick={onClick} className="uf-view-item" data-active={active || undefined}>
      <Box w={wide ? 40 : 16} style={{ display: 'flex', justifyContent: 'center' }}>{mark ?? (active ? '•' : '')}</Box>
      <Text size="sm">{children}</Text>
    </UnstyledButton>
  );
  return (
    <Group align="flex-start" gap={0} wrap="nowrap" className="uf-view">
      <Stack gap={2} p="xs" miw={170}>
        <Text size="sm" fw={650} px={6} pb={4}>{t('density')}</Text>
        {(['compact', 'normal', 'comfortable'] as const).map((d) => <Item key={d} active={dens === d} onClick={() => setPrefs({ density: d })}>{t(`d_${d}`)}</Item>)}
        <Divider my={6} />
        <Text size="sm" fw={650} px={6} pb={4}>{t('bars')}</Text>
        {BAR_STYLES.map((b) => {
          const on = (prefs.bar ?? 'pill') === b;
          return (
            <Item key={b} wide active={on} onClick={() => setPrefs({ bar: b })}
              mark={b === 'none' ? <Text size="xs" c="dimmed">62%</Text> : <Box w={40} style={{ display: 'flex', justifyContent: 'center', opacity: on ? 1 : 0.7 }}><Bar v={62} kind={b} /></Box>}>
              {t(`b_${b}`)}
            </Item>
          );
        })}
      </Stack>
      <Divider orientation="vertical" />
      <ScrollArea.Autosize mah={380} type="auto">
        <Stack gap={2} p="xs" miw={170}>
          <Text size="sm" fw={650} px={6} pb={4}>{t('status')}</Text>
          <Item active={st === 'all'} onClick={() => setPrefs({ status: 'all' })}>{t('s_all')}</Item>
          <Item active={st === 'online'} onClick={() => setPrefs({ status: 'online' })}>{t('s_online', counts.online)}</Item>
          <Item active={st === 'offline'} onClick={() => setPrefs({ status: 'offline' })}>{t('s_offline', counts.offline)}</Item>
          <Item active={st === 'problems'} onClick={() => setPrefs({ status: 'problems' })}>{t('s_problems', counts.problems)}</Item>
          <Divider my={6} />
          <Text size="sm" fw={650} px={6} pb={4}>{t('sort')}</Text>
          {['system', ...COLUMNS.map((c) => c.id)].map((id) => (
            <Item key={id} active={sort[0] === id} onClick={() => setSort([id, sort[0] === id ? !sort[1] : id !== 'system'])}
              mark={sort[0] === id ? (sort[1] ? <IconArrowDown size={13} /> : <IconArrowUp size={13} />) : undefined}>
              {t(id as 'cpu')}
            </Item>
          ))}
        </Stack>
      </ScrollArea.Autosize>
      <Divider orientation="vertical" />
      <ScrollArea.Autosize mah={380} type="auto">
        <Stack gap={4} p="xs" miw={180}>
          <Text size="sm" fw={650} px={6} pb={4}>{t('columns')}</Text>
          {COLUMNS.map((c) => (
            <Checkbox key={c.id} size="xs" px={6} py={3} label={t(c.id as 'cpu')} checked={cols.includes(c.id)}
              onChange={(e) => setPrefs({ columns: e.currentTarget.checked ? COLUMNS.map((x) => x.id).filter((id) => id === c.id || cols.includes(id)) : cols.filter((x) => x !== c.id) })} />
          ))}
          <Button size="compact-xs" variant="subtle" mt={6} onClick={() => setPrefs({ columns: undefined, widths: undefined, sort: undefined, density: undefined, status: undefined, bar: undefined })}>{t('reset')}</Button>
        </Stack>
      </ScrollArea.Autosize>
    </Group>
  );
}

/**
 * SystemsTable is the dashboard's servers view, after Beszel: one row per
 * server at a comfortable height, the columns the user picked, widths dragged
 * by hand, all of it saved to the user's profile.
 */
export function SystemsTable({ probes: all, hubURL, incidents }: { probes: ProbeRow[]; hubURL: string; incidents: Incident[] }) {
  // Checker agents are other people's machines: they have their own tab.
  const probes = useMemo(() => all.filter((p) => !isChecker(p)), [all]);
  const t = useT(L);
  const c = useCommon();
  const { lang } = useLang();
  const nav = useNavigate();
  const meta = useMeta();
  const rwq = useRemnawave();
  const rwHour = useRemnawaveUsers('range=1h');
  const { prefs, setPrefs } = usePrefs();
  const [q, setQ] = useState('');
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<ProbeRow | null>(null);
  const [install, setInstall] = useState<Install | null>(null);
  const [drag, setDrag] = useState<Record<string, number>>({});
  const isAdmin = meta.data?.user.role === 'admin';

  const rw = useRemnawaveIndex(rwq.data?.nodes);
  const usersHourAgo = useMemo(() => {
    const m = new Map<string, number>();
    for (const [id, pts] of Object.entries(rwHour.data?.nodes ?? {})) if (pts.length) m.set(id, Math.round(pts[0].avg));
    return m;
  }, [rwHour.data]);
  const alerts = useMemo(() => {
    const m = new Map<string, Incident[]>();
    for (const i of incidents) if (i.key.startsWith('host:')) {
      const id = i.key.split(':')[1];
      m.set(id, [...(m.get(id) ?? []), i]);
    }
    return m;
  }, [incidents]);
  const ctx: Ctx = { t, lang, hubVersion: meta.data?.version, rw, usersHourAgo, alerts, bar: prefs.bar ?? 'pill' };

  const visible = (prefs.columns ?? DEFAULT_COLUMNS).map((id) => COLUMNS.find((x) => x.id === id)).filter((x): x is Col => !!x)
    .filter((x) => x.id !== 'users' || rwq.data?.configured);
  const widths = { ...prefs.widths, ...drag };
  const width = (col: { id: string; width: number }) => widths[col.id] ?? col.width;
  const sort: [string, boolean] = prefs.sort ?? ['system', false];
  const setSort = (s: [string, boolean]) => setPrefs({ sort: s });
  const density = prefs.density ?? 'normal';

  const counts = useMemo(() => ({
    online: probes.filter((p) => p.online).length,
    offline: probes.filter((p) => !p.online).length,
    problems: probes.filter((p) => stateOf(p, alerts.get(p.id) ?? []) !== 'ok').length,
  }), [probes, alerts]);

  const rows = useMemo(() => {
    const needle = q.trim().toLowerCase();
    const st = prefs.status ?? 'all';
    let list = probes.filter((p) => !needle || `${p.name} ${p.id} ${p.host ?? ''} ${p.region ?? ''} ${Object.values(p.labels ?? {}).join(' ')}`.toLowerCase().includes(needle));
    if (st === 'online') list = list.filter((p) => p.online);
    if (st === 'offline') list = list.filter((p) => !p.online);
    if (st === 'problems') list = list.filter((p) => stateOf(p, alerts.get(p.id) ?? []) !== 'ok');
    const [k, desc] = sort;
    const col = COLUMNS.find((x) => x.id === k);
    const val = (p: ProbeRow) => (col?.value ? col.value(p, ctx) : p.name.toLowerCase());
    return [...list].sort((a, b) => {
      const x = val(a), y = val(b);
      const r = x < y ? -1 : x > y ? 1 : 0;
      return (desc ? -r : r) || a.name.localeCompare(b.name);
    });
  }, [probes, q, prefs.status, sort, alerts, rw, usersHourAgo]);

  // Column resize: drag the right edge of a header; saved when the mouse goes up.
  const start = useRef<{ id: string; x: number; w: number } | null>(null);
  const onResize = (id: string, w: number) => (e: React.PointerEvent) => {
    e.preventDefault();
    e.stopPropagation();
    start.current = { id, x: e.clientX, w };
    const min = id === 'system' ? 160 : COLUMNS.find((x) => x.id === id)?.min ?? 60;
    const move = (ev: PointerEvent) => {
      const s = start.current;
      if (s) setDrag({ [s.id]: Math.max(min, Math.round(s.w + ev.clientX - s.x)) });
    };
    const up = (ev: PointerEvent) => {
      const s = start.current;
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
      if (s) setPrefs({ widths: { ...prefs.widths, [s.id]: Math.max(min, Math.round(s.w + ev.clientX - s.x)) } });
      start.current = null;
      setDrag({});
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  };

  // Column moving: press a header and drag it past 6px; it lands left or
  // right of the header under the pointer. Pointer events, not HTML5 drag
  // and drop, so it works the same with a mouse, a pen or a finger.
  const [moving, setMoving] = useState<{ id: string; over?: string; after?: boolean } | null>(null);
  const moved = useRef(false);
  const move = (id: string, over: string, after: boolean) => {
    const cols = (prefs.columns ?? DEFAULT_COLUMNS).filter((x) => x !== id);
    const at = over === 'system' ? 0 : cols.indexOf(over) + (after ? 1 : 0);
    cols.splice(Math.max(0, at), 0, id);
    setPrefs({ columns: cols });
  };
  const target = (x: number, y: number) => {
    const th = document.elementFromPoint(x, y)?.closest<HTMLElement>('th[data-col]');
    if (!th) return undefined;
    const r = th.getBoundingClientRect();
    const over = th.dataset.col!;
    return { over, after: over !== 'system' && x > r.left + r.width / 2 };
  };
  const startMove = (id: string) => (e: React.PointerEvent) => {
    if (e.button !== 0) return;
    const x0 = e.clientX, y0 = e.clientY;
    moved.current = false;
    let cur: { over?: string; after?: boolean } = {};
    const mv = (ev: PointerEvent) => {
      if (!moved.current && Math.hypot(ev.clientX - x0, ev.clientY - y0) < 6) return;
      moved.current = true;
      cur = target(ev.clientX, ev.clientY) ?? {};
      setMoving({ id, ...cur });
    };
    const up = () => {
      window.removeEventListener('pointermove', mv);
      window.removeEventListener('pointerup', up);
      if (moved.current && cur.over && cur.over !== id) move(id, cur.over, !!cur.after);
      setMoving(null);
    };
    window.addEventListener('pointermove', mv);
    window.addEventListener('pointerup', up);
  };

  const remove = (p: ProbeRow) => modals.openConfirmModal({
    title: t('delete'), children: <Text size="sm">{t('confirm', p.name)}</Text>, labels: { confirm: t('delete'), cancel: c('cancel') },
    confirmProps: { color: 'red' },
    onConfirm: async () => {
      try {
        await api('DELETE', `/api/v1/probes/${enc(p.id)}`);
        await queryClient.invalidateQueries();
        notifyOk(t('deleted'));
      } catch (e) {
        notifyError(e);
      }
    },
  });
  const rotate = (p: ProbeRow) => modals.openConfirmModal({
    title: t('rotate'), children: <Text size="sm">{t('rotate_confirm')}</Text>, labels: { confirm: t('rotate'), cancel: c('cancel') },
    onConfirm: async () => {
      try {
        const res = await api<{ install: Install }>('POST', `/api/v1/probes/${enc(p.id)}/token`);
        setInstall(res.install);
      } catch (e) {
        notifyError(e);
      }
    },
  });

  const Th = ({ id, icon: I, w, hint }: { id: string; icon: Icon; w: number; hint?: string }) => {
    const active = sort[0] === id;
    const S = !active ? IconArrowsSort : sort[1] ? IconSortDescending : IconSortAscending;
    const movable = id !== 'system';
    const drop = moving && moving.over === id && moving.id !== id ? (moving.after ? 'after' : 'before') : undefined;
    return (
      <th title={hint ? t(hint as 'cpu') : movable ? t('move') : undefined} data-col={id} data-drop={drop} data-moving={moving?.id === id || undefined}>
        <UnstyledButton className="uf-sys-th" data-movable={movable || undefined}
          onPointerDown={movable ? startMove(id) : undefined}
          onClick={() => {
            if (moved.current) { moved.current = false; return; } // that was a move, not a click
            setSort([id, active ? !sort[1] : id !== 'system']);
          }}>
          <I size={15} stroke={1.7} style={{ flex: 'none', opacity: 0.7 }} />
          <Text size="sm" fw={600} truncate>{t(id as 'cpu')}</Text>
          <S size={13} className="uf-sys-sort" data-active={active || undefined} style={{ flex: 'none' }} />
        </UnstyledButton>
        <span className="uf-sys-resize" onPointerDown={onResize(id, w)} />
      </th>
    );
  };

  // The line under a server's name: where it is and what it runs.
  const regions = useMemo(() => new Intl.DisplayNames([lang], { type: 'region' }), [lang]);
  const subline = (p: ProbeRow) => {
    let where = '';
    try {
      where = p.region ? regions.of(p.region.toUpperCase()) ?? '' : '';
    } catch {
      where = p.region ?? '';
    }
    // Hardware, not the OS: an agent in Docker reports its container's Alpine.
    const hw = p.sysinfo ? [p.sysinfo.cores ? t('vcpu', p.sysinfo.cores) : '', p.sysinfo.mem_total ? bytes(p.sysinfo.mem_total, 0) : ''] : [];
    const what = p.mode === 'agent' ? hw : [t(p.mode === 'via' ? 'via' : 'hub')];
    return [where, ...what].filter(Boolean).join(' · ');
  };

  const total = width({ id: 'system', width: SYSTEM_WIDTH }) + visible.reduce((s, col) => s + width(col), 0) + 44;
  // Untouched widths fit the screen, shared in proportion; dragged ones are kept as set.
  const custom = !!Object.keys(prefs.widths ?? {}).length || !!Object.keys(drag).length;
  const floor = 160 + visible.reduce((s, col) => s + col.min, 0) + 44;
  const colW = (w: number) => (custom ? w : `${(w / total) * 100}%`);

  return (
    <Box className="uf-systems-card">
      <Group justify="space-between" align="flex-start" p="lg" pb="md" gap="md">
        <Box>
          <Text fw={700} fz={22} lh={1.2}>{t('title')}</Text>
          <Text size="sm" c="dimmed" mt={4}>{t('sub')}</Text>
        </Box>
        <Group gap="sm" wrap="wrap" justify="flex-end">
          <TextInput placeholder={t('filter')} leftSection={<IconSearch size={15} />} value={q} onChange={(e) => setQ(e.currentTarget.value)} w={240} radius="md" />
          <Popover position="bottom-end" shadow="lg" radius="md" withinPortal middlewares={{ flip: false, shift: true }}>
            <Popover.Target>
              <Button variant="default" radius="md" leftSection={<IconAdjustmentsHorizontal size={16} />}>{t('view')}</Button>
            </Popover.Target>
            <Popover.Dropdown p={0}>
              <ViewMenu prefs={prefs} setPrefs={setPrefs} counts={counts} t={t} sort={sort} setSort={setSort} />
            </Popover.Dropdown>
          </Popover>
          {isAdmin && <Button radius="md" leftSection={<IconPlus size={16} />} onClick={() => setAdding(true)}>{t('add')}</Button>}
        </Group>
      </Group>

      <Box className="uf-scroll-x" px="md" pb="md">
        <table className="uf-sys" data-density={density} style={{ width: custom ? `max(100%, ${total}px)` : `max(100%, ${floor}px)`, ['--uf-row' as string]: `${ROW[density]}px` }}>
          <colgroup>
            <col style={{ width: colW(width({ id: 'system', width: SYSTEM_WIDTH })) }} />
            {visible.map((col) => <col key={col.id} style={{ width: colW(width(col)) }} />)}
            <col style={{ width: colW(44) }} />
          </colgroup>
          <thead>
            <tr>
              <Th id="system" icon={IconServer2} w={width({ id: 'system', width: SYSTEM_WIDTH })} />
              {visible.map((col) => <Th key={col.id} id={col.id} icon={col.icon} w={width(col)} hint={col.hint} />)}
              <th />
            </tr>
          </thead>
          <tbody>
            {rows.map((p) => {
              const m = fresh(p);
              const st = stateOf(p, alerts.get(p.id) ?? []);
              const note = !m && (st === 'offline'
                ? t('offline', duration((Date.now() - Date.parse(p.since)) / 1000, lang))
                : st === 'never' ? t('never') : !p.internet ? t('no_net') : p.mode === 'via' ? t('via') : '');
              return (
                <tr key={p.id} data-state={st} {...clickableRow(() => nav(`/probes/${enc(p.id)}`))}>
                  <td>
                    <Group gap={12} wrap="nowrap" style={{ minWidth: 0 }}>
                      <Dot color={stateColor[st]} pulse={st === 'ok'} size={11} />
                      <span className="uf-sys-flag" aria-hidden>{flag(p.region) || '📍'}</span>
                      <Tooltip label={[p.host, p.mode === 'agent' ? '' : t(p.mode === 'via' ? 'via' : 'hub')].filter(Boolean).join(' · ') || p.id} openDelay={400}>
                        <Box style={{ minWidth: 0 }}>
                          <Text className="uf-sys-name" truncate>{p.name}</Text>
                          {density !== 'compact' && <Text className="uf-sys-sub" c="dimmed" truncate>{subline(p)}</Text>}
                        </Box>
                      </Tooltip>
                      {(alerts.get(p.id) ?? []).slice(0, 1).map((a) => a.host && (
                        <Tooltip key={a.id} label={a.name}><Badge size="xs" variant="light" color="orange" leftSection={<IconGauge size={10} />} style={{ flex: 'none' }}>{a.host.metric}</Badge></Tooltip>
                      ))}
                    </Group>
                  </td>
                  {note ? (
                    <td colSpan={visible.length}><Text size="sm" c={st === 'offline' ? status.down : 'dimmed'} truncate>{note}</Text></td>
                  ) : visible.map((col) => <td key={col.id}>{col.cell(p, ctx, m) ?? <Text size="sm" c="dimmed">—</Text>}</td>)}
                  <td onClick={(e) => e.stopPropagation()}>
                    <Menu position="bottom-end" withinPortal>
                      <Menu.Target><ActionIcon variant="subtle" color="gray" aria-label={t('menu', p.name)}><IconDots size={18} /></ActionIcon></Menu.Target>
                      <Menu.Dropdown>
                        <Menu.Item leftSection={<IconExternalLink size={15} />} onClick={() => nav(`/probes/${enc(p.id)}`)}>{t('open')}</Menu.Item>
                        {isAdmin && !p.managed && (
                          <>
                            <Menu.Item leftSection={<IconPencil size={15} />} onClick={() => setEditing(p)}>{t('edit')}</Menu.Item>
                            {p.mode === 'agent' && <Menu.Item leftSection={<IconKey size={15} />} onClick={() => rotate(p)}>{t('rotate')}</Menu.Item>}
                            <Menu.Divider />
                            <Menu.Item color="red" leftSection={<IconTrash size={15} />} onClick={() => remove(p)}>{t('delete')}</Menu.Item>
                          </>
                        )}
                      </Menu.Dropdown>
                    </Menu>
                  </td>
                </tr>
              );
            })}
            {!rows.length && <tr><td colSpan={visible.length + 2}><Text c="dimmed" size="sm" ta="center" py="md">{t('empty')}</Text></td></tr>}
          </tbody>
        </table>
      </Box>

      {adding && <ProbeForm opened={adding} onClose={() => setAdding(false)} hubURL={hubURL} />}
      {editing && <ProbeForm opened={!!editing} onClose={() => setEditing(null)} initial={editing.config} network={editing.network} hubURL={hubURL} />}
      <TokenModal install={install} hubURL={hubURL} title={t('new_token')} onClose={() => setInstall(null)} />
    </Box>
  );
}
