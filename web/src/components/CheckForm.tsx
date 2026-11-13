import {
  Accordion, Alert, Autocomplete, Box, Button, Code, Group, JsonInput, Modal, MultiSelect, NumberInput, PasswordInput, SegmentedControl, Select, Stack, Switch,
  TagsInput, Text, Textarea, TextInput,
} from '@mantine/core';
import { IconAlertCircle, IconInfoCircle } from '@tabler/icons-react';
import { useMemo, useState } from 'react';
import { api, ApiError, queryClient, useChecks, useMeta, type CatalogEntry, type CatalogField, type CheckConfig } from '../api';
import { parseGoDuration } from '../format';
import { useParentLabels, useParentOptions } from './checks/parents';
import { useGuardedClose } from './guards';
import { compactDuration } from './SettingsKit';
import { useCommon, useLang, useT } from '../i18n';
import { checkText } from './checkText';
import { CopyBlock, notifyOk } from './Misc';
import { carriesSecrets, ProbePicker } from './ProbePicker';

const L = {
  ru: {
    create: 'Новая проверка', edit: 'Изменить проверку', type: 'Что проверяем', type_ph: 'Найдите по названию: HTTP, Redis, ping…', name: 'Название', id: 'ID', id_hint: 'Латиница, цифры, - и _. Не меняется после создания',
    group: 'Группа', group_hint: 'Например «VPN-ноды»: раздел на странице проверок',
    deps: 'Зависит от', deps_hint: 'Если это лежит, тревога по проверке не уходит отдельно, а попадает в карточку причины.', d_auto: 'Автоматически', d_manual: 'Вручную', d_none: 'Ни от чего', d_any: 'Тревога сворачивается, если лежит любой из выбранных.',
    d_found: 'Найдено по адресу: {0}', d_found_none: 'По адресу ничего не нашлось', d_pick: 'Проверки, ноды, серверы', d_checks: 'Проверки', d_nodes: 'Ноды панели', d_servers: 'Серверы',
    tags: 'Метки', tags_hint: 'de, vless, critical: для фильтров и правил уведомлений. Enter добавляет метку',
    source: 'Источник', src_link: 'Ссылка', src_sub: 'Подписка', src_json: 'Outbound JSON',
    link: 'Ссылка на конфиг', link_ph: 'vless://… vmess://… trojan://… ss://… hy2://…',
    sub: 'URL подписки', sub_hint: 'Каждый конфиг подписки станет отдельной проверкой. Берите подписку тестового пользователя.',
    ua: 'User-Agent', ua_hint: 'Панели отдают разный формат в зависимости от клиента', filter: 'Фильтр по имени (regex)', refresh: 'Обновлять подписку каждые',
    outbound: 'Xray outbound (JSON)', advanced: 'Тонкая настройка', probe_url: 'URL лёгкой проверки', dl_url: 'URL для загрузки',
    dl_bytes: 'Сколько качать, байт', heavy: 'Загрузка каждые N прогонов', heavy_hint: 'Ловит обрыв соединения на ~16 КБ (ТСПУ). 0 значит по умолчанию (5), -1 значит никогда',
    stall: 'Обрыв, если нет данных дольше', egress: 'Ожидаемый IP выхода', egress_hint: 'IP или подсеть, Enter после каждого',
    mt_link: 'Ссылка tg://proxy или t.me/proxy', mt_or: 'или адрес и секрет', dc_all: 'все', address: 'Адрес (host:port)', secret: 'Секрет', dc: 'DC Telegram',
    keyword: 'Ключевое слово', keyword_hint: 'Проверка упадёт, если этого текста нет в ответе', keyword_invert: 'Наоборот: падать, если текст есть',
    push_url: 'Адрес для задачи', push_hint: 'Вызывайте его из cron или скрипта. Если вызовов нет дольше окна ожидания, проверка упадёт. status=down сообщит об ошибке сразу.',
    needs: 'На пробе должно быть: {0}', needs_docker: 'доступ к Docker', needs_chromium: 'браузер Chromium', needs_systemd: 'systemd', needs_pm2: 'PM2', needs_tailscale: 'Tailscale', needs_icmp: 'разрешённый ICMP',
    needs_hint: 'Иначе проверка честно покажет «недоступно на этой пробе».', hub_only: 'Эта проверка работает на самом хабе, выбирать пробы не нужно.',
    schedule: 'Расписание и оповещения', interval: 'Интервал', timeout: 'Таймаут', retries: 'Неудач подряд до тревоги',
    min_failing: 'Минимум проб со сбоем', severity: 'Важность', sev_auto: 'Авто (не работает или блок значит критично)', sev_crit: 'Всегда критично', sev_warn: 'Только предупреждение',
    probes: 'С каких проб проверять', probes_hint: 'Конкретный сервер можно выбрать, даже если на нём выключены проверки.', exclude: 'Кроме',
    all_probes: 'Вся инфраструктура', created: 'Проверка создана', updated: 'Сохранено',
    bad_json: 'Некорректный JSON', t_xray: 'VPN-конфиг', d_xray: 'Реальный запрос через Xray-туннель: VLESS, VMess, Trojan, Shadowsocks, Hysteria2', t_mtproto: 'Telegram-прокси', d_mtproto: 'Обмен с Telegram через MTProxy или telemt',
  },
  en: {
    create: 'New check', edit: 'Edit check', type: 'What to check', type_ph: 'Search by name: HTTP, Redis, ping…', name: 'Name', id: 'ID', id_hint: 'Latin letters, digits, - and _. Fixed after creation',
    group: 'Group', group_hint: 'E.g. "VPN nodes": a section of the checks page',
    deps: 'Depends on', deps_hint: 'When this is down, the check does not alert on its own: it joins the cause\'s card.', d_auto: 'Automatic', d_manual: 'By hand', d_none: 'Nothing', d_any: 'The alert folds when any one of these is down.',
    d_found: 'Found by address: {0}', d_found_none: 'Nothing found by address', d_pick: 'Checks, nodes, servers', d_checks: 'Checks', d_nodes: 'Panel nodes', d_servers: 'Servers',
    tags: 'Tags', tags_hint: 'de, vless, critical: for filters and alert rules. Enter adds a tag',
    source: 'Source', src_link: 'Link', src_sub: 'Subscription', src_json: 'Outbound JSON',
    link: 'Config link', link_ph: 'vless://… vmess://… trojan://… ss://… hy2://…',
    sub: 'Subscription URL', sub_hint: 'Every config in it becomes its own check. Use a canary user’s subscription.',
    ua: 'User-Agent', ua_hint: 'Panels serve different formats per client', filter: 'Name filter (regex)', refresh: 'Refresh the subscription every',
    outbound: 'Xray outbound (JSON)', advanced: 'Fine tuning', probe_url: 'Light probe URL', dl_url: 'Download URL',
    dl_bytes: 'Bytes to download', heavy: 'Download every N runs', heavy_hint: 'Catches the ~16 KB DPI freeze. 0 = default (5), -1 = never',
    stall: 'Stalled if no data for', egress: 'Expected exit IP', egress_hint: 'IP or CIDR, press Enter after each',
    mt_link: 'tg://proxy or t.me/proxy link', mt_or: 'or address and secret', dc_all: 'all', address: 'Address (host:port)', secret: 'Secret', dc: 'Telegram DC',
    keyword: 'Keyword', keyword_hint: 'The check fails when this text is not in the response', keyword_invert: 'Reverse: fail when the text is there',
    push_url: 'URL for your job', push_hint: 'Call it from cron or a script. With no calls for longer than the window the check fails. status=down reports a failure at once.',
    needs: 'The probe needs: {0}', needs_docker: 'access to Docker', needs_chromium: 'the Chromium browser', needs_systemd: 'systemd', needs_pm2: 'PM2', needs_tailscale: 'Tailscale', needs_icmp: 'ICMP allowed',
    needs_hint: 'Otherwise the check honestly says "not available on this probe".', hub_only: 'This check runs on the hub itself, no probes to pick.',
    schedule: 'Schedule and alerting', interval: 'Interval', timeout: 'Timeout', retries: 'Failures in a row before alerting',
    min_failing: 'Minimum failing probes', severity: 'Severity', sev_auto: 'Auto (down or blocked = critical)', sev_crit: 'Always critical', sev_warn: 'Warning only',
    probes: 'Check from', probes_hint: 'A specific server can be picked even with probing off.', exclude: 'Except',
    all_probes: 'All infrastructure', created: 'Check created', updated: 'Saved',
    bad_json: 'Invalid JSON', t_xray: 'VPN config', d_xray: 'A real request through an Xray tunnel: VLESS, VMess, Trojan, Shadowsocks, Hysteria2', t_mtproto: 'Telegram proxy', d_mtproto: 'An exchange with Telegram through MTProxy or telemt',
  },
};

