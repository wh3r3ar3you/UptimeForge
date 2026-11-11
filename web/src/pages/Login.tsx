import { Alert, Box, Button, Card, Center, PasswordInput, SegmentedControl, Stack, Text, TextInput, Title } from '@mantine/core';
import { IconAlertCircle } from '@tabler/icons-react';
import { useState, type FormEvent } from 'react';
import { api, ApiError, queryClient } from '../api';
import { useLang, useT } from '../i18n';

const L = {
  ru: {
    login: 'Вход', setup: 'Первый запуск', setup_hint: 'Создайте администратора. Остальные пользователи добавляются в настройках.',
    username: 'Логин', password: 'Пароль', password_hint: 'Не короче 8 символов', submit_login: 'Войти', submit_setup: 'Создать и войти',
    tagline: 'Мониторинг VPN и прокси-инфраструктуры',
    setup_token: 'Токен первого запуска', setup_token_hint: 'Напечатан в логе хаба при первом старте, или задайте UF_SETUP_TOKEN',
    bad_token: 'Неверный или отсутствующий токен первого запуска',
  },
  en: {
    login: 'Sign in', setup: 'First run', setup_hint: 'Create the administrator. More users can be added in settings.',
    username: 'Username', password: 'Password', password_hint: 'At least 8 characters', submit_login: 'Sign in', submit_setup: 'Create and sign in',
    tagline: 'Monitoring for VPN and proxy infrastructure',
    setup_token: 'Setup token', setup_token_hint: 'Printed in the hub log on first start, or set UF_SETUP_TOKEN',
    bad_token: 'Wrong or missing setup token',
  },
};

export function Login({ setup }: { setup: boolean; from?: string }) {
  const t = useT(L);
  const { lang, setLang } = useLang();
  const [username, setUsername] = useState(setup ? 'admin' : '');
  const [password, setPassword] = useState('');
  const [token, setToken] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      // First run needs the one-time token from the hub log, so whoever finds
      // a fresh hub on the network cannot claim it.
      if (setup) await api('POST', '/api/v1/auth/setup', { username, password, setup_token: token.trim() });
      else await api('POST', '/api/v1/auth/login', { username, password });
      await queryClient.invalidateQueries();
    } catch (err) {
      setError(setup && err instanceof ApiError && err.status === 403 ? t('bad_token') : err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Center mih="100vh" p="md">
      <Stack w={400} maw="100%" gap="lg">
        <Stack gap={6} align="center">
          <svg width="56" height="56" viewBox="0 0 64 64" aria-hidden>
            <defs>
              <linearGradient id="ufl" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stopColor="#2ee6c4" /><stop offset="1" stopColor="#2a8cf0" /></linearGradient>
            </defs>
            <rect width="64" height="64" rx="16" fill="rgba(46,230,196,0.08)" stroke="rgba(46,230,196,0.35)" />
            <path d="M10 36h10l5-12 8 22 6-16 4 6h11" fill="none" stroke="url(#ufl)" strokeWidth="5" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
          <Text className="uf-logo" fz={28}>UptimeForge</Text>
          <Text c="dimmed" size="sm">{t('tagline')}</Text>
        </Stack>
        <Card padding="xl">
          <form onSubmit={submit}>
            <Stack>
              <Title order={3}>{setup ? t('setup') : t('login')}</Title>
              {setup && <Text size="sm" c="dimmed">{t('setup_hint')}</Text>}
              {error && <Alert color="red" icon={<IconAlertCircle size={16} />}>{error}</Alert>}
              {setup && (
                <PasswordInput label={t('setup_token')} description={t('setup_token_hint')} value={token} onChange={(e) => setToken(e.currentTarget.value)}
                  autoComplete="off" required autoFocus styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)' } }} />
              )}
              <TextInput label={t('username')} value={username} onChange={(e) => setUsername(e.currentTarget.value)} autoComplete="username" required autoFocus={!setup} />
              <PasswordInput
                label={t('password')}
                description={setup ? t('password_hint') : undefined}
                value={password}
                onChange={(e) => setPassword(e.currentTarget.value)}
                autoComplete={setup ? 'new-password' : 'current-password'}
                required
              />
              <Button type="submit" loading={busy} size="md" variant="gradient" gradient={{ from: '#20cfae', to: '#2a8cf0', deg: 90 }}>
                {setup ? t('submit_setup') : t('submit_login')}
              </Button>
            </Stack>
          </form>
        </Card>
        <Box ta="center">
          <SegmentedControl size="xs" value={lang} onChange={(v) => setLang(v as 'ru' | 'en')} data={[{ label: 'RU', value: 'ru' }, { label: 'EN', value: 'en' }]} />
        </Box>
      </Stack>
    </Center>
  );
}
