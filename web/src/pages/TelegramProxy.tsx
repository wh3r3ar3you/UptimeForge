import {
  ActionIcon, Alert, Anchor, Box, Button, Card, Checkbox, Group, Modal, SimpleGrid, Skeleton, Stack, Table, Text, TextInput, Tooltip,
} from '@mantine/core';
import { IconAlertCircle, IconPencil, IconPlayerPause, IconPlayerPlay, IconPlus } from '@tabler/icons-react';
import { useState } from 'react';
import { Link, useNavigate } from 'react-router';
import { api, ApiError, enc, isChecker, queryClient, useChecks, useMeta, useProbes, type CheckConfig, type CheckRow } from '../api';
import { useGuardedClose } from '../components/guards';
import { clickableRow, PageHeader, notifyError, notifyOk, QueryError } from '../components/Misc';
import { VerdictBadge } from '../components/Status';
import { TelegramLogo } from '../components/TelegramLogo';
import { flag, ms, pct } from '../format';
import { useCommon, useT } from '../i18n';
import { ProbeDots, uptimeColor } from './Checks';

const L = {
  ru: {
    title: 'Telegram Proxy', subtitle: 'MTProxy и telemt: проверка обмена с Telegram через прокси', add: 'Добавить прокси', edit: 'Настроить',
    pause: 'Пауза', resume: 'Возобновить', state: 'Состояние', proxy: 'Прокси', nodes: 'Ноды', dcs: 'DC', latency: 'Задержка', uptime: 'Аптайм 24ч',
    no_nodes: 'ноды не выбраны', dc_ok: 'Ответили DC: {0} из {1}',
    empty: 'Прокси пока нет', empty_hint: 'Вставьте ссылку tg://proxy или t.me/proxy и выберите ноды, с которых её проверять',
    f_add: 'Новый прокси', f_edit: 'Настройка прокси', link: 'Ссылка на прокси', name: 'Название', pick: 'С каких нод проверять',
    pick_hint: 'На выбранных нодах запустится только эта проверка, остальные проверки на них не появятся', pick_none: 'Выберите хотя бы одну ноду',
    bad_link: 'Нужна ссылка tg://proxy?… или https://t.me/proxy?…', hub: 'хаб', offline: 'офлайн', saved: 'Сохранено', created: 'Прокси добавлен',
  },
  en: {
    title: 'Telegram Proxy', subtitle: 'MTProxy and telemt: a real exchange with Telegram through the proxy', add: 'Add proxy', edit: 'Configure',
    pause: 'Pause', resume: 'Resume', state: 'State', proxy: 'Proxy', nodes: 'Nodes', dcs: 'DCs', latency: 'Latency', uptime: 'Uptime 24h',
    no_nodes: 'no nodes selected', dc_ok: 'DCs answered: {0} of {1}',
    empty: 'No proxies yet', empty_hint: 'Paste a tg://proxy or t.me/proxy link and pick the nodes to check it from',
    f_add: 'New proxy', f_edit: 'Proxy settings', link: 'Proxy link', name: 'Name', pick: 'Check from these nodes',
    pick_hint: 'Selected nodes run only this check; other checks do not appear on them', pick_none: 'Pick at least one node',
    bad_link: 'Expected a tg://proxy?… or https://t.me/proxy?… link', hub: 'hub', offline: 'offline', saved: 'Saved', created: 'Proxy added',
  },
};

/** proxyEndpoint reads the server and port of an MTProxy link, never its secret. */
function proxyEndpoint(link: string) {
  const m = link.trim().match(/^(?:tg:\/\/proxy|https?:\/\/(?:t|telegram)\.me\/proxy)\/?\?(.*)$/i);
  if (!m) return null;
  const q = new URLSearchParams(m[1].replaceAll('+', '%2B'));
  const server = q.get('server'), port = q.get('port');
  return server && port ? { server, port } : null;
}

const NODE_PREFIX = 'id=';

