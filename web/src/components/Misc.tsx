import { ActionIcon, Alert, Box, Button, Code, CopyButton, Group, Stack, Text, Title, Tooltip } from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconAlertCircle, IconCheck, IconCopy, IconRefresh } from '@tabler/icons-react';
import type { KeyboardEvent, ReactNode } from 'react';
import { ApiError } from '../api';
import { flag } from '../format';
import { common, useCommon, useT, type Lang } from '../i18n';

const L = {
  ru: { load_failed: 'Не удалось загрузить данные', retry: 'Повторить', http: 'Хаб ответил {0}', network: 'Хаб не отвечает' },
  en: { load_failed: 'Could not load the data', retry: 'Retry', http: 'The hub answered {0}', network: 'The hub does not answer' },
};

export function PageHeader({ title, subtitle, right }: { title: ReactNode; subtitle?: ReactNode; right?: ReactNode }) {
  return (
    <Group justify="space-between" align="flex-start" mb="lg" gap="md">
      <Stack gap={4} style={{ minWidth: 0 }}>
        <Title order={2} fw={700} fz={{ base: 22, sm: 26 }}>{title}</Title>
        {subtitle && <Text c="dimmed" size="sm" component="div">{subtitle}</Text>}
      </Stack>
      {right && <Group gap="sm">{right}</Group>}
    </Group>
  );
}

export function CopyBlock({ value, label }: { value: string; label?: string }) {
  const c = useCommon();
  return (
    <Stack gap={4}>
      {label && <Text size="xs" c="dimmed" fw={600} tt="uppercase">{label}</Text>}
      <Group gap={0} wrap="nowrap" align="stretch" className="uf-copy">
        <Code block style={{ flex: 1, whiteSpace: 'pre-wrap', wordBreak: 'break-all', fontSize: 12, background: 'transparent' }}>{value}</Code>
        <CopyButton value={value} timeout={1500}>
          {({ copied, copy }) => (
            <Tooltip label={copied ? c('copied') : c('copy')}>
              <ActionIcon variant="subtle" color={copied ? 'green' : 'gray'} onClick={copy} m={6} size="lg" aria-label={c('copy')}>
                {copied ? <IconCheck size={16} /> : <IconCopy size={16} />}
              </ActionIcon>
            </Tooltip>
          )}
        </CopyButton>
      </Group>
    </Stack>
  );
}

export function ProbeName({ name, region, size = 'sm' }: { name: string; region?: string; size?: 'xs' | 'sm' | 'md' }) {
  return (
    <Text size={size} fw={500} truncate component="span">
      {region && <Box component="span" mr={6}>{flag(region) || '📍'}</Box>}
      {name}
    </Text>
  );
}

/**
 * QueryError says a request failed, with a Retry, where a page would
 * otherwise show an empty state ("no checks yet") or a skeleton forever.
 */
export function QueryError({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  const t = useT(L);
  const detail = error instanceof ApiError ? `${t('http', error.status)}: ${error.message}` : error instanceof TypeError ? t('network') : String(error ?? '');
  return (
    <Alert color="red" variant="light" icon={<IconAlertCircle size={18} />} title={t('load_failed')}>
      <Group justify="space-between" gap="sm" wrap="wrap">
        <Text size="sm" style={{ wordBreak: 'break-word' }}>{detail}</Text>
        <Button size="xs" variant="default" leftSection={<IconRefresh size={14} />} onClick={onRetry}>{t('retry')}</Button>
      </Group>
    </Alert>
  );
}

/**
 * clickableRow makes a table row that opens a page work from the keyboard
 * too: focusable, Enter or Space opens it. Keys inside its own controls stay theirs.
 */
export function clickableRow(open: () => void) {
  return {
    'data-clickable': true,
    tabIndex: 0,
    onClick: open,
    onKeyDown: (e: KeyboardEvent<HTMLElement>) => {
      if (e.target !== e.currentTarget || (e.key !== 'Enter' && e.key !== ' ')) return;
      e.preventDefault();
      open();
    },
  };
}

/** The UI language for toasts raised outside React (mutation callbacks): the one the user picked. */
function currentLang(): Lang {
  return document.documentElement.lang === 'ru' ? 'ru' : 'en';
}

export function notifyError(e: unknown) {
  notifications.show({ color: 'red', title: common[currentLang()].error, message: e instanceof ApiError ? e.message : String(e) });
}

export function notifyOk(message: string) {
  notifications.show({ color: 'forge', message, icon: <IconCheck size={16} /> });
}
