import {
  ActionIcon, Alert, Avatar, Badge, Button, Card, Group, Modal, PasswordInput, SegmentedControl, SimpleGrid, Skeleton, Stack, Table, Text,
  TextInput, Tooltip,
} from '@mantine/core';
import { modals } from '@mantine/modals';
import { IconEye, IconKey, IconShieldLock, IconTrash, IconUserPlus } from '@tabler/icons-react';
import dayjs from 'dayjs';
import { useState, type FormEvent } from 'react';
import { enc, useApiMutation, useMeta, useUsers, type User } from '../../api';
import { ChangeOwnPassword } from '../../components/AccountMenu';
import { notifyError, notifyOk, PageHeader } from '../../components/Misc';
import { ago } from '../../format';
import { useCommon, useLang, useT } from '../../i18n';

const L = {
  ru: {
    title: 'Пользователи', subtitle: 'Кто может заходить в UptimeForge', add: 'Добавить пользователя',
    username: 'Логин', role: 'Роль', created: 'Создан', you: 'это вы', admin: 'Администратор', viewer: 'Наблюдатель',
    password: 'Пароль', password2: 'Ещё раз', password_hint: 'Не короче 8 символов', too_short: 'Не короче 8 символов',
    mismatch: 'Пароли не совпадают', change_pw: 'Сменить пароль', change_pw_for: 'Новый пароль для {0}', pw_changed: 'Пароль изменён',
    created_ok: 'Пользователь {0} добавлен', cant_delete_self: 'Нельзя удалить себя', delete_title: 'Удалить пользователя',
    roles: 'Роли', admin_desc: 'Всё: проверки, пробы, настройки, уведомления, пользователи.',
    viewer_desc: 'Смотрит дашборды и может принимать и глушить инциденты. Ничего не меняет в конфигурации.',
    bad_username: 'Только латиница, цифры, точка, дефис и подчёркивание',
  },
  en: {
    title: 'Users', subtitle: 'Who can sign in to UptimeForge', add: 'Add user',
    username: 'Username', role: 'Role', created: 'Created', you: 'you', admin: 'Administrator', viewer: 'Viewer',
    password: 'Password', password2: 'Repeat', password_hint: 'At least 8 characters', too_short: 'At least 8 characters',
    mismatch: 'Passwords do not match', change_pw: 'Change password', change_pw_for: 'New password for {0}', pw_changed: 'Password changed',
    created_ok: 'User {0} added', cant_delete_self: "You can't delete yourself", delete_title: 'Delete user',
    roles: 'Roles', admin_desc: 'Everything: checks, probes, settings, notifications, users.',
    viewer_desc: 'Sees dashboards and may acknowledge and mute incidents. Changes nothing in the configuration.',
    bad_username: 'Latin letters, digits, dot, dash and underscore only',
  },
};

// Badges in table cells must not shrink into an ellipsis.
const badgeStyles = { root: { textTransform: 'none' as const, flex: 'none' }, label: { overflow: 'visible' } };

function RoleBadge({ role }: { role: User['role'] }) {
  const t = useT(L);
  return role === 'admin'
    ? <Badge variant="light" color="forge" leftSection={<IconShieldLock size={12} />} styles={badgeStyles}>{t('admin')}</Badge>
    : <Badge variant="light" color="gray" leftSection={<IconEye size={12} />} styles={badgeStyles}>{t('viewer')}</Badge>;
}

