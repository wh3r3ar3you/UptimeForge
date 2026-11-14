import { Alert, Autocomplete, Button, Group, Modal, NumberInput, SegmentedControl, Select, Stack, Switch, Tabs, Text, TextInput, Textarea } from '@mantine/core';
import { IconAlertCircle, IconAlertTriangle, IconBrandDocker, IconBinary, IconTerminal2 } from '@tabler/icons-react';
import { useEffect, useRef, useState } from 'react';
import { api, ApiError, queryClient, useRemnawave, type Install, type NetworkView, type ProbeConfig } from '../api';
import { countryMatches, countryOptions } from '../format';
import { useCommon, useLang, useT } from '../i18n';
import { CopyBlock, notifyOk } from './Misc';

const L = {
  ru: {
    create: 'Новый сервер', edit: 'Настройки сервера', mode: 'Как проба подключается',
    m_agent: 'Агент на сервере', m_via: 'Через прокси', m_local: 'Сам хаб',
    m_agent_hint: 'Ставите uptimeforge-agent на сервер, роутер или домашний ПК — он сам подключается к хабу, открытых портов не нужно.',
    m_via_hint: 'Хаб выполняет проверки через указанный прокси: так можно смотреть на мир из сети провайдера (резидентный/мобильный прокси) без установки агента.',
    m_local_hint: 'Проверки выполняются из сети, где стоит хаб. Удобно как контрольная точка за пределами РФ.',
    name: 'Название', name_ph: 'Дом · Москва', id: 'ID', country: 'Страна', country_hint: 'По стране определяется «блок в регионе»',
    city: 'Город', isp: 'Провайдер', kind: 'Тип сети', k_home: 'домашняя', k_dc: 'датацентр', k_mobile: 'мобильная', k_proxy: 'прокси',
    via: 'Ссылка на прокси', via_ph: 'socks5://user:pass@1.2.3.4:1080 или vless://…', concurrency: 'Параллельных проверок',
    install: 'Установка агента', once: 'Токен показан один раз. Сохраните команду — потом токен можно только перевыпустить.',
    plain_http: 'Хаб открыт по http: токен агента и все проверки (с VPN-ссылками) пойдут открытым текстом. Включите HTTPS и задайте UF_PUBLIC_URL с https://.',
    rw_node: 'Нода Remnawave', rw_node_hint: 'Укажите, если хаб не нашёл ноду сам. Пусто — по имени, адресу ноды или IP, с которого подключается агент.', rw_node_gone: 'нода удалена из панели',
    probing: 'Запускать проверки с этого сервера', probing_hint: 'Выключено: агент только присылает метрики сервера (ЦП, память, диск, сеть). Включите, если сервер должен ещё и проверять ваши сервисы из своей сети.', nothing: 'Не найдено',
    compose: 'Docker Compose', compose_oneline: 'Одной командой', compose_hint: 'Вставьте на сервере: создаст /opt/uptimeforge-agent/compose.yml и запустит агента. Нужны Docker и compose.', compose_file: 'Или только compose.yml',
    private_hub: 'Хаб указан как {0}: это внутренний адрес, агенты из других сетей до него не достанут. Задайте публичный адрес в настройках → Public URL и создайте токен заново.',
    script: 'Скрипт', docker: 'Docker', binary: 'Вручную', script_hint: 'Linux с systemd, а также Keenetic (Entware) и OpenWrt.',
    docker_hint: 'Сеть хоста обязательна — иначе проба смотрит на мир из сети Docker.', binary_hint: 'Бинарники для всех платформ: {0}/download/…',
    saved_it: 'Я сохранил', created: 'Проба создана', saved: 'Сохранено',
    geo_busy: 'Определяю страну по адресу прокси…', geo_found: 'Определено по {0} · {1}', geo_unknown: 'Страну {0} определить не удалось', geo_err: 'Страна не определилась: {0}',
    role: 'Чья это машина', r_server: 'Наша инфраструктура', r_checker: 'Агент проверок',
    r_checker_hint: 'Чужая машина (доброволец): получает только проверки, выбранные явно, метрики хоста не шлёт, её офлайн не тревога.',
    trusted: 'Доверенный', trusted_hint: 'Получит проверки с секретами: VPN-ссылки, подписки, пароли. Включайте только для машин, владельцу которых доверяете.',
    asn: 'ASN', from_net: 'Определено по сети агента. Измените — и значение закрепится; очистите — снова будет браться из сети.', locked: 'Задано вручную. Очистите поле, чтобы снова брать из сети.',
  },
  en: {
    create: 'New server', edit: 'Server settings', mode: 'How the probe connects',
    m_agent: 'Agent on a server', m_via: 'Through a proxy', m_local: 'The hub itself',
    m_agent_hint: 'Install uptimeforge-agent on a server, router or home PC — it connects to the hub by itself, no open ports needed.',
    m_via_hint: 'The hub runs checks through this proxy: see the world from an ISP’s network (residential/mobile proxy) without installing anything.',
    m_local_hint: 'Checks run from the hub’s own network. A good control point outside the filtered region.',
    name: 'Name', name_ph: 'Home · Berlin', id: 'ID', country: 'Country', country_hint: '“Blocked in a region” is decided by country',
    city: 'City', isp: 'ISP', kind: 'Network', k_home: 'home', k_dc: 'datacenter', k_mobile: 'mobile', k_proxy: 'proxy',
    via: 'Proxy link', via_ph: 'socks5://user:pass@1.2.3.4:1080 or vless://…', concurrency: 'Parallel checks',
    install: 'Install the agent', once: 'The token is shown once. Keep the command — later you can only rotate the token.',
    plain_http: 'The hub is served over http: the agent token and every check (with VPN links) travel in clear text. Enable HTTPS and set UF_PUBLIC_URL to https://.',
    rw_node: 'Remnawave node', rw_node_hint: 'Pick it when the hub did not find the node itself. Empty: matched by name, by the node address or by the IP the agent connects from.', rw_node_gone: 'node deleted from the panel',
    probing: 'Run checks from this server', probing_hint: 'Off: the agent only reports server metrics (CPU, memory, disk, network). Turn it on if this server should also test your services from its network.', nothing: 'Nothing found',
    compose: 'Docker Compose', compose_oneline: 'One command', compose_hint: 'Paste it on the server: it writes /opt/uptimeforge-agent/compose.yml and starts the agent. Needs Docker with compose.', compose_file: 'Or just compose.yml',
    private_hub: 'The hub address is {0}: a private address that agents in other networks cannot reach. Set a public address in Settings → Public URL and create the token again.',
    script: 'Script', docker: 'Docker', binary: 'Manual', script_hint: 'Linux with systemd, plus Keenetic (Entware) and OpenWrt.',
    docker_hint: 'Host networking is required — otherwise the probe sees the world from Docker’s network.', binary_hint: 'Binaries for every platform: {0}/download/…',
    saved_it: 'I saved it', created: 'Probe created', saved: 'Saved',
    geo_busy: 'Finding the country of the proxy…', geo_found: 'Found from {0} · {1}', geo_unknown: 'No country known for {0}', geo_err: 'Country not found: {0}',
    role: 'Whose machine', r_server: 'Our infrastructure', r_checker: 'Checker agent',
    r_checker_hint: 'Someone else’s machine (a volunteer): it runs only the checks aimed at it, sends no host metrics, and going offline is not an alert.',
    trusted: 'Trusted', trusted_hint: 'Gets checks with secrets: VPN links, subscriptions, passwords. Only for machines whose owner you trust.',
    asn: 'ASN', from_net: 'Found from the agent’s network. Change it and the value sticks; clear it and it follows the network again.', locked: 'Set by hand. Clear the field to follow the network again.',
  },
};


