import { ActionIcon, Button, Group, Menu, Modal, PasswordInput, Stack, Text, Tooltip } from '@mantine/core';
import { IconKey, IconLogout, IconUserCircle } from '@tabler/icons-react';
import { useState, type FormEvent } from 'react';
import { api, ApiError, enc, queryClient, type User } from '../api';
import { useCommon, useT } from '../i18n';
import { notifyOk } from './Misc';

const L = {
  ru: {
    account: 'Учётная запись: {0}', admin: 'администратор', viewer: 'наблюдатель', change_pw: 'Сменить пароль', logout: 'Выйти',
    current: 'Текущий пароль', new_pw: 'Новый пароль', new_pw2: 'Новый пароль ещё раз', hint: 'Не короче 8 символов',
    too_short: 'Не короче 8 символов', mismatch: 'Пароли не совпадают', wrong: 'Текущий пароль неверный', changed: 'Пароль изменён',
  },
  en: {
    account: 'Account: {0}', admin: 'administrator', viewer: 'viewer', change_pw: 'Change password', logout: 'Sign out',
    current: 'Current password', new_pw: 'New password', new_pw2: 'New password again', hint: 'At least 8 characters',
    too_short: 'At least 8 characters', mismatch: 'Passwords do not match', wrong: 'The current password is wrong', changed: 'Password changed',
  },
};

/**
 * ChangeOwnPassword lets any signed-in user, viewers included, change their
 * own password. The hub asks for the current one (`current`), so a session
 * left open on someone else's screen cannot be turned into the account.
 */
export function ChangeOwnPassword({ user, opened, onClose }: { user: User; opened: boolean; onClose: () => void }) {
  const t = useT(L);
  const c = useCommon();
  const [current, setCurrent] = useState('');
  const [pw, setPw] = useState('');
  const [pw2, setPw2] = useState('');
  const [tried, setTried] = useState(false);
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);
  const close = () => { setCurrent(''); setPw(''); setPw2(''); setTried(false); setErr(''); onClose(); };
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setTried(true);
    setErr('');
    if (!current || pw.length < 8 || pw !== pw2) return;
    setBusy(true);
    try {
      await api('PUT', `/api/v1/users/${enc(String(user.id))}/password`, { current, password: pw });
      notifyOk(t('changed'));
      close();
    } catch (e) {
      setErr(e instanceof ApiError && (e.status === 401 || e.status === 403) ? t('wrong') : e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal opened={opened} onClose={close} title={<Text fw={650}>{t('change_pw')}</Text>}>
      <form onSubmit={submit}>
        <Stack>
          <PasswordInput label={t('current')} value={current} onChange={(e) => setCurrent(e.currentTarget.value)} required data-autofocus
            autoComplete="current-password" error={err || undefined} />
          <PasswordInput label={t('new_pw')} description={t('hint')} value={pw} onChange={(e) => setPw(e.currentTarget.value)} required
            autoComplete="new-password" error={(tried || pw) && pw.length < 8 ? t('too_short') : undefined} />
          <PasswordInput label={t('new_pw2')} value={pw2} onChange={(e) => setPw2(e.currentTarget.value)} required
            autoComplete="new-password" error={tried && pw !== pw2 ? t('mismatch') : undefined} />
          <Group justify="flex-end" mt="xs">
            <Button variant="default" onClick={close}>{c('cancel')}</Button>
            <Button type="submit" loading={busy} leftSection={<IconKey size={16} />}>{c('save')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

/** AccountMenu is the header's user button: who is signed in, the password, signing out. */
export function AccountMenu({ user }: { user?: User }) {
  const t = useT(L);
  const [pwOpen, setPwOpen] = useState(false);
  const logout = async () => {
    await api('POST', '/api/v1/auth/logout').catch(() => {});
    queryClient.clear();
    queryClient.invalidateQueries({ queryKey: ['auth'] });
  };
  const label = t('account', user?.username ?? '');
  return (
    <>
      <Menu position="bottom-end" withinPortal shadow="md" width={220}>
        <Menu.Target>
          <Tooltip label={label}>
            <ActionIcon variant="default" size="lg" radius="md" aria-label={label}><IconUserCircle size={18} /></ActionIcon>
          </Tooltip>
        </Menu.Target>
        <Menu.Dropdown>
          {user && (
            <Menu.Label>
              <Text size="sm" fw={600} c="var(--mantine-color-text)" truncate>{user.username}</Text>
              <Text size="xs" c="dimmed">{t(user.role === 'admin' ? 'admin' : 'viewer')}</Text>
            </Menu.Label>
          )}
          {user && <Menu.Item leftSection={<IconKey size={15} />} onClick={() => setPwOpen(true)}>{t('change_pw')}</Menu.Item>}
          <Menu.Divider />
          <Menu.Item leftSection={<IconLogout size={15} />} onClick={logout}>{t('logout')}</Menu.Item>
        </Menu.Dropdown>
      </Menu>
      {user && <ChangeOwnPassword user={user} opened={pwOpen} onClose={() => setPwOpen(false)} />}
    </>
  );
}
