import {
  ActionIcon, AppShell, Badge, Box, Burger, Group, NavLink, ScrollArea, SegmentedControl, Text, Tooltip,
} from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import {
  IconActivityHeartbeat, IconAlertTriangle, IconBell, IconTemplate, IconLayoutDashboard, IconPalette,
  IconSettings, IconShieldCheck, IconUsers, IconWorldWww, IconFlask2, IconAffiliate,
} from '@tabler/icons-react';
import { useEffect, useId, type ComponentType, type ReactNode } from 'react';
import { NavLink as RouterLink, useLocation } from 'react-router';
import { useMeta, useOverviewCounts } from '../api';
import { useLang, useT } from '../i18n';
import { AccountMenu } from './AccountMenu';
import { TelegramLogo } from './TelegramLogo';
import { ThemePicker } from './ThemePicker';

const L = {
  ru: {
    overview: 'Обзор', home: 'Главная', monitoring: 'Мониторинг', checks: 'Проверки', probes: 'Серверы и пробы', incidents: 'Инциденты',
    tools: 'Инструменты', checker: 'Проверка конфигов', remnawave: 'Remnawave',
    settings: 'Настройки', status_pages: 'Статус-страницы', users: 'Пользователи', general: 'Общие',
    theme: 'Внешний вид', alerts: 'Уведомления', rules: 'Правила', templates: 'Шаблоны',
    open_menu: 'Открыть меню', close_menu: 'Закрыть меню',
  },
  en: {
    overview: 'Overview', home: 'Dashboard', monitoring: 'Monitoring', checks: 'Checks', probes: 'Servers & probes', incidents: 'Incidents',
    tools: 'Tools', checker: 'Config checker', remnawave: 'Remnawave',
    settings: 'Settings', status_pages: 'Status pages', users: 'Users', general: 'General',
    theme: 'Appearance', alerts: 'Notifications', rules: 'Rules', templates: 'Templates',
    open_menu: 'Open the menu', close_menu: 'Close the menu',
  },
};

function Logo() {
  const gradient = useId(); // rendered twice (header, navbar): a shared id would point at the hidden copy
  return (
    <Group gap={10} wrap="nowrap">
      <svg width="30" height="30" viewBox="0 0 64 64" aria-hidden>
        <defs>
          <linearGradient id={gradient} x1="0" y1="0" x2="1" y2="1"><stop offset="0" stopColor="#2ee6c4" /><stop offset="1" stopColor="#2a8cf0" /></linearGradient>
        </defs>
        <rect width="64" height="64" rx="16" fill="rgba(46,230,196,0.08)" stroke="rgba(46,230,196,0.35)" />
        <path d="M10 36h10l5-12 8 22 6-16 4 6h11" fill="none" stroke={`url(#${gradient})`} strokeWidth="5" strokeLinecap="round" strokeLinejoin="round" />
      </svg>
      <Text className="uf-logo">UptimeForge</Text>
    </Group>
  );
}

function Item({ to, icon: I, label, badge, onClick }: { to: string; icon: ComponentType<{ size?: number; stroke?: number }>; label: string; badge?: ReactNode; onClick: () => void }) {
  const loc = useLocation();
  const active = to === '/' ? loc.pathname === '/' : loc.pathname.startsWith(to);
  return (
    <NavLink
      component={RouterLink}
      to={to}
      onClick={onClick}
      active={active}
      className="uf-nav-link"
      label={label}
      leftSection={<I size={19} stroke={1.6} />}
      rightSection={badge}
      variant="subtle"
    />
  );
}