function AddUser({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const t = useT(L);
  const c = useCommon();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [role, setRole] = useState<'admin' | 'viewer'>('viewer');
  const [tried, setTried] = useState(false);
  const create = useApiMutation<{ username: string; password: string; role: string }>('POST', () => '/api/v1/users', (b) => b);
  const userErr = username && !/^[A-Za-z0-9._-]+$/.test(username) ? t('bad_username') : undefined;
  const pwErr = (tried || password) && password.length < 8 ? t('too_short') : undefined;
  const close = () => {
    setUsername(''); setPassword(''); setRole('viewer'); setTried(false);
    onClose();
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setTried(true);
    if (!username || userErr || password.length < 8) return;
    create.mutate({ username: username.trim(), password, role }, {
      onSuccess: () => { notifyOk(t('created_ok', username)); close(); },
      onError: notifyError,
    });
  };
  return (
    <Modal opened={opened} onClose={close} title={<Text fw={650}>{t('add')}</Text>}>
      <form onSubmit={submit}>
        <Stack>
          <TextInput label={t('username')} value={username} onChange={(e) => setUsername(e.currentTarget.value)} error={userErr} required data-autofocus autoComplete="off" />
          <PasswordInput label={t('password')} description={t('password_hint')} value={password} onChange={(e) => setPassword(e.currentTarget.value)} error={pwErr} required autoComplete="new-password" />
          <Stack gap={4}>
            <Text size="sm" fw={500}>{t('role')}</Text>
            <SegmentedControl
              value={role}
              onChange={(v) => setRole(v as 'admin' | 'viewer')}
              data={[{ value: 'viewer', label: t('viewer') }, { value: 'admin', label: t('admin') }]}
              fullWidth
            />
            <Text size="xs" c="dimmed">{role === 'admin' ? t('admin_desc') : t('viewer_desc')}</Text>
          </Stack>
          <Group justify="flex-end" mt="xs">
            <Button variant="default" onClick={close}>{c('cancel')}</Button>
            <Button type="submit" loading={create.isPending} leftSection={<IconUserPlus size={16} />}>{c('add')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

function ChangePassword({ user, onClose }: { user: User | null; onClose: () => void }) {
  const t = useT(L);
  const c = useCommon();
  const [pw, setPw] = useState('');
  const [pw2, setPw2] = useState('');
  const [tried, setTried] = useState(false);
  const set = useApiMutation<{ id: number; password: string }>('PUT', (b) => `/api/v1/users/${enc(String(b.id))}/password`, (b) => ({ password: b.password }));
  const close = () => { setPw(''); setPw2(''); setTried(false); onClose(); };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setTried(true);
    if (!user || pw.length < 8 || pw !== pw2) return;
    set.mutate({ id: user.id, password: pw }, { onSuccess: () => { notifyOk(t('pw_changed')); close(); }, onError: notifyError });
  };
  return (
    <Modal opened={!!user} onClose={close} title={<Text fw={650}>{t('change_pw_for', user?.username ?? '')}</Text>}>
      <form onSubmit={submit}>
        <Stack>
          <PasswordInput
            label={t('password')}
            description={t('password_hint')}
            value={pw}
            onChange={(e) => setPw(e.currentTarget.value)}
            error={(tried || pw) && pw.length < 8 ? t('too_short') : undefined}
            required
            data-autofocus
            autoComplete="new-password"
          />
          <PasswordInput
            label={t('password2')}
            value={pw2}
            onChange={(e) => setPw2(e.currentTarget.value)}
            error={tried && pw !== pw2 ? t('mismatch') : undefined}
            required
            autoComplete="new-password"
          />
          <Group justify="flex-end" mt="xs">
            <Button variant="default" onClick={close}>{c('cancel')}</Button>
            <Button type="submit" loading={set.isPending} leftSection={<IconKey size={16} />}>{c('save')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

export default function Users() {
  const t = useT(L);
  const c = useCommon();
  const { lang } = useLang();
  const q = useUsers();
  const me = useMeta().data?.user;
  const [adding, setAdding] = useState(false);
  const [pwUser, setPwUser] = useState<User | null>(null);
  const del = useApiMutation<number>('DELETE', (id) => `/api/v1/users/${enc(String(id))}`);

  const confirmDelete = (u: User) =>
    modals.openConfirmModal({
      title: <Text fw={650}>{t('delete_title')}</Text>,
      children: <Text size="sm">{c('confirm_delete', u.username)}</Text>,
      labels: { confirm: c('delete'), cancel: c('cancel') },
      confirmProps: { color: 'red' },
      onConfirm: () => del.mutate(u.id, { onSuccess: () => notifyOk(c('deleted')), onError: notifyError }),
    });

  const users = [...(q.data ?? [])].sort((a, b) => (a.role === b.role ? a.username.localeCompare(b.username) : a.role === 'admin' ? -1 : 1));

  return (
    <>
      <PageHeader
        title={t('title')}
        subtitle={t('subtitle')}
        right={<Button leftSection={<IconUserPlus size={16} />} onClick={() => setAdding(true)}>{t('add')}</Button>}
      />
      <Stack gap="lg">
        {q.isError && <Alert color="red">{q.error.message}</Alert>}
        <Card padding={0}>
          <Table.ScrollContainer minWidth={560}>
            <Table className="uf-table" verticalSpacing="sm" horizontalSpacing="md" style={{ whiteSpace: 'nowrap' }}>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>{t('username')}</Table.Th>
                  <Table.Th>{t('role')}</Table.Th>
                  <Table.Th>{t('created')}</Table.Th>
                  <Table.Th w={100} />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {q.isLoading && [0, 1].map((i) => <Table.Tr key={i}><Table.Td colSpan={4}><Skeleton h={28} /></Table.Td></Table.Tr>)}
                {users.map((u) => {
                  const self = u.id === me?.id;
                  return (
                    <Table.Tr key={u.id}>
                      <Table.Td>
                        <Group gap="sm" wrap="nowrap">
                          <Avatar size={32} radius="md" color={u.role === 'admin' ? 'forge' : 'gray'} variant="light">
                            {u.username.slice(0, 2).toUpperCase()}
                          </Avatar>
                          <Text size="sm" fw={600}>{u.username}</Text>
                          {self && <Badge size="xs" variant="outline" color="gray" styles={badgeStyles}>{t('you')}</Badge>}
                        </Group>
                      </Table.Td>
                      <Table.Td><RoleBadge role={u.role} /></Table.Td>
                      <Table.Td>
                        <Tooltip label={dayjs(u.created).format('DD.MM.YYYY HH:mm')}>
                          <Text size="sm" c="dimmed" span>{ago(u.created, lang)}</Text>
                        </Tooltip>
                      </Table.Td>
                      <Table.Td>
                        <Group gap={4} justify="flex-end" wrap="nowrap">
                          <Tooltip label={t('change_pw')}>
                            <ActionIcon variant="subtle" color="gray" size="lg" onClick={() => setPwUser(u)} aria-label={t('change_pw')}>
                              <IconKey size={17} />
                            </ActionIcon>
                          </Tooltip>
                          <Tooltip label={self ? t('cant_delete_self') : c('delete')}>
                            <ActionIcon variant="subtle" color="red" size="lg" onClick={() => confirmDelete(u)} disabled={self} aria-label={c('delete')}>
                              <IconTrash size={17} />
                            </ActionIcon>
                          </Tooltip>
                        </Group>
                      </Table.Td>
                    </Table.Tr>
                  );
                })}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        </Card>

        <SimpleGrid cols={{ base: 1, sm: 2 }} spacing="md">
          <Card padding="md">
            <Stack gap={6} align="flex-start">
              <RoleBadge role="admin" />
              <Text size="sm" c="dimmed">{t('admin_desc')}</Text>
            </Stack>
          </Card>
          <Card padding="md">
            <Stack gap={6} align="flex-start">
              <RoleBadge role="viewer" />
              <Text size="sm" c="dimmed">{t('viewer_desc')}</Text>
            </Stack>
          </Card>
        </SimpleGrid>
      </Stack>
      <AddUser opened={adding} onClose={() => setAdding(false)} />
      {/* The hub asks for the current password when it is your own: the account menu form has that field. */}
      {pwUser && me && pwUser.id === me.id
        ? <ChangeOwnPassword user={pwUser} opened onClose={() => setPwUser(null)} />
        : <ChangePassword user={pwUser} onClose={() => setPwUser(null)} />}
    </>
  );
}