function ProxyForm({ initial, onClose }: { initial?: CheckConfig; onClose: () => void }) {
  const t = useT(L);
  const c = useCommon();
  const probes = useProbes({ live: false });
  const [link, setLink] = useState(initial?.mtproto?.link ?? '');
  const [name, setName] = useState(initial?.name ?? '');
  const [nodes, setNodes] = useState<string[]>((initial?.probes ?? []).filter((s) => s.startsWith(NODE_PREFIX)).map((s) => s.slice(NODE_PREFIX.length)));
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);
  const [pristine] = useState(() => JSON.stringify([link, name, nodes]));
  const guard = useGuardedClose(JSON.stringify([link, name, nodes]) !== pristine, onClose);
  const pinned = !!initial && !initial.mtproto?.link; // address + secret, set in the full check form
  // MTProxy links carry their secret: an untrusted checker agent never gets one.
  const list = [...(probes.data?.probes ?? [])].filter((p) => !isChecker(p) || p.config.trusted || nodes.includes(p.id)).sort((a, b) => a.name.localeCompare(b.name));

  const submit = async () => {
    setErr('');
    const ep = proxyEndpoint(link);
    if (!pinned && !ep) return setErr(t('bad_link'));
    if (!nodes.length) return setErr(t('pick_none'));
    const endpoint = ep ? `${ep.server}:${ep.port}` : '';
    const body: CheckConfig = {
      ...(initial ?? { id: `tg-${endpoint.toLowerCase().replace(/[^a-z0-9]+/g, '-')}`, type: 'mtproto', group: 'Telegram' }),
      name: name.trim() || endpoint,
      mtproto: pinned ? initial!.mtproto : { ...initial?.mtproto, link: link.trim() },
      probes: nodes.map((id) => NODE_PREFIX + id),
    };
    setBusy(true);
    try {
      if (initial) await api('PUT', `/api/v1/checks/${enc(initial.id)}`, body);
      else await api('POST', '/api/v1/checks', body);
      await queryClient.invalidateQueries();
      notifyOk(initial ? t('saved') : t('created'));
      onClose();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal opened {...guard.modalProps} size="lg" title={<Text fw={700} fz="lg">{initial ? t('f_edit') : t('f_add')}</Text>}>
      <Stack gap="md">
        {err && <Alert color="red" icon={<IconAlertCircle size={16} />}>{err}</Alert>}
        {!pinned && (
          <TextInput label={t('link')} placeholder="https://t.me/proxy?server=…&port=443&secret=ee…" value={link} onChange={(e) => setLink(e.currentTarget.value)} required styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)', fontSize: 12 } }} />
        )}
        <TextInput label={t('name')} placeholder={proxyEndpoint(link) ? `${proxyEndpoint(link)!.server}:${proxyEndpoint(link)!.port}` : ''} value={name} onChange={(e) => setName(e.currentTarget.value)} />
        <Checkbox.Group label={t('pick')} description={t('pick_hint')} value={nodes} onChange={setNodes} required>
          <SimpleGrid cols={{ base: 1, sm: 2 }} spacing={6} mt="xs">
            {list.map((p) => (
              <Checkbox
                key={p.id}
                value={p.id}
                label={
                  <Group gap={6} wrap="nowrap">
                    <Text size="sm" truncate>{flag(p.region)} {p.name}</Text>
                    {p.mode === 'local' && <Text size="xs" c="dimmed">{t('hub')}</Text>}
                    {!p.online && <Text size="xs" c="red.5">{t('offline')}</Text>}
                  </Group>
                }
              />
            ))}
          </SimpleGrid>
        </Checkbox.Group>
        <Group justify="flex-end">
          <Button variant="default" onClick={guard.requestClose}>{c('cancel')}</Button>
          <Button onClick={submit} loading={busy}>{c('save')}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}

/** dcSummary counts the DCs on the best node, since one node's path can be the problem. */
function dcSummary(r: CheckRow) {
  let best = -1, total = 0;
  for (const c of r.probes) {
    const up = (c.dc_ms ?? '').split(' ').filter(Boolean).length;
    const all = up + (c.dc_down ?? '').split(',').filter(Boolean).length;
    if (all && up > best) { best = up; total = all; }
  }
  return best < 0 ? null : { up: best, total };
}

