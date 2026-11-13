import {
  ActionIcon, Badge, Box, Button, Card, Group, Menu, Modal, Skeleton, Stack, Switch, Table, Text, TextInput, Tooltip,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import {
  IconDots, IconFlask2, IconLock, IconPencil, IconPlus, IconTicket, IconTrash, IconWorldSearch,
} from '@tabler/icons-react';
import { useMemo, useState } from 'react';
import { useNavigate } from 'react-router';
import { api, enc, isChecker, queryClient, useInvites, useIsAdmin, useProbes, type Invite, type ProbeConfig, type ProbeRow } from '../api';
import { Section } from '../components/Cards';
import { InviteModal } from '../components/InviteModal';
import { clickableRow, notifyError, notifyOk, PageHeader, QueryError } from '../components/Misc';
import { probeBody } from '../components/ProbeForm';
import { Dot } from '../components/Status';
import { ago, clock, flag } from '../format';
import { countWord, useCommon, useLang, useT } from '../i18n';
import { status } from '../theme';

const L = {
  ru: {
    title: 'Агенты проверок', subtitle: 'Точки наблюдения на чужих машинах: проверяют ваши ноды глазами абонентов разных операторов',
    add: 'Добавить точку', name: 'Точка', operator: 'Оператор', state: 'Связь', trusted: 'Доверенный', version: 'Версия', checks: 'Проверок',
    online: 'в сети', no_net: 'нет интернета', seen: 'был {0}', never: 'ещё не подключался',
    rename: 'Переименовать', trust_on: 'Сделать доверенным', trust_off: 'Снять доверие', check_here: 'Проверить отсюда', delete: 'Удалить',
    trust_title: 'Доверить точке «{0}»?',
    trust_body: 'Она получит VPN-ссылки и подписки, MTProxy-секреты и пароли из проверок, которые на неё направлены. Владелец машины сможет их прочитать.',
    trust_ok: 'Доверять', trusted_saved: '«{0}» теперь доверенная', untrusted_saved: '«{0}» больше не получает проверки с секретами',
    del_body: 'Удалить агента проверок «{0}»? Его токен перестанет работать, проверки с этой точки прекратятся. Чтобы вернуть её, нужно новое приглашение.',
    deleted: 'Точка удалена', renamed: 'Переименовано', new_name: 'Новое название', managed: 'Из config.yaml',
    empty: 'Агентов проверок пока нет',
    empty_hint: 'Агент проверок — это uptimeforge-agent на машине добровольца: домашний ПК или виртуалка у МТС, Ростелекома, Билайна… Он проверяет ваши ноды из сети этого оператора и не видит ваших VPN-конфигов, пока вы не отметите его доверенным. Метрики хоста он не присылает, его пропажа не тревога.',
    invites: 'Приглашения', inv_name: 'Название', inv_created: 'Создано', inv_expires: 'Срок', inv_state: 'Состояние',
    inv_active: 'ждёт', inv_used: 'использовано', inv_expired: 'истекло', revoke: 'Отозвать', remove: 'Убрать',
    revoke_body: 'Отозвать приглашение «{0}»? Команда с этим кодом перестанет работать.', revoked: 'Приглашение отозвано',
    no_invites: 'Активных приглашений нет', unnamed: 'без названия', w_check: 'проверка|проверки|проверок', checks_tip: 'Назначено {0} {1}',
  },
  en: {
    title: 'Checker agents', subtitle: 'Vantage points on other people’s machines: they test your nodes the way subscribers of different operators see them',
    add: 'Add a point', name: 'Point', operator: 'Operator', state: 'Link', trusted: 'Trusted', version: 'Version', checks: 'Checks',
    online: 'online', no_net: 'no internet', seen: 'seen {0}', never: 'never connected',
    rename: 'Rename', trust_on: 'Trust it', trust_off: 'Stop trusting', check_here: 'Check from here', delete: 'Delete',
    trust_title: 'Trust “{0}”?',
    trust_body: 'It will get the VPN links and subscriptions, MTProxy secrets and passwords of the checks aimed at it. The machine’s owner can read them.',
    trust_ok: 'Trust', trusted_saved: '“{0}” is trusted now', untrusted_saved: '“{0}” no longer gets checks with secrets',
    del_body: 'Delete checker agent “{0}”? Its token stops working and checks from it stop. Bringing it back takes a new invite.',
    deleted: 'Point deleted', renamed: 'Renamed', new_name: 'New name', managed: 'From config.yaml',
    empty: 'No checker agents yet',
    empty_hint: 'A checker agent is uptimeforge-agent on a volunteer’s machine: a home PC or a VM at some ISP. It tests your nodes from that operator’s network and never sees your VPN configs unless you mark it trusted. It sends no host metrics, and it going away is not an alert.',
    invites: 'Invites', inv_name: 'Name', inv_created: 'Created', inv_expires: 'Expires', inv_state: 'State',
    inv_active: 'waiting', inv_used: 'used', inv_expired: 'expired', revoke: 'Revoke', remove: 'Remove',
    revoke_body: 'Revoke the invite “{0}”? The command with its code stops working.', revoked: 'Invite revoked',
    no_invites: 'No active invites', unnamed: 'unnamed', w_check: 'check|checks', checks_tip: '{0} {1} assigned',
  },
};

async function saveProbe(p: ProbeRow, patch: Partial<ProbeConfig>) {
  const next = { ...p.config, ...patch };
  // Nothing here touches labels: what the config has is what the form "showed".
  await api('PUT', `/api/v1/probes/${enc(p.id)}`, probeBody(next, next.labels ?? {}));
  await queryClient.invalidateQueries();
}

function RenameModal({ probe, onClose }: { probe: ProbeRow | null; onClose: () => void }) {
  const t = useT(L);
  const c = useCommon();
  const [name, setName] = useState(probe?.name ?? '');
  const [busy, setBusy] = useState(false);
  const submit = async () => {
    if (!probe || !name.trim()) return;
    setBusy(true);
    try {
      await saveProbe(probe, { name: name.trim() });
      notifyOk(t('renamed'));
      onClose();
    } catch (e) {
      notifyError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal opened={!!probe} onClose={onClose} title={<Text fw={700}>{t('rename')}</Text>}>
      <Stack>
        <TextInput label={t('new_name')} value={name} onChange={(e) => setName(e.currentTarget.value)} data-autofocus
          onKeyDown={(e) => { if (e.key === 'Enter') submit(); }} />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>{c('cancel')}</Button>
          <Button onClick={submit} loading={busy} disabled={!name.trim()}>{c('save')}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}

function Invites({ list }: { list: Invite[] }) {
  const t = useT(L);
  const c = useCommon();
  const { lang } = useLang();
  const now = Date.now();
  const state = (i: Invite) => (i.used ? 'used' : Date.parse(i.expires) < now ? 'expired' : 'active');
  const rows = [...list].sort((a, b) => Number(state(a) !== 'active') - Number(state(b) !== 'active') || b.created.localeCompare(a.created));

  const remove = async (i: Invite) => {
    try {
      await api('DELETE', `/api/v1/checkers/invites/${enc(i.id)}`);
      await queryClient.invalidateQueries({ queryKey: ['invites'] });
      if (state(i) === 'active') notifyOk(t('revoked'));
    } catch (e) {
      notifyError(e);
    }
  };
  const revoke = (i: Invite) => modals.openConfirmModal({
    title: t('revoke'), children: <Text size="sm">{t('revoke_body', i.name || t('unnamed'))}</Text>,
    labels: { confirm: t('revoke'), cancel: c('cancel') }, confirmProps: { color: 'red' },
    onConfirm: () => remove(i),
  });

  if (!rows.length) return <Text size="sm" c="dimmed">{t('no_invites')}</Text>;
  return (
    <Card padding={0}>
      <Box className="uf-scroll-x">
        <Table className="uf-table" verticalSpacing="sm" miw={620}>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>{t('inv_name')}</Table.Th><Table.Th>{t('inv_state')}</Table.Th><Table.Th>{t('inv_created')}</Table.Th>
              <Table.Th>{t('inv_expires')}</Table.Th><Table.Th />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {rows.map((i) => {
              const st = state(i);
              return (
                <Table.Tr key={i.id} style={{ opacity: st === 'active' ? 1 : 0.6 }}>
                  <Table.Td>
                    <Group gap={6} wrap="nowrap">
                      <Text size="sm" fw={600} c={i.name ? undefined : 'dimmed'}>{i.name || t('unnamed')}</Text>
                      {i.trusted && <Tooltip label={t('trusted')}><IconLock size={14} /></Tooltip>}
                    </Group>
                  </Table.Td>
                  <Table.Td><Badge variant="light" color={st === 'active' ? 'forge' : 'gray'} radius="sm">{t(`inv_${st}`)}</Badge></Table.Td>
                  <Table.Td><Text size="sm">{clock(i.created, true)}</Text></Table.Td>
                  <Table.Td><Text size="sm">{st === 'active' ? ago(i.expires, lang) : clock(i.expires, true)}</Text></Table.Td>
                  <Table.Td ta="right">
                    {st === 'active'
                      ? <Button size="xs" variant="subtle" color="red" onClick={() => revoke(i)}>{t('revoke')}</Button>
                      : <Button size="xs" variant="subtle" color="gray" onClick={() => remove(i)}>{t('remove')}</Button>}
                  </Table.Td>
                </Table.Tr>
              );
            })}
          </Table.Tbody>
        </Table>
      </Box>
    </Card>
  );
}

export default function Checkers() {
  const t = useT(L);
  const c = useCommon();
  const { lang } = useLang();
  const nav = useNavigate();
  const isAdmin = useIsAdmin();
  const probes = useProbes();
  const invites = useInvites(isAdmin);
  const [adding, setAdding] = useState(false);
  const [renaming, setRenaming] = useState<ProbeRow | null>(null);

  const list = useMemo(() => (probes.data?.probes ?? []).filter(isChecker)
    .sort((a, b) => Number(b.online) - Number(a.online) || a.name.localeCompare(b.name)), [probes.data]);

  const setTrust = (p: ProbeRow, trusted: boolean) => {
    const save = async () => {
      try {
        await saveProbe(p, { trusted });
        notifyOk(t(trusted ? 'trusted_saved' : 'untrusted_saved', p.name));
      } catch (e) {
        notifyError(e);
      }
    };
    if (!trusted) return save();
    modals.openConfirmModal({
      title: t('trust_title', p.name), children: <Text size="sm">{t('trust_body')}</Text>,
      labels: { confirm: t('trust_ok'), cancel: c('cancel') }, confirmProps: { color: 'orange' }, onConfirm: save,
    });
  };
  const remove = (p: ProbeRow) => modals.openConfirmModal({
    title: t('delete'), children: <Text size="sm">{t('del_body', p.name)}</Text>,
    labels: { confirm: t('delete'), cancel: c('cancel') }, confirmProps: { color: 'red' },
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

  const addButton = isAdmin && <Button leftSection={<IconPlus size={16} />} onClick={() => setAdding(true)}>{t('add')}</Button>;
  if (!probes.data && probes.isError) return <Stack><PageHeader title={t('title')} /><QueryError error={probes.error} onRetry={() => probes.refetch()} /></Stack>;

  return (
    <Stack gap="xl">
      <PageHeader title={t('title')} subtitle={t('subtitle')} right={list.length ? addButton : undefined} />
      {probes.isLoading ? <Skeleton h={240} radius="lg" /> : list.length === 0 ? (
        <Card padding="xl" style={{ borderStyle: 'dashed', background: 'transparent' }}>
          <Stack align="center" gap="sm" py="md">
            <Group gap="xs" c="dimmed"><IconWorldSearch size={26} /></Group>
            <Text fw={650}>{t('empty')}</Text>
            <Text size="sm" c="dimmed" ta="center" maw={620}>{t('empty_hint')}</Text>
            {addButton}
          </Stack>
        </Card>
      ) : (
        <Card padding={0}>
          <Box className="uf-scroll-x">
            <Table className="uf-table" verticalSpacing="sm" miw={760}>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>{t('name')}</Table.Th><Table.Th>{t('operator')}</Table.Th><Table.Th>{t('state')}</Table.Th>
                  <Table.Th>{t('trusted')}</Table.Th><Table.Th>{t('version')}</Table.Th><Table.Th ta="right">{t('checks')}</Table.Th>
                  {isAdmin && <Table.Th w={48} />}
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {list.map((p) => {
                  const n = p.network;
                  const country = p.region || n?.country;
                  // A checker going offline is not an alarm: grey, not red.
                  const dot = !p.online ? status.unknown : !p.internet ? status.partial : status.ok;
                  const link = !p.version && !p.online ? t('never') : !p.online ? t('seen', ago(p.since, lang)) : !p.internet ? t('no_net') : t('online');
                  return (
                    <Table.Tr key={p.id} {...clickableRow(() => nav(`/probes/${enc(p.id)}`))}>
                      <Table.Td>
                        <Group gap={8} wrap="nowrap">
                          <Text fz="lg" lh={1}>{flag(country) || '📍'}</Text>
                          <Box style={{ minWidth: 0 }}>
                            <Text size="sm" fw={600} truncate>{p.name}</Text>
                            {p.host && <Text size="xs" c="dimmed" ff="monospace" truncate>{p.host}</Text>}
                          </Box>
                          {p.managed && <Badge variant="outline" color="gray" size="xs">{t('managed')}</Badge>}
                        </Group>
                      </Table.Td>
                      <Table.Td>
                        <Text size="sm">{[n?.org || n?.isp, n?.asn?.toUpperCase()].filter(Boolean).join(' · ') || '—'}</Text>
                      </Table.Td>
                      <Table.Td>
                        <Group gap={6} wrap="nowrap"><Dot color={dot} /><Text size="sm" c={p.online ? undefined : 'dimmed'}>{link}</Text></Group>
                      </Table.Td>
                      <Table.Td onClick={(e) => e.stopPropagation()}>
                        {isAdmin && !p.managed
                          ? <Switch size="sm" color="orange" checked={!!p.config.trusted} onChange={(e) => setTrust(p, e.currentTarget.checked)} aria-label={t('trusted')} />
                          : p.config.trusted ? <IconLock size={16} aria-label={t('trusted')} /> : <Text size="sm" c="dimmed">—</Text>}
                      </Table.Td>
                      <Table.Td><Text size="sm" ff="monospace">{p.version || '—'}</Text></Table.Td>
                      <Table.Td ta="right">
                        <Tooltip label={t('checks_tip', p.checks, countWord(lang, p.checks, t('w_check')))}>
                          <Text size="sm" className="uf-num" fw={600}>{p.checks}</Text>
                        </Tooltip>
                      </Table.Td>
                      {isAdmin && (
                        <Table.Td onClick={(e) => e.stopPropagation()}>
                          <Menu position="bottom-end">
                            <Menu.Target><ActionIcon variant="subtle" color="gray" aria-label={t('title')}><IconDots size={16} /></ActionIcon></Menu.Target>
                            <Menu.Dropdown>
                              <Menu.Item leftSection={<IconFlask2 size={15} />} onClick={() => nav(`/tools?probe=${enc(p.id)}`)}>{t('check_here')}</Menu.Item>
                              {!p.managed && (
                                <>
                                  <Menu.Item leftSection={<IconPencil size={15} />} onClick={() => setRenaming(p)}>{t('rename')}</Menu.Item>
                                  <Menu.Item leftSection={<IconLock size={15} />} onClick={() => setTrust(p, !p.config.trusted)}>{t(p.config.trusted ? 'trust_off' : 'trust_on')}</Menu.Item>
                                  <Menu.Divider />
                                  <Menu.Item color="red" leftSection={<IconTrash size={15} />} onClick={() => remove(p)}>{t('delete')}</Menu.Item>
                                </>
                              )}
                            </Menu.Dropdown>
                          </Menu>
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

      {isAdmin && (
        <Section title={<Group gap={8}><IconTicket size={18} />{t('invites')}</Group>}>
          {invites.data ? <Invites list={invites.data.invites ?? []} /> : invites.isError
            ? <QueryError error={invites.error} onRetry={() => invites.refetch()} /> : <Skeleton h={80} radius="lg" />}
        </Section>
      )}

      {renaming && <RenameModal probe={renaming} onClose={() => setRenaming(null)} />}
      {isAdmin && <InviteModal opened={adding} onClose={() => setAdding(false)} hubURL={probes.data?.hub_url ?? ''} />}
    </Stack>
  );
}