const intervals = ['20s', '30s', '1m', '2m', '5m', '10m', '30m', '1h'];
const timeouts = ['5s', '10s', '15s', '25s', '45s'];
const refreshes = ['5m', '10m', '30m', '1h'];
/**
 * durationOptions lists the presets plus the value the check already has
 * (15s, 2m30s…), so an unusual value shows as itself, not as the placeholder.
 * Values are compact ("1m", not Go's "1m0s"); the hub parses both.
 */
function durationOptions(presets: string[], current?: string) {
  const cur = compactDuration(current);
  const all = cur && !presets.includes(cur) ? [...presets, cur].sort((a, b) => parseGoDuration(a) - parseGoDuration(b)) : presets;
  return all.map((x) => ({ value: x, label: x }));
}
const uaPresets = ['v2rayN/7.0', 'Happ/2.0', 'Streisand/1.0', 'clash-verge/2.0', 'sing-box/1.11'];
const OWN_KEYS = ['xray', 'mtproto', 'http', 'tcp', 'tls', 'dns', 'params'] as const;
const STRUCT_TYPES = ['http', 'dns', 'tcp', 'tls'];

function slugify(s: string) {
  const map: Record<string, string> = { а: 'a', б: 'b', в: 'v', г: 'g', д: 'd', е: 'e', ё: 'e', ж: 'zh', з: 'z', и: 'i', й: 'y', к: 'k', л: 'l', м: 'm', н: 'n', о: 'o', п: 'p', р: 'r', с: 's', т: 't', у: 'u', ф: 'f', х: 'h', ц: 'c', ч: 'ch', ш: 'sh', щ: 'sch', ы: 'y', э: 'e', ю: 'yu', я: 'ya' };
  return s.toLowerCase().split('').map((c) => map[c] ?? c).join('').replace(/[^a-z0-9_-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 60);
}

/** Drop empty strings/arrays/objects so the hub applies its defaults. */
function clean<T>(v: T): T {
  if (Array.isArray(v)) return v.filter((x) => x !== '' && x != null).map(clean) as T;
  if (v && typeof v === 'object') {
    const out: Record<string, unknown> = {};
    for (const [k, x] of Object.entries(v)) {
      const c = clean(x);
      if (c === '' || c == null || (Array.isArray(c) && c.length === 0) || (typeof c === 'object' && !Array.isArray(c) && Object.keys(c as object).length === 0)) continue;
      out[k] = c;
    }
    return out as T;
  }
  return v;
}

/** presetOf finds which catalog entry an existing check belongs to. */
function presetOf(c?: CheckConfig): string {
  if (!c) return 'http';
  const http = c.http as Record<string, unknown> | undefined;
  if (c.type === 'http' && http?.json_path) return 'json-query';
  if (c.type === 'http' && http?.keyword) return 'keyword';
  return c.type;
}

const key = (e: CatalogEntry) => e.preset || e.type;

export function newToken() {
  const b = crypto.getRandomValues(new Uint8Array(20));
  return Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
}

// Headers are one "Name: value" per line in the form and a map in the API.
const headersToText = (h?: Record<string, string>) => Object.entries(h ?? {}).map(([k, v]) => `${k}: ${v}`).join('\n');
function textToHeaders(s: string) {
  const out: Record<string, string> = {};
  for (const line of s.split('\n')) {
    const i = line.indexOf(':');
    if (i > 0) out[line.slice(0, i).trim()] = line.slice(i + 1).trim();
  }
  return out;
}

export function CheckForm({ initial, opened, onClose }: { initial?: CheckConfig; opened: boolean; onClose: () => void }) {
  const t = useT(L);
  const c = useCommon();
  const { lang } = useLang();
  const words = checkText[lang] as Record<string, string>;
  const meta = useMeta();
  const editing = !!initial;
  const [d, setD] = useState<CheckConfig>(() => initial ? structuredClone(initial) : { id: '', type: 'http', http: { url: '' } });
  const [picked, setPicked] = useState(presetOf(initial));
  const [idTouched, setIdTouched] = useState(editing);
  const [src, setSrc] = useState<'link' | 'sub' | 'json'>(initial?.xray?.subscription ? 'sub' : initial?.xray?.outbound ? 'json' : 'link');
  const [json, setJson] = useState(initial?.xray?.outbound ? JSON.stringify(initial.xray.outbound, null, 2) : '');
  const [headers, setHeaders] = useState(headersToText(initial?.http?.headers));
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);
  // What the form started from; anything else is input a stray click must not throw away.
  const snapshot = (x: unknown[]) => JSON.stringify(x);
  const [pristine] = useState(() => snapshot([d, picked, src, json, headers]));
  const guard = useGuardedClose(snapshot([d, picked, src, json, headers]) !== pristine, onClose);
  const checks = useChecks({ live: false });

  const set = (patch: Partial<CheckConfig>) => setD((x) => ({ ...x, ...patch }));
  const groups = useMemo(() => [...new Set((checks.data?.checks ?? []).map((r) => r.group).filter(Boolean) as string[])], [checks.data]);
  const parentLabel = useParentLabels();
  const parentOptions = useParentOptions(d.id).map((g) => ({ ...g, group: t(g.group === 'checks' ? 'd_checks' : g.group === 'nodes' ? 'd_nodes' : 'd_servers') }));
  const autoParents = (checks.data?.checks ?? []).find((r) => r.id === d.id)?.parents ?? [];
  // The mode is its own state: a hand-set list may be empty while being picked.
  const [manualDeps, setManualDeps] = useState(!!initial?.depends_on?.length && initial.depends_on[0] !== 'none');
  const depMode = d.depends_on?.[0] === 'none' ? 'none' : manualDeps || d.depends_on?.length ? 'manual' : 'auto';
  const allTags = useMemo(() => [...new Set((checks.data?.checks ?? []).flatMap((r) => r.tags ?? []))].sort(), [checks.data]);
  const catalog = meta.data?.check_catalog ?? [];
  const info = catalog.find((e) => key(e) === picked);

  const title = (k: string) => words[`t_${k}`] ?? (L[lang] as Record<string, string>)[`t_${k}`] ?? k;
  const desc = (k: string) => words[`d_${k}`] ?? (L[lang] as Record<string, string>)[`d_${k}`] ?? '';

  const typeOptions = useMemo(() => {
    const cats = [...new Set(catalog.map((e) => e.category))];
    return cats.map((cat) => ({
      group: words[`c_${cat}`] ?? cat,
      items: catalog.filter((e) => e.category === cat).map((e) => ({ value: key(e), label: title(key(e)) })),
    }));
  }, [catalog, words]);

  // Untrusted checker agents never get a check with a VPN link or a password.
  const secrets = carriesSecrets(d, catalog);

  const pick = (k: string) => {
    const e = catalog.find((x) => key(x) === k);
    if (!e) return;
    setPicked(k);
    setD((x) => {
      const next: CheckConfig = { ...x, type: e.type };
      if (e.type === 'xray') next.xray ??= { link: '' };
      else if (e.type === 'mtproto') next.mtproto ??= { link: '' };
      else if (STRUCT_TYPES.includes(e.type)) (next as unknown as Record<string, unknown>)[e.type] ??= {};
      else {
        const params: Record<string, string> = {};
        for (const f of e.fields ?? []) if (f.default) params[f.name] = f.default;
        if (e.type === 'push') params.token = newToken();
        next.params = params;
      }
      return next;
    });
  };

  // A field's value lives in the type's own struct for http, dns, tcp, tls, and in params for the rest.
  const structured = info ? STRUCT_TYPES.includes(info.type) : false;
  const bag = (): Record<string, unknown> => (structured ? ((d as unknown as Record<string, Record<string, unknown>>)[d.type] ?? {}) : d.params ?? {});
  const fieldValue = (name: string): string => {
    const v = bag()[name];
    if (name === 'accepted_status' && Array.isArray(v)) return v.join(', ');
    return v == null ? '' : String(v);
  };
  const setField = (f: CatalogField, raw: string | boolean) => {
    if (structured) {
      let v: unknown = raw;
      if (f.name === 'accepted_status') v = String(raw).split(/[,\s]+/).filter(Boolean);
      else if (f.kind === 'number') v = raw === '' ? undefined : Number(raw);
      set({ [d.type]: { ...bag(), [f.name]: v } } as Partial<CheckConfig>);
    } else {
      const v = typeof raw === 'boolean' ? (raw ? 'true' : '') : String(raw);
      set({ params: { ...(d.params ?? {}), [f.name]: v } });
    }
  };
  const visible = (f: CatalogField) => {
    if (!f.show_if) return true;
    const [k, want] = f.show_if.split('=');
    return (fieldValue(k) || info?.fields?.find((x) => x.name === k)?.default || '') === want;
  };

  const renderField = (f: CatalogField) => {
    if (!visible(f) || (f.name === 'token' && d.type === 'push')) return null;
    const common = { label: words[`f_${f.name}`] ?? f.name, description: words[`h_${f.name}`], required: f.required };
    const mono = { input: { fontFamily: 'var(--mantine-font-family-monospace)', fontSize: 12 } };
    if (f.name === 'headers' && d.type === 'http') {
      return <Textarea key={f.name} {...common} autosize minRows={2} maxRows={8} styles={mono} placeholder="Authorization: Bearer …" value={headers} onChange={(e) => { const v = e.currentTarget.value; setHeaders(v); set({ http: { ...d.http!, headers: textToHeaders(v) } }); }} />;
    }
    switch (f.kind) {
      case 'password':
        return <PasswordInput key={f.name} {...common} autoComplete="new-password" value={fieldValue(f.name)} onChange={(e) => setField(f, e.currentTarget.value)} />;
      case 'number':
        return <NumberInput key={f.name} {...common} placeholder={f.default} hideControls value={fieldValue(f.name)} onChange={(v) => setField(f, String(v))} />;
      case 'bool':
        return <Switch key={f.name} label={common.label} description={common.description} checked={fieldValue(f.name) === 'true'} onChange={(e) => setField(f, e.currentTarget.checked)} />;
      case 'select':
        return <Select key={f.name} {...common} allowDeselect={!f.required} clearable={!f.required} placeholder={f.default} data={(f.options ?? []).map((o) => ({ value: o, label: words[`o_${o}`] ?? o }))} value={fieldValue(f.name) || f.default || null} onChange={(v) => setField(f, v ?? '')} />;
      case 'textarea':
        return <Textarea key={f.name} {...common} autosize minRows={3} maxRows={14} styles={mono} value={fieldValue(f.name)} onChange={(e) => setField(f, e.currentTarget.value)} />;
    }
    return <TextInput key={f.name} {...common} value={fieldValue(f.name)} onChange={(e) => setField(f, e.currentTarget.value)} />;
  };

  const submit = async () => {
    setErr('');
    const body: CheckConfig = { ...d };
    for (const k of OWN_KEYS) {
      const own = d.type === k || (k === 'params' && !STRUCT_TYPES.includes(d.type) && d.type !== 'xray' && d.type !== 'mtproto');
      if (!own) delete body[k];
    }
    if (d.type === 'xray') {
      const x = { ...(d.xray ?? {}) };
      if (src !== 'link') delete x.link;
      if (src !== 'sub') { delete x.subscription; delete x.user_agent; delete x.filter; }
      if (src === 'json') {
        try {
          x.outbound = JSON.parse(json);
        } catch {
          setErr(t('bad_json'));
          return;
        }
      } else delete x.outbound;
      body.xray = x;
      if (src !== 'sub') delete body.refresh;
    }
    setBusy(true);
    try {
      const payload = clean(body);
      if (editing) await api('PUT', `/api/v1/checks/${encodeURIComponent(d.id)}`, payload);
      else await api('POST', '/api/v1/checks', payload);
      await queryClient.invalidateQueries();
      notifyOk(editing ? t('updated') : t('created'));
      onClose();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const fields = info?.fields ?? [];
  const main = fields.filter((f) => !f.advanced);
  const extra = fields.filter((f) => f.advanced);
  // http has its own keyword fields; other text-returning types share the spec-level ones.
  const sharedKeyword = !!info?.keyword && info.type !== 'http';
  // The job runs elsewhere: it needs the hub's public address, not the one this browser happens to use.
  const base = (meta.data?.public_url || window.location.origin).replace(/\/+$/, '');
  const pushURL = d.type === 'push' && d.params?.token ? `${base}/api/push/${d.params.token}?status=up&msg=OK&ping=` : '';

  return (
    <Modal opened={opened} {...guard.modalProps} title={<Text fw={700} fz="lg">{editing ? t('edit') : t('create')}</Text>} size="lg">
      <Stack gap="md">
        {err && <Alert color="red" icon={<IconAlertCircle size={16} />}>{err}</Alert>}
        <Select
          label={t('type')}
          placeholder={t('type_ph')}
          description={desc(picked)}
          data={typeOptions}
          value={picked}
          disabled={editing}
          searchable
          allowDeselect={false}
          maxDropdownHeight={380}
          nothingFoundMessage="—"
          onChange={(v) => v && pick(v)}
          renderOption={({ option }) => (
            <Box>
              <Text size="sm">{option.label}</Text>
              <Text size="xs" c="dimmed" lineClamp={1}>{desc(option.value)}</Text>
            </Box>
          )}
        />
        <TextInput
          label={t('name')}
          value={d.name ?? ''}
          onChange={(e) => {
            const name = e.currentTarget.value;
            set(idTouched ? { name } : { name, id: slugify(name) });
          }}
          required
        />
        {info?.needs && (
          <Alert color="blue" variant="light" icon={<IconInfoCircle size={16} />}>
            {t('needs', t(`needs_${info.needs}` as 'needs_docker'))}. {t('needs_hint')}
          </Alert>
        )}

        {d.type === 'xray' && (
          <Stack gap="sm">
            <SegmentedControl value={src} onChange={(v) => setSrc(v as typeof src)} data={[{ value: 'link', label: t('src_link') }, { value: 'sub', label: t('src_sub') }, { value: 'json', label: t('src_json') }]} />
            {src === 'link' && (
              <Textarea label={t('link')} placeholder={t('link_ph')} autosize minRows={2} maxRows={5} styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)', fontSize: 12 } }}
                value={d.xray?.link ?? ''} onChange={(e) => set({ xray: { ...d.xray, link: e.currentTarget.value.trim() } })} required />
            )}
            {src === 'sub' && (
              <>
                <TextInput label={t('sub')} description={t('sub_hint')} value={d.xray?.subscription ?? ''} onChange={(e) => set({ xray: { ...d.xray, subscription: e.currentTarget.value.trim() } })} required />
                <Group grow align="flex-start">
                  <Autocomplete label={t('ua')} description={t('ua_hint')} data={uaPresets} value={d.xray?.user_agent ?? ''} onChange={(v) => set({ xray: { ...d.xray, user_agent: v } })} />
                  <TextInput label={t('filter')} value={d.xray?.filter ?? ''} onChange={(e) => set({ xray: { ...d.xray, filter: e.currentTarget.value } })} />
                </Group>
                <Select label={t('refresh')} data={durationOptions(refreshes, d.refresh)} value={compactDuration(d.refresh) || '10m'} onChange={(v) => set({ refresh: v ?? '' })} />
              </>
            )}
            {src === 'json' && <JsonInput label={t('outbound')} autosize minRows={8} maxRows={18} formatOnBlur value={json} onChange={setJson} />}
            <Accordion variant="contained" radius="md">
              <Accordion.Item value="adv">
                <Accordion.Control>{t('advanced')}</Accordion.Control>
                <Accordion.Panel>
                  <Stack gap="sm">
                    <Group grow>
                      <TextInput label={t('probe_url')} placeholder="https://www.gstatic.com/generate_204" value={d.xray?.url ?? ''} onChange={(e) => set({ xray: { ...d.xray, url: e.currentTarget.value } })} />
                      <TextInput label={t('dl_url')} placeholder="https://speed.cloudflare.com/__down?bytes=131072" value={d.xray?.download_url ?? ''} onChange={(e) => set({ xray: { ...d.xray, download_url: e.currentTarget.value } })} />
                    </Group>
                    <Group grow align="flex-start">
                      <NumberInput label={t('heavy')} description={t('heavy_hint')} min={-1} value={d.xray?.heavy_every ?? 0} onChange={(v) => set({ xray: { ...d.xray, heavy_every: Number(v) || 0 } })} />
                      <NumberInput label={t('dl_bytes')} min={0} step={16384} placeholder="131072" value={d.xray?.download_bytes ?? ''} onChange={(v) => set({ xray: { ...d.xray, download_bytes: Number(v) || undefined } })} />
                      <Select label={t('stall')} data={['2s', '5s', '10s']} placeholder="5s" value={d.xray?.stall_timeout ?? null} onChange={(v) => set({ xray: { ...d.xray, stall_timeout: v ?? undefined } })} clearable />
                    </Group>
                    <TagsInput label={t('egress')} description={t('egress_hint')} value={d.xray?.expect_egress ?? []} onChange={(v) => set({ xray: { ...d.xray, expect_egress: v } })} />
                  </Stack>
                </Accordion.Panel>
              </Accordion.Item>
            </Accordion>
          </Stack>
        )}

        {d.type === 'mtproto' && (
          <Stack gap="sm">
            <TextInput label={t('mt_link')} placeholder="tg://proxy?server=…&port=443&secret=ee…" value={d.mtproto?.link ?? ''} onChange={(e) => set({ mtproto: { ...d.mtproto, link: e.currentTarget.value.trim() } })} />
            <Text size="xs" c="dimmed">{t('mt_or')}</Text>
            <Group grow>
              <TextInput label={t('address')} value={d.mtproto?.address ?? ''} onChange={(e) => set({ mtproto: { ...d.mtproto, address: e.currentTarget.value.trim() } })} />
              <TextInput label={t('secret')} value={d.mtproto?.secret ?? ''} onChange={(e) => set({ mtproto: { ...d.mtproto, secret: e.currentTarget.value.trim() } })} />
              <NumberInput label={t('dc')} min={1} max={5} placeholder={t('dc_all')} value={d.mtproto?.dc ?? ''} onChange={(v) => set({ mtproto: { ...d.mtproto, dc: Number(v) || undefined } })} maw={110} />
            </Group>
          </Stack>
        )}

        {main.map(renderField)}

        {sharedKeyword && (
          <Stack gap="xs">
            <TextInput label={t('keyword')} description={t('keyword_hint')} value={d.keyword ?? ''} onChange={(e) => set({ keyword: e.currentTarget.value })} />
            {!!d.keyword && <Switch label={t('keyword_invert')} checked={!!d.keyword_invert} onChange={(e) => set({ keyword_invert: e.currentTarget.checked })} />}
          </Stack>
        )}

        {pushURL && (
          <Stack gap={4}>
            <CopyBlock label={t('push_url')} value={pushURL} />
            <Text size="xs" c="dimmed">{t('push_hint')}</Text>
            <Code block>{`*/5 * * * * curl -fsS "${pushURL}" >/dev/null`}</Code>
          </Stack>
        )}

        {extra.some(visible) && (
          <Accordion variant="contained" radius="md">
            <Accordion.Item value="adv">
              <Accordion.Control>{t('advanced')}</Accordion.Control>
              <Accordion.Panel><Stack gap="sm">{extra.map(renderField)}</Stack></Accordion.Panel>
            </Accordion.Item>
          </Accordion>
        )}

        {info?.hub_only ? (
          <Text size="xs" c="dimmed">{t('hub_only')}</Text>
        ) : (
          <ProbePicker mode="selectors" summary label={t('probes')} description={t('probes_hint')} placeholder={t('all_probes')} secrets={secrets}
            value={d.probes ?? []} exclude={d.exclude} onChange={(v) => set({ probes: v })} />
        )}
        <Accordion variant="contained" radius="md">
          <Accordion.Item value="schedule">
            <Accordion.Control>{t('schedule')}</Accordion.Control>
            <Accordion.Panel>
              <Stack gap="sm">
                <Group grow align="flex-start">
                  <Autocomplete label={t('group')} description={t('group_hint')} data={groups} value={d.group ?? ''} onChange={(group) => set({ group })} />
                  <TextInput label={t('id')} description={editing ? undefined : t('id_hint')} value={d.id} disabled={editing} onChange={(e) => { setIdTouched(true); set({ id: e.currentTarget.value }); }} required />
                </Group>
                <TagsInput label={t('tags')} description={t('tags_hint')} data={allTags} value={d.tags ?? []} clearable
                  onChange={(v) => set({ tags: v.length ? [...new Set(v.map((x) => x.trim().toLowerCase()).filter(Boolean))] : undefined })} />
                <Stack gap={6}>
                  <Text size="sm" fw={500}>{t('deps')}</Text>
                  <Text size="xs" c="dimmed">{t('deps_hint')}</Text>
                  <SegmentedControl size="xs" value={depMode} data={[{ value: 'auto', label: t('d_auto') }, { value: 'manual', label: t('d_manual') }, { value: 'none', label: t('d_none') }]}
                    onChange={(v) => {
                      setManualDeps(v === 'manual');
                      // Only the checks carry over: the panel's nodes and their agents count
                      // together (all nodes on the address down), which a hand-set list,
                      // where any one parent down is enough, cannot say.
                      const checksOnly = autoParents.filter((p) => !p.startsWith('rw:') && !p.startsWith('probe:'));
                      set({ depends_on: v === 'auto' ? undefined : v === 'none' ? ['none'] : checksOnly.length ? checksOnly : undefined });
                    }} />
                  {depMode === 'auto' && <Text size="xs" c="dimmed">{autoParents.length ? t('d_found', autoParents.map(parentLabel).join(', ')) : t('d_found_none')}</Text>}
                  {depMode === 'manual' && <Text size="xs" c="dimmed">{t('d_any')}</Text>}
                  {depMode === 'manual' && (
                    <MultiSelect size="xs" placeholder={t('d_pick')} data={parentOptions} value={d.depends_on ?? []} searchable
                      onChange={(v) => set({ depends_on: v.length ? v : undefined })} />
                  )}
                </Stack>
                <Group grow>
                  <Select label={t('interval')} data={durationOptions(intervals, d.interval)} placeholder="1m" value={compactDuration(d.interval) || null} onChange={(v) => set({ interval: v ?? undefined })} clearable />
                  <Select label={t('timeout')} data={durationOptions(timeouts, d.timeout)} placeholder={d.type === 'xray' ? '25s' : '15s'} value={compactDuration(d.timeout) || null} onChange={(v) => set({ timeout: v ?? undefined })} clearable />
                </Group>
                <Group grow>
                  <NumberInput label={t('retries')} min={1} max={10} placeholder="2" value={d.retries ?? ''} onChange={(v) => set({ retries: Number(v) || undefined })} />
                  <NumberInput label={t('min_failing')} min={1} max={20} placeholder="1" value={d.min_failing ?? ''} onChange={(v) => set({ min_failing: Number(v) || undefined })} />
                </Group>
                <Select label={t('severity')} data={[{ value: '', label: t('sev_auto') }, { value: 'critical', label: t('sev_crit') }, { value: 'warning', label: t('sev_warn') }]} value={d.severity ?? ''} onChange={(v) => set({ severity: (v ?? '') as CheckConfig['severity'] })} allowDeselect={false} />
                {!info?.hub_only && <ProbePicker mode="selectors" label={t('exclude')} value={d.exclude ?? []} onChange={(v) => set({ exclude: v })} />}
              </Stack>
            </Accordion.Panel>
          </Accordion.Item>
        </Accordion>
        <Group justify="flex-end">
          <Button variant="default" onClick={guard.requestClose}>{c('cancel')}</Button>
          <Button onClick={submit} loading={busy}>{c('save')}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}