export default function TelegramProxy() {
  const t = useT(L);
  const nav = useNavigate();
  const { data, isLoading, isError, error, refetch } = useChecks();
  const meta = useMeta();
  const isAdmin = meta.data?.user.role === 'admin';
  const [form, setForm] = useState<{ initial?: CheckConfig } | null>(null);

  const rows = (data?.checks ?? []).filter((r) => r.type === 'mtproto');

  const edit = async (id: string) => {
    try {
      const r = await api<{ config: CheckConfig }>('GET', `/api/v1/checks/${enc(id)}`);
      setForm({ initial: r.config });
    } catch (e) {
      notifyError(e);
    }
  };
  const setPaused = async (id: string, paused: boolean) => {
    try {
      await api('POST', `/api/v1/checks/${enc(id)}/${paused ? 'pause' : 'resume'}`);
      await queryClient.invalidateQueries();
    } catch (e) {
      notifyError(e);
    }
  };

  const add = isAdmin ? <Button leftSection={<IconPlus size={16} />} onClick={() => setForm({})}>{t('add')}</Button> : undefined;
  if (isLoading) return <Stack><PageHeader title={t('title')} /><Skeleton h={300} radius="lg" /></Stack>;
  if (isError && !data) return <Stack><PageHeader title={t('title')} /><QueryError error={error} onRetry={() => refetch()} /></Stack>;

  return (
    <>
      <PageHeader title={t('title')} subtitle={t('subtitle')} right={add} />
      {rows.length === 0 ? (
        <Card padding="xl">
          <Stack align="center" gap="sm" py="xl">
            <TelegramLogo size={56} />
            <Text fw={650} fz="lg">{t('empty')}</Text>
            <Text c="dimmed" size="sm" ta="center">{t('empty_hint')}</Text>
            {add}
          </Stack>
        </Card>
      ) : (
        <Card padding={0} style={{ overflow: 'hidden' }}>
          <Box className="uf-scroll-x">
            <Table className="uf-table" verticalSpacing="sm" miw={820}>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th w={150}>{t('state')}</Table.Th>
                  <Table.Th>{t('proxy')}</Table.Th>
                  <Table.Th>{t('nodes')}</Table.Th>
                  <Table.Th ta="right">{t('dcs')}</Table.Th>
                  <Table.Th ta="right">{t('latency')}</Table.Th>
                  <Table.Th ta="right">{t('uptime')}</Table.Th>
                  {isAdmin && <Table.Th w={84} />}
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {rows.map((r) => {
                  const dc = dcSummary(r);
                  return (
                    <Table.Tr key={r.id} {...clickableRow(() => nav(`/checks/${enc(r.id)}`))}>
                      <Table.Td><VerdictBadge kind={r.verdict} region={r.region} size="sm" /></Table.Td>
                      <Table.Td>
                        <Anchor component={Link} to={`/checks/${enc(r.id)}`} size="sm" fw={600} c="var(--mantine-color-text)" onClick={(e) => e.stopPropagation()}>{r.name}</Anchor>
                        {r.target !== r.name && <Text size="xs" c="dimmed" ff="monospace">{r.target}</Text>}
                      </Table.Td>
                      <Table.Td>
                        {r.probes.length ? <ProbeDots row={r} probes={data?.probes ?? []} /> : <Text size="xs" c="dimmed">{r.paused ? '—' : t('no_nodes')}</Text>}
                      </Table.Td>
                      <Table.Td ta="right">
                        {dc ? (
                          <Tooltip label={t('dc_ok', dc.up, dc.total)}>
                            <Text span size="sm" className="uf-num" c={dc.up < dc.total ? 'yellow.5' : undefined}>{dc.up}/{dc.total}</Text>
                          </Tooltip>
                        ) : <Text span size="sm" c="dimmed">—</Text>}
                      </Table.Td>
                      <Table.Td ta="right"><Text size="sm" className="uf-num">{ms(r.latency_24h)}</Text></Table.Td>
                      <Table.Td ta="right"><Text size="sm" className="uf-num" fw={600} c={uptimeColor(r.uptime_24h)}>{pct(r.uptime_24h)}</Text></Table.Td>
                      {isAdmin && (
                        <Table.Td onClick={(e) => e.stopPropagation()}>
                          <Group gap={4} wrap="nowrap" justify="flex-end">
                            {!r.managed && (
                              <Tooltip label={t('edit')}>
                                <ActionIcon variant="subtle" color="gray" aria-label={t('edit')} onClick={() => edit(r.id)}><IconPencil size={16} /></ActionIcon>
                              </Tooltip>
                            )}
                            <Tooltip label={r.paused ? t('resume') : t('pause')}>
                              <ActionIcon variant="subtle" color="gray" aria-label={r.paused ? t('resume') : t('pause')} onClick={() => setPaused(r.id, !r.paused)}>
                                {r.paused ? <IconPlayerPlay size={16} /> : <IconPlayerPause size={16} />}
                              </ActionIcon>
                            </Tooltip>
                          </Group>
                        </Table.Td>
                      )}
                    </Table.Tr>
                  );
                })}
              </Table.Tbody>
            </Table>
          </Box>
        </Card>
      )}
      {form && <ProxyForm key={form.initial?.id ?? 'new'} initial={form.initial} onClose={() => setForm(null)} />}
    </>
  );
}