export function Layout({ children }: { children: ReactNode }) {
  const t = useT(L);
  const [opened, { toggle, close }] = useDisclosure();
  const [themesOpen, themes] = useDisclosure(false);
  const { lang, setLang } = useLang();
  const meta = useMeta();
  const counts = useOverviewCounts();
  const openIncidents = counts.data?.incidents ?? 0;
  const critical = counts.data?.critical ?? 0;
  const isAdmin = meta.data?.user.role === 'admin';

  useEffect(() => {
    document.title = openIncidents > 0 ? `(${openIncidents}) UptimeForge` : 'UptimeForge';
  }, [openIncidents]);

  return (
    <AppShell
      layout="alt"
      header={{ height: 64 }}
      navbar={{ width: 272, breakpoint: 'md', collapsed: { mobile: !opened } }}
      padding={{ base: 'md', md: 'xl' }}
    >
      <AppShell.Header withBorder={false} style={{ background: 'transparent', backdropFilter: 'blur(10px)' }}>
        <Group h="100%" px="md" justify="space-between">
          <Group gap="sm">
            <Burger opened={opened} onClick={toggle} hiddenFrom="md" size="sm" aria-label={t(opened ? 'close_menu' : 'open_menu')} />
            <Box hiddenFrom="md"><Logo /></Box>
          </Group>
          <Group gap="xs">
            {meta.data && (
              <Badge variant="light" color="forge" size="lg" radius="md" leftSection={<IconActivityHeartbeat size={14} />} visibleFrom="sm">
                {meta.data.version}
              </Badge>
            )}
            <SegmentedControl visibleFrom="xs" size="xs" radius="md" value={lang} onChange={(v) => setLang(v as 'ru' | 'en')} data={[{ label: 'RU', value: 'ru' }, { label: 'EN', value: 'en' }]} />
            <Tooltip label={t('theme')}>
              <ActionIcon variant="default" size="lg" radius="md" onClick={themes.open} aria-label={t('theme')}>
                <IconPalette size={18} />
              </ActionIcon>
            </Tooltip>
            <AccountMenu user={meta.data?.user} />
          </Group>
        </Group>
      </AppShell.Header>

      <AppShell.Navbar p="md" style={{ background: 'var(--uf-nav)', borderColor: 'var(--uf-border)' }}>
        <Box px={8} pb="sm" visibleFrom="md"><Logo /></Box>
        <ScrollArea type="never" style={{ flex: 1 }}>
          <div className="uf-nav-section">{t('overview')}</div>
          <Item to="/" icon={IconLayoutDashboard} label={t('home')} onClick={close} />
          <div className="uf-nav-section">{t('monitoring')}</div>
          <Item to="/checks" icon={IconShieldCheck} label={t('checks')} onClick={close} />
          <Item to="/telegram-proxy" icon={TelegramLogo} label="Telegram Proxy" onClick={close} />
          <Item
            to="/incidents"
            icon={IconAlertTriangle}
            label={t('incidents')}
            onClick={close}
            badge={openIncidents > 0 ? (
              <Badge size="sm" circle color={critical > 0 ? 'red' : 'yellow'} className={critical > 0 ? 'uf-pulse' : undefined}
                style={{ ['--uf-pulse' as string]: 'var(--mantine-color-red-6)', overflow: 'visible' }}>{openIncidents}</Badge>
            ) : undefined}
          />
          <div className="uf-nav-section">{t('tools')}</div>
          {isAdmin && <Item to="/tools" icon={IconFlask2} label={t('checker')} onClick={close} />}
          <Item to="/remnawave" icon={IconAffiliate} label={t('remnawave')} onClick={close} />
          {isAdmin && (
            <>
              <div className="uf-nav-section">{t('alerts')}</div>
              <Item to="/settings/telegram" icon={TelegramLogo} label="Telegram" onClick={close} />
              <Item to="/settings/notifications" icon={IconBell} label={t('rules')} onClick={close} />
              <Item to="/settings/templates" icon={IconTemplate} label={t('templates')} onClick={close} />
              <div className="uf-nav-section">{t('settings')}</div>
              <Item to="/settings/remnawave" icon={IconAffiliate} label={t('remnawave')} onClick={close} />
              <Item to="/settings/status-pages" icon={IconWorldWww} label={t('status_pages')} onClick={close} />
              <Item to="/settings/users" icon={IconUsers} label={t('users')} onClick={close} />
              <Item to="/settings/general" icon={IconSettings} label={t('general')} onClick={close} />
            </>
          )}
        </ScrollArea>
        <Group px={8} pt="sm" justify="space-between">
          <Text size="xs" c="dimmed">UptimeForge · {meta.data?.version}</Text>
          <SegmentedControl hiddenFrom="xs" size="xs" radius="md" value={lang} onChange={(v) => setLang(v as 'ru' | 'en')} data={[{ label: 'RU', value: 'ru' }, { label: 'EN', value: 'en' }]} />
        </Group>
      </AppShell.Navbar>

      <ThemePicker opened={themesOpen} onClose={themes.close} />
      <AppShell.Main>
        <Box maw={1500} mx="auto">{children}</Box>
      </AppShell.Main>
    </AppShell>
  );
}
