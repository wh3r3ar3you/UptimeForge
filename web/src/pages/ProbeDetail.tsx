import {
  Alert, Anchor, Badge, Box, Breadcrumbs, Button, Card, Group, Menu, SimpleGrid, Skeleton, Stack, Table, Text, Tooltip,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import {
  IconActivity, IconArrowsDownUp, IconCpu, IconDatabase, IconDots, IconGauge, IconKey, IconPencil, IconPlugConnected, IconStack2, IconTerminal2, IconTicket, IconTrash,
} from '@tabler/icons-react';
import { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router';
import { api, enc, isChecker, queryClient, useChecks, useMeta, useOverview, useProbes, type Install, type NetworkView } from '../api';
import { Section, StatCard, Tiles } from '../components/Cards';
import { HostCharts } from '../components/HostCharts';
import { clickableRow, notifyError, notifyOk, PageHeader, QueryError } from '../components/Misc';
import { ProbeForm, TokenModal } from '../components/ProbeForm';
import { OnlineDot, VerdictBadge } from '../components/Status';
import { ago, bitrate, bytes, days, flag, ms, pct, since } from '../format';
import { useCommon, useLang, useT } from '../i18n';
import { hostPeak, hostText, reasonText } from '../reason';
import { status } from '../theme';
import { uptimeColor } from './Checks';

const L = {
  ru: {
    probes: 'Все системы', edit: 'Изменить', rotate: 'Перевыпустить токен', delete: 'Удалить', online: 'онлайн', offline: 'офлайн',
    no_net: 'нет интернета', m_agent: 'агент', m_via: 'через прокси', m_local: 'хаб', cpu: 'ЦП', mem: 'Память', disk: 'Диск', net: 'Сеть',
    load: 'Нагрузка', tcp: 'TCP-сокеты', host: 'Хост', os: 'Система', kernel: 'Ядро', cpu_model: 'Процессор', ram: 'Память', boot: 'Работает',
    agent: 'Агент', nics: 'Интерфейсы', link: 'скорость линка {0}', labels: 'Метки',
    checks: 'Проверки с этой пробы', check: 'Проверка', state: 'С этой пробы', verdict: 'Итог', latency: 'Задержка', uptime: 'Аптайм 24ч',
    no_metrics: 'Метрики хоста собирает агент. У проб через прокси их нет.', no_data: 'Нет данных за этот период',
    up: 'работает', down: 'не работает', pending: 'сбой, перепроверяем', nodata: 'нет данных', not_found: 'Проба не найдена',
    confirm: 'Удалить пробу «{0}»? Проверки с неё прекратятся.', rotate_confirm: 'Текущий агент отключится — его нужно будет запустить с новым токеном.',
    host_since: 'Выше порога уже {0}', of: 'из {0}', deleted: 'Проба удалена', new_token: 'Новый токен', via: 'Прокси', never: 'Агент ещё ни разу не подключался. Покажем команду установки с новым токеном: прежний токен перестанет работать.',
    never_viewer: 'Агент ещё ни разу не подключался.', show_install: 'Показать команду установки',
    more: 'Ещё действия', vcpu: '{0} vCPU', iowait_steal: 'iowait {0}% · steal {1}%', tcp_n: 'TCP {0}', load_failed: 'Не удалось загрузить серверы',
    network: 'Сеть', n_country: 'Страна', n_isp: 'Оператор', n_asn: 'ASN', n_ip: 'IP-адрес', n_ip_hint: 'Видят только администраторы', n_at: 'Определено {0}',
    checkers: 'Агенты проверок', m_checker: 'агент проверок', trusted: 'доверенный', untrusted: 'недоверенный',
    no_metrics_checker: 'Агент проверок не присылает метрики хоста: это чужая машина, её нагрузка нас не касается.',
    confirm_checker: 'Удалить агента проверок «{0}»? Его токен перестанет работать; чтобы вернуть точку, нужно новое приглашение.',
    checker_away: 'Точка не на связи с тех пор, как запущен хаб. Обычно она возвращается сама. Если машину переустановили или токен потерян, подключите её заново новым приглашением.',
    new_invite: 'Новое приглашение',
  },
  en: {
    probes: 'All systems', edit: 'Edit', rotate: 'Rotate token', delete: 'Delete', online: 'online', offline: 'offline',
    no_net: 'no internet', m_agent: 'agent', m_via: 'via proxy', m_local: 'hub', cpu: 'CPU', mem: 'Memory', disk: 'Disk', net: 'Network',
    load: 'Load', tcp: 'TCP sockets', host: 'Host', os: 'OS', kernel: 'Kernel', cpu_model: 'CPU', ram: 'Memory', boot: 'Up for',
    agent: 'Agent', nics: 'Interfaces', link: 'link speed {0}', labels: 'Labels',
    checks: 'Checks from this probe', check: 'Check', state: 'From this probe', verdict: 'Overall', latency: 'Latency', uptime: 'Uptime 24h',
    no_metrics: 'Host metrics come from the agent. Proxy probes have none.', no_data: 'No data for this period',
    up: 'up', down: 'down', pending: 'failed, rechecking', nodata: 'no data', not_found: 'Probe not found',
    confirm: 'Delete probe “{0}”? Checks from it will stop.', rotate_confirm: 'The running agent will be disconnected and must restart with the new token.',
    host_since: 'Above the threshold for {0}', of: 'of {0}', deleted: 'Probe deleted', new_token: 'New token', via: 'Proxy', never: 'The agent has never connected. Get the install command with a new token: the previous token stops working.',
    never_viewer: 'The agent has never connected.', show_install: 'Show install command',
    more: 'More actions', vcpu: '{0} vCPU', iowait_steal: 'iowait {0}% · steal {1}%', tcp_n: 'TCP {0}', load_failed: 'Could not load the servers',
    network: 'Network', n_country: 'Country', n_isp: 'Operator', n_asn: 'ASN', n_ip: 'IP address', n_ip_hint: 'Only admins see it', n_at: 'Looked up {0}',
    checkers: 'Checker agents', m_checker: 'checker agent', trusted: 'trusted', untrusted: 'untrusted',
    no_metrics_checker: 'A checker agent sends no host metrics: it is someone else’s machine and its load is none of our business.',
    confirm_checker: 'Delete checker agent “{0}”? Its token stops working; bringing the point back takes a new invite.',
    checker_away: 'The point has not connected since the hub started. It usually comes back by itself. If the machine was reinstalled or the token is lost, connect it again with a new invite.',
    new_invite: 'New invite',
  },
};

function Info({ label, value }: { label: string; value?: React.ReactNode }) {
  if (!value) return null;
  return (
    <Stack gap={2}>
      <Text size="xs" c="dimmed" fw={600} tt="uppercase" style={{ letterSpacing: '0.06em' }}>{label}</Text>
      <Text size="sm" fw={500} component="div">{value}</Text>
    </Stack>
  );
}

/** NetworkCard is where an agent connects from. The address is its owner's home: admins only (the hub strips it for viewers). */
function NetworkCard({ n, admin }: { n: NetworkView; admin: boolean }) {
  const t = useT(L);
  const { lang } = useLang();
  return (
    <Section title={t('network')}>
      <Card>
        <SimpleGrid cols={{ base: 2, sm: 4 }} spacing="lg">
          <Info label={t('n_country')} value={n.country ? `${flag(n.country)} ${n.country.toUpperCase()}` : '—'} />
          <Info label={t('n_isp')} value={n.org || n.isp || '—'} />
          <Info label={t('n_asn')} value={n.asn ? <Text span ff="monospace" size="sm">{n.asn.toUpperCase()}</Text> : '—'} />
          {admin && <Info label={t('n_ip')} value={n.ip ? <Tooltip label={t('n_ip_hint')}><Text span ff="monospace" size="sm">{n.ip}</Text></Tooltip> : '—'} />}
        </SimpleGrid>
        {n.at && <Text size="xs" c="dimmed" mt="sm">{t('n_at', ago(n.at, lang))}</Text>}
      </Card>
    </Section>
  );
}

export default function ProbeDetail() {
  const { id = '' } = useParams();
  const t = useT(L);
  const c = useCommon();
  const { lang } = useLang();
  const nav = useNavigate();
  const meta = useMeta();
  const probes = useProbes();
  const checks = useChecks();
  const ov = useOverview();
  const [editing, setEditing] = useState(false);
  const [install, setInstall] = useState<Install | null>(null);
  const [issuing, setIssuing] = useState(false);

  const p = probes.data?.probes.find((x) => x.id === id);

  if (probes.isLoading) return <Stack><Skeleton h={60} /><Skeleton h={300} /></Stack>;
  if (!probes.data && probes.isError) return <Stack><PageHeader title={t('load_failed')} /><QueryError error={probes.error} onRetry={() => probes.refetch()} /></Stack>;
  if (!p) return <PageHeader title={t('not_found')} />;
  const isAdmin = meta.data?.user.role === 'admin';
  const m = p.metrics && p.metrics_at && Date.now() - Date.parse(p.metrics_at) < 120_000 ? p.metrics : undefined;
  const si = p.sysinfo;
  const rows = (checks.data?.checks ?? []).filter((r) => r.probes.some((x) => x.probe === id));
  const checker = isChecker(p);
  const home = checker ? '/checkers' : '/';

  const remove = () => modals.openConfirmModal({
    title: t('delete'), children: <Text size="sm">{t(checker ? 'confirm_checker' : 'confirm', p.name)}</Text>, labels: { confirm: t('delete'), cancel: c('cancel') },
    confirmProps: { color: 'red' },
    onConfirm: async () => {
      try {
        await api('DELETE', `/api/v1/probes/${enc(id)}`);
        await queryClient.invalidateQueries();
        notifyOk(t('deleted'));
        nav(home);
      } catch (e) {
        notifyError(e);
      }
    },
  });
  const rotate = () => modals.openConfirmModal({
    title: t('rotate'), children: <Text size="sm">{t('rotate_confirm')}</Text>, labels: { confirm: t('rotate'), cancel: c('cancel') },
    onConfirm: async () => {
      try {
        const res = await api<{ install: Install }>('POST', `/api/v1/probes/${enc(id)}/token`);
        setInstall(res.install);
      } catch (e) {
        notifyError(e);
      }
    },
  });

  // The hub keeps only a hash of the token, so the command needs a new one.
  // The agent never connected, so nothing running loses its token.
  const showInstall = async () => {
    setIssuing(true);
    try {
      const res = await api<{ install: Install }>('POST', `/api/v1/probes/${enc(id)}/token`);
      setInstall(res.install);
    } catch (e) {
      notifyError(e);
    } finally {
      setIssuing(false);
    }
  };

  const stateText =!p.online ? t('offline') : !p.internet ? t('no_net') : t('online');
  return (
    <Stack gap="xl">
      <Box>
        <Breadcrumbs mb="xs" separator="›">
          <Anchor component={Link} to={home} size="sm">{t(checker ? 'checkers' : 'probes')}</Anchor>
          <Text size="sm" c="dimmed">{p.name}</Text>
        </Breadcrumbs>
        <PageHeader
          title={`${flag(p.region || p.network?.country) || '📍'} ${p.name}`}
          subtitle={
            <Group gap="xs" component="span">
              <OnlineDot online={p.online} internet={p.internet} />
              <Text span size="sm">{stateText}</Text>
              <Badge variant="default" radius="sm">{checker ? t('m_checker') : t(`m_${p.mode}`)}</Badge>
              {checker && <Badge variant="light" color={p.config.trusted ? 'forge' : 'gray'} radius="sm">{t(p.config.trusted ? 'trusted' : 'untrusted')}</Badge>}
              {p.host && <Text span size="sm" c="dimmed" ff="monospace">{p.host}</Text>}
              {p.managed && <Badge variant="outline" color="gray">{c('managed')}</Badge>}
            </Group>
          }
          right={isAdmin && !p.managed ? (
            <>
              <Button variant="default" leftSection={<IconPencil size={16} />} onClick={() => setEditing(true)}>{t('edit')}</Button>
              <Menu position="bottom-end">
                <Menu.Target><Button variant="default" px="xs" aria-label={t('more')}><IconDots size={18} /></Button></Menu.Target>
                <Menu.Dropdown>
                  {/* A checker's token lives on a volunteer's machine: a new one would cut it off. It rejoins by invite. */}
                  {p.mode === 'agent' && !checker && <Menu.Item leftSection={<IconKey size={16} />} onClick={rotate}>{t('rotate')}</Menu.Item>}
                  <Menu.Item color="red" leftSection={<IconTrash size={16} />} onClick={remove}>{t('delete')}</Menu.Item>
                </Menu.Dropdown>
              </Menu>
            </>
          ) : undefined}
        />
      </Box>

      {checker && !p.online && !p.version && (
        <Alert color="gray" icon={<IconPlugConnected size={16} />}>
          <Stack gap="xs" align="flex-start">
            <Text size="sm">{t('checker_away')}</Text>
            {isAdmin && (
              <Button size="xs" variant="white" color="dark" leftSection={<IconTicket size={14} />} component={Link} to="/checkers?invite=1">{t('new_invite')}</Button>
            )}
          </Stack>
        </Alert>
      )}
      {p.mode === 'agent' && !checker && !p.version && (
        <Alert color="yellow" icon={<IconPlugConnected size={16} />}>
          <Stack gap="xs" align="flex-start">
            <Text size="sm">{isAdmin && !p.managed ? t('never') : t('never_viewer')}</Text>
            {isAdmin && !p.managed && (
              <Button size="xs" variant="white" color="dark" leftSection={<IconTerminal2 size={14} />} loading={issuing} onClick={showInstall}>{t('show_install')}</Button>
            )}
          </Stack>
        </Alert>
      )}
      {(ov.data?.incidents ?? []).filter((i) => i.key.startsWith(`host:${id}:`) && i.host).map((i) => (
        <Alert key={i.id} color="orange" variant="light" icon={<IconGauge size={18} />}
          title={<Anchor component={Link} to="/incidents" c="inherit" fw={650}>{hostText(i.host!, lang)}</Anchor>}>
          <Text size="sm">{t('host_since', since(i.started, lang))}{i.host!.peak > i.host!.value ? ` · ${hostPeak(i.host!, lang)}` : ''}</Text>
        </Alert>
      ))}

      {m && (
        <Tiles cols={5}>
          <StatCard icon={IconCpu} color="blue" label={t('cpu')} value={`${m.cpu.toFixed(1)}%`}
            sub={[si?.cores ? t('vcpu', si.cores) : '', m.ext ? t('iowait_steal', m.ext.cpu_iowait.toFixed(1), m.ext.cpu_steal.toFixed(1)) : ''].filter(Boolean).join(' · ') || undefined} />
          <StatCard icon={IconStack2} color="forge" label={t('mem')} value={bytes(m.mem_used)} sub={`/ ${bytes(m.mem_total)} · ${pct(m.mem_used / m.mem_total, 0)}`} />
          <StatCard icon={IconDatabase} color="violet" label={t('disk')} value={bytes(m.disk_used, 1)} sub={`/ ${bytes(m.disk_total, 0)} · ${pct(m.disk_used / m.disk_total, 0)}`} />
          <StatCard icon={IconArrowsDownUp} color="orange" label={t('net')} value={bitrate(m.rx + m.tx)} sub={`↓ ${bitrate(m.rx)} · ↑ ${bitrate(m.tx)}`} />
          <StatCard icon={IconActivity} color="green" label={t('load')} value={m.load1.toFixed(2)} sub={`${m.load5.toFixed(2)} · ${m.load15.toFixed(2)}${m.tcp ? ` · ${t('tcp_n', m.tcp)}` : ''}`} />
        </Tiles>
      )}

      <Card>
        <SimpleGrid cols={{ base: 2, sm: 3, lg: 4 }} spacing="lg">
          <Info label={t('os')} value={si?.platform || si?.os} />
          <Info label={t('kernel')} value={si?.kernel} />
          <Info label={t('cpu_model')} value={si?.cpu_model ? `${si.cpu_model}${si.cores ? ` × ${si.cores}` : ''}` : undefined} />
          <Info label={t('ram')} value={si?.mem_total ? bytes(si.mem_total) : undefined} />
          <Info label={t('boot')} value={m ? days(m.uptime, lang) : undefined} />
          <Info label={t('agent')} value={p.version ? [p.version, si?.arch].filter(Boolean).join(' · ') : undefined} />
          <Info label={t('nics')} value={si?.nics?.length ? `${si.nics.join(', ')}${si.link_mbps ? ` · ${t('link', `${si.link_mbps >= 1000 ? `${si.link_mbps / 1000} Gbps` : `${si.link_mbps} Mbps`}`)}` : ''}` : undefined} />
          <Info label={t('via')} value={p.config.via ? <Text span ff="monospace" size="xs">{p.config.via.replace(/\/\/[^@]*@/, '//•••@')}</Text> : undefined} />
          <Info label={t('labels')} value={
            <Group gap={4}>{Object.entries(p.labels ?? {}).filter(([k]) => k !== 'id').map(([k, v]) => <Badge key={k} variant="default" size="sm" radius="sm" tt="none">{k}={v}</Badge>)}</Group>
          } />
        </SimpleGrid>
      </Card>

      {p.network && <NetworkCard n={p.network} admin={isAdmin} />}

      {p.mode !== 'via' && !checker && <HostCharts id={id} cores={si?.cores} online={p.online} />}
      {p.mode === 'via' && <Text c="dimmed" size="sm">{t('no_metrics')}</Text>}
      {checker && <Text c="dimmed" size="sm">{t('no_metrics_checker')}</Text>}

      <Section title={t('checks')}>
        <Card padding={0}>
          <Box className="uf-scroll-x">
            <Table className="uf-table" verticalSpacing="sm" miw={680}>
              <Table.Thead>
                <Table.Tr><Table.Th>{t('check')}</Table.Th><Table.Th>{t('state')}</Table.Th><Table.Th>{t('verdict')}</Table.Th><Table.Th ta="right">{t('latency')}</Table.Th><Table.Th ta="right">{t('uptime')}</Table.Th></Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {rows.map((r) => {
                  const cell = r.probes.find((x) => x.probe === id)!;
                  const col = !cell.status ? status.unknown : cell.failing ? status.down : cell.status === 'down' ? status.partial : status.ok;
                  const st = !cell.status ? t('nodata') : cell.failing ? t('down') : cell.status === 'down' ? t('pending') : t('up');
                  return (
                    <Table.Tr key={r.id} {...clickableRow(() => nav(`/checks/${enc(r.id)}`))}>
                      <Table.Td><Text size="sm" fw={600}>{r.name}</Text><Text size="xs" c="dimmed" ff="monospace">{r.target}</Text></Table.Td>
                      <Table.Td>
                        <Group gap={6} wrap="nowrap">
                          <Box w={8} h={8} style={{ borderRadius: 8, background: col }} />
                          <Text size="sm">{st}</Text>
                          {cell.status === 'down' && <Text size="xs" c="dimmed" truncate maw={220}>{reasonText(cell.reason, cell.error, lang)}</Text>}
                        </Group>
                        <Text size="xs" c="dimmed">{ago(cell.at, lang)}</Text>
                      </Table.Td>
                      <Table.Td><VerdictBadge kind={r.verdict} region={r.region} size="sm" /></Table.Td>
                      <Table.Td ta="right"><Text size="sm" className="uf-num">{cell.status === 'up' ? ms(cell.latency) : '—'}</Text></Table.Td>
                      <Table.Td ta="right"><Text size="sm" className="uf-num" fw={600} c={uptimeColor(cell.uptime_24h)}>{pct(cell.uptime_24h)}</Text></Table.Td>
                    </Table.Tr>
                  );
                })}
              </Table.Tbody>
            </Table>
          </Box>
        </Card>
      </Section>

      {editing && <ProbeForm opened={editing} onClose={() => setEditing(false)} initial={p.config} network={p.network} hubURL={probes.data?.hub_url ?? ''} />}
      <TokenModal install={install} hubURL={probes.data?.hub_url ?? ''} title={t('new_token')} onClose={() => setInstall(null)} />
    </Stack>
  );
}