function slugify(s: string) {
  const map: Record<string, string> = { а: 'a', б: 'b', в: 'v', г: 'g', д: 'd', е: 'e', ё: 'e', ж: 'zh', з: 'z', и: 'i', й: 'y', к: 'k', л: 'l', м: 'm', н: 'n', о: 'o', п: 'p', р: 'r', с: 's', т: 't', у: 'u', ф: 'f', х: 'h', ц: 'c', ч: 'ch', ш: 'sh', щ: 'sch', ы: 'y', э: 'e', ю: 'yu', я: 'ya' };
  return s.toLowerCase().split('').map((c) => map[c] ?? c).join('').replace(/[^a-z0-9_-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 60);
}

// A hub on a private address can't be reached by agents in other networks.
export function privateHub(url: string) {
  try {
    return /^(localhost|127\.|10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|\[?::1\]?$)|\.local$/.test(new URL(url).hostname);
  } catch {
    return false;
  }
}

export function InstallPanel({ install, hubURL }: { install: Install; hubURL: string }) {
  const t = useT(L);
  return (
    <Stack gap="sm">
      <Alert color="yellow" icon={<IconAlertTriangle size={16} />}>{t('once')}</Alert>
      {privateHub(hubURL) && <Alert color="red" icon={<IconAlertCircle size={16} />}>{t('private_hub', hubURL)}</Alert>}
      {install.warning && <Alert color="red" icon={<IconAlertCircle size={16} />}>{t('plain_http')}</Alert>}
      <Tabs defaultValue="compose" variant="pills" radius="md">
        <Tabs.List mb="sm">
          <Tabs.Tab value="compose" leftSection={<IconBrandDocker size={15} />}>{t('compose')}</Tabs.Tab>
          <Tabs.Tab value="script" leftSection={<IconTerminal2 size={15} />}>{t('script')}</Tabs.Tab>
          <Tabs.Tab value="docker" leftSection={<IconBrandDocker size={15} />}>{t('docker')}</Tabs.Tab>
          <Tabs.Tab value="binary" leftSection={<IconBinary size={15} />}>{t('binary')}</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="compose">
          <Stack gap={6}>
            <Text size="sm" fw={600}>{t('compose_oneline')}</Text>
            <CopyBlock value={install.compose_cmd} />
            <Text size="xs" c="dimmed">{t('compose_hint')}</Text>
            <Text size="sm" fw={600} mt="xs">{t('compose_file')}</Text>
            <CopyBlock value={install.compose} />
          </Stack>
        </Tabs.Panel>
        <Tabs.Panel value="script"><Stack gap={6}><CopyBlock value={install.script} /><Text size="xs" c="dimmed">{t('script_hint')}</Text></Stack></Tabs.Panel>
        <Tabs.Panel value="docker"><Stack gap={6}><CopyBlock value={install.docker} /><Text size="xs" c="dimmed">{t('docker_hint')}</Text></Stack></Tabs.Panel>
        <Tabs.Panel value="binary"><Stack gap={6}><CopyBlock value={install.binary} /><Text size="xs" c="dimmed">{t('binary_hint', hubURL)}</Text></Stack></Tabs.Panel>
      </Tabs>
    </Stack>
  );
}

/**
 * TokenModal shows a freshly issued agent token. The hub stores only its
 * hash, so a stray click or Esc would lose it: the modal closes only through
 * its own "I saved it" button.
 */
export function TokenModal({ install, hubURL, title, onClose }: { install: Install | null; hubURL: string; title: string; onClose: () => void }) {
  const t = useT(L);
  return (
    <Modal opened={!!install} onClose={onClose} size="lg" title={<Text fw={700}>{title}</Text>}
      closeOnClickOutside={false} closeOnEscape={false} withCloseButton={false}>
      {install && (
        <Stack>
          <InstallPanel install={install} hubURL={hubURL} />
          <Group justify="flex-end"><Button onClick={onClose}>{t('saved_it')}</Button></Group>
        </Stack>
      )}
    </Modal>
  );
}

/** The checker labels the hub sets from the agent's network, unless set by hand (labels_locked). */
export const NETWORK_LABELS = ['country', 'asn', 'isp'] as const;

/** networkLabels are the values the hub would give a checker from its network. */
export function networkLabels(n?: NetworkView): Record<string, string> {
  return { country: n?.country?.toLowerCase() ?? '', asn: n?.asn ?? '', isp: n?.isp ?? '' };
}

/**
 * probeBody is a probe config the hub accepts: trusted only on a checker, a
 * role only on an agent. For a checker, a network label the user changed
 * from what the form showed is locked (the lookup leaves it alone), a cleared
 * one is unlocked and follows the network again, and an untouched one that
 * is not locked is left to the network.
 */
export function probeBody(next: ProbeConfig, shown: Record<string, string>): ProbeConfig {
  const body: ProbeConfig = { ...next, labels: Object.fromEntries(Object.entries(next.labels ?? {}).filter(([k, v]) => v && k !== 'id' && k !== 'role')) };
  if (body.via || body.local) delete body.role;
  if (body.role !== 'checker') {
    delete body.trusted;
    delete body.labels_locked;
    return body;
  }
  const locked = new Set(next.labels_locked ?? []);
  for (const k of NETWORK_LABELS) {
    const v = body.labels![k] ?? '';
    if (!v) locked.delete(k);
    else if (v !== (shown[k] ?? '')) locked.add(k);
    if (!locked.has(k)) delete body.labels![k];
  }
  body.labels_locked = [...locked];
  return body;
}

export function ProbeForm({ initial, network, opened, onClose, hubURL }: {
  initial?: ProbeConfig; opened: boolean; onClose: () => void; hubURL: string;
  /** Where the agent connects from: a checker's country, ASN and operator default to it. */
  network?: NetworkView;
}) {
  const t = useT(L);
  const c = useCommon();
  const editing = !!initial;
  const { lang } = useLang();
  // A checker shows its network labels where none was set by hand: the stored config does not keep them.
  // On a probe that is not a checker yet, every label was set by hand: shown stays empty, so they get locked.
  const [shown] = useState<Record<string, string>>(() => {
    if (initial?.role !== 'checker') return {};
    const net = networkLabels(network);
    return Object.fromEntries(NETWORK_LABELS.map((k) => [k, initial?.labels_locked?.includes(k) ? initial.labels?.[k] ?? '' : net[k] || initial?.labels?.[k] || '']));
  });
  const [p, setP] = useState<ProbeConfig>(() => {
    if (!initial) return { id: '', name: '', labels: {} };
    const x = structuredClone(initial);
    if (x.role === 'checker') x.labels = { ...x.labels, ...Object.fromEntries(Object.entries(shown).filter(([, v]) => v)) };
    return x;
  });
  const checker = p.role === 'checker';
  const netHint = (k: string) => {
    const v = p.labels?.[k] ?? '';
    if (!checker || !v) return undefined;
    return p.labels_locked?.includes(k) || v !== shown[k] ? t('locked') : t('from_net');
  };
  const rwq = useRemnawave();
  const [mode, setMode] = useState<'agent' | 'via' | 'local'>(initial?.via ? 'via' : initial?.local ? 'local' : 'agent');
  const [idTouched, setIdTouched] = useState(editing);
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);
  const [install, setInstall] = useState<Install | null>(null);

  const label = (k: string, v: string) => setP((x) => ({ ...x, labels: { ...(x.labels ?? {}), [k]: v } }));

  // A proxy's country is looked up from its link, unless the user picked one.
  const picked = useRef(editing && !!initial?.labels?.country);
  const [geo, setGeo] = useState<{ busy?: boolean; ip?: string; source?: string; country?: string; err?: string } | null>(null);
  useEffect(() => {
    if (mode !== 'via' || !p.via || picked.current) return;
    const link = p.via;
    const timer = setTimeout(async () => {
      setGeo({ busy: true });
      try {
        const r = await api<{ ip: string; country: string; source: string }>('POST', '/api/v1/geoip/proxy', { link });
        setGeo({ ip: r.ip, source: r.source, country: r.country });
        if (r.country && !picked.current) label('country', r.country);
      } catch (e) {
        setGeo({ err: e instanceof ApiError ? e.message : String(e) });
      }
    }, 600);
    return () => clearTimeout(timer);
  }, [p.via, mode]);

  const submit = async () => {
    setErr('');
    setBusy(true);
    const body = probeBody({ ...p, via: mode === 'via' ? p.via : undefined, local: mode === 'local', probing: mode === 'agent' && !checker && !!p.probing }, shown);
    try {
      const res = editing
        ? await api<{ install?: Install }>('PUT', `/api/v1/probes/${encodeURIComponent(p.id)}`, body)
        : await api<{ install?: Install }>('POST', '/api/v1/probes', body);
      await queryClient.invalidateQueries();
      notifyOk(editing ? t('saved') : t('created'));
      if (res.install) setInstall(res.install);
      else onClose();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    // Once the token is on screen, only "I saved it" closes the modal (see TokenModal).
    <Modal opened={opened} onClose={onClose} size="lg" title={<Text fw={700} fz="lg">{install ? t('install') : editing ? t('edit') : t('create')}</Text>}
      closeOnClickOutside={!install} closeOnEscape={!install} withCloseButton={!install}>
      {install ? (
        <Stack>
          <InstallPanel install={install} hubURL={hubURL} />
          <Group justify="flex-end"><Button onClick={onClose}>{t('saved_it')}</Button></Group>
        </Stack>
      ) : (
        <Stack gap="md">
          {err && <Alert color="red" icon={<IconAlertCircle size={16} />}>{err}</Alert>}
          <Stack gap={6}>
            <Text size="sm" fw={500}>{t('mode')}</Text>
            <SegmentedControl value={mode} onChange={(v) => setMode(v as typeof mode)} fullWidth
              data={[{ value: 'agent', label: t('m_agent') }, { value: 'via', label: t('m_via') }, { value: 'local', label: t('m_local') }]} />
            <Text size="xs" c="dimmed">{t(`m_${mode}_hint`)}</Text>
          </Stack>
          <Group grow align="flex-start">
            <TextInput label={t('name')} placeholder={t('name_ph')} value={p.name} required
              onChange={(e) => { const name = e.currentTarget.value; setP((x) => ({ ...x, name, id: idTouched ? x.id : slugify(name) })); }} />
            <TextInput label={t('id')} value={p.id} disabled={editing} required onChange={(e) => { const id = e.currentTarget.value; setIdTouched(true); setP((x) => ({ ...x, id })); }} />
          </Group>
          {mode === 'agent' && (
            <Stack gap={6}>
              <Text size="sm" fw={500}>{t('role')}</Text>
              <SegmentedControl value={checker ? 'checker' : 'server'} fullWidth
                data={[{ value: 'server', label: t('r_server') }, { value: 'checker', label: t('r_checker') }]}
                // Trusted belongs to a checker: the hub refuses it on anything else.
                onChange={(v) => setP((x) => (v === 'checker' ? { ...x, role: 'checker', remnawave_node: undefined } : { ...x, role: undefined, trusted: undefined }))} />
              {checker && <Text size="xs" c="dimmed">{t('r_checker_hint')}</Text>}
            </Stack>
          )}
          {!checker && rwq.data?.configured && (
            <Select label={t('rw_node')} description={t('rw_node_hint')} searchable clearable nothingFoundMessage={t('nothing')}
              data={[
                ...(rwq.data.nodes ?? []).map((n) => ({ value: n.uuid, label: n.address ? `${n.name} · ${n.address}` : n.name })),
                ...(p.remnawave_node && !rwq.data.nodes?.some((n) => n.uuid === p.remnawave_node) ? [{ value: p.remnawave_node, label: t('rw_node_gone') }] : []),
              ]}
              value={p.remnawave_node ?? null} onChange={(v) => setP((x) => ({ ...x, remnawave_node: v || undefined }))} />
          )}
          {mode === 'agent' && !checker && (
            <Switch checked={!!p.probing} onChange={(e) => { const probing = e.currentTarget.checked; setP((x) => ({ ...x, probing })); }}
              label={t('probing')} description={t('probing_hint')} />
          )}
          {mode === 'agent' && checker && (
            <Switch checked={!!p.trusted} color="orange" onChange={(e) => { const trusted = e.currentTarget.checked; setP((x) => ({ ...x, trusted })); }}
              label={t('trusted')} description={t('trusted_hint')} />
          )}
          {mode === 'via' && (
            <Textarea label={t('via')} placeholder={t('via_ph')} autosize minRows={1} styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)', fontSize: 12 } }}
              value={p.via ?? ''} onChange={(e) => { const via = e.currentTarget.value.trim(); setP((x) => ({ ...x, via })); }} required />
          )}
          <Group grow align="flex-start">
            <Select label={t('country')} description={netHint('country') ?? t('country_hint')} searchable
              data={countryOptions(lang)} clearable nothingFoundMessage={t('nothing')}
              filter={({ options, search }) => (options as { value: string }[]).filter((o) => countryMatches(o.value, search)) as typeof options}
              value={p.labels?.country ?? null} onChange={(v) => { picked.current = !!v; label('country', v ?? ''); }}
              inputWrapperOrder={['label', 'description', 'input', 'error']}
              error={mode === 'via' && geo?.err ? t('geo_err', geo.err) : undefined} />
            <Select label={t('kind')} data={(['home', 'dc', 'mobile', 'proxy'] as const).map((k) => ({ value: k, label: t(`k_${k}`) }))}
              value={p.labels?.kind ?? null} onChange={(v) => label('kind', v ?? '')} clearable />
          </Group>
          {mode === 'via' && geo && !geo.err && (
            <Text size="xs" c="dimmed" mt={-8}>
              {geo.busy ? t('geo_busy') : geo.country ? t('geo_found', geo.ip ?? '', geo.source ?? '') : t('geo_unknown', geo.ip ?? '')}
            </Text>
          )}
          <Group grow align="flex-start">
            <TextInput label={t('city')} value={p.labels?.city ?? ''} onChange={(e) => label('city', e.currentTarget.value)} />
            <Autocomplete label={t('isp')} description={netHint('isp')} data={['rostelecom', 'mts', 'beeline', 'megafon', 't2', 'dom.ru', 'selectel', 'timeweb', 'vk-cloud', 'reg.ru', 'hetzner']}
              value={p.labels?.isp ?? ''} onChange={(v) => label('isp', v)} />
            {checker && <TextInput label={t('asn')} description={netHint('asn')} placeholder="as8359" value={p.labels?.asn ?? ''} onChange={(e) => label('asn', e.currentTarget.value.trim().toLowerCase())} />}
            <NumberInput label={t('concurrency')} min={1} max={128} placeholder="16" value={p.concurrency ?? ''} onChange={(v) => setP((x) => ({ ...x, concurrency: Number(v) || undefined }))} />
          </Group>
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{c('cancel')}</Button>
            <Button onClick={submit} loading={busy}>{c('save')}</Button>
          </Group>
        </Stack>
      )}
    </Modal>
  );
}
