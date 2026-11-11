import { Alert, Button, Center, Code, Group, Stack, Text } from '@mantine/core';
import { IconAlertTriangle, IconRefresh } from '@tabler/icons-react';
import { Component, useEffect, type ErrorInfo, type ReactNode } from 'react';
import { useRouteError } from 'react-router';
import { useT } from '../i18n';

const L = {
  ru: {
    title: 'Эта страница сломалась', hint: 'Скорее всего, интерфейс обновился, а вкладка открыта со старой версией. Перезагрузите страницу.',
    reload: 'Перезагрузить', retry: 'Попробовать ещё раз', details: 'Подробности',
  },
  en: {
    title: 'This page broke', hint: 'Most likely the hub was updated while this tab ran an older version. Reload the page.',
    reload: 'Reload', retry: 'Try again', details: 'Details',
  },
};

const RELOADED = 'uf-chunk-reload';

/** isChunkError tells a failed lazy import (the hub was updated under an open tab) from a bug. */
export function isChunkError(e: unknown): boolean {
  const msg = e instanceof Error ? `${e.name} ${e.message}` : String(e);
  return /dynamically imported module|Importing a module script failed|error loading dynamically imported|ChunkLoadError|Unable to preload CSS|Failed to fetch dynamically/i.test(msg);
}

/**
 * reloadForNewVersion reloads the page once to pick up the new build. The
 * time of the last try lives in sessionStorage, so a chunk that is really
 * missing shows the error instead of reloading in a loop.
 */
export function reloadForNewVersion(): boolean {
  try {
    const last = Number(sessionStorage.getItem(RELOADED) ?? 0);
    if (Date.now() - last < 30_000) return false;
    sessionStorage.setItem(RELOADED, String(Date.now()));
  } catch {
    return false; // no storage, no loop guard: let the user press the button
  }
  window.location.reload();
  return true;
}

function Fallback({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  const t = useT(L);
  const msg = error instanceof Error ? error.message : String(error);
  return (
    <Center mih="50vh" p="md">
      <Alert color="red" variant="light" icon={<IconAlertTriangle size={20} />} title={t('title')} maw={560} w="100%">
        <Stack gap="sm">
          <Text size="sm">{t('hint')}</Text>
          <Group gap="xs">
            <Button size="xs" leftSection={<IconRefresh size={14} />} onClick={() => window.location.reload()}>{t('reload')}</Button>
            <Button size="xs" variant="default" onClick={onRetry}>{t('retry')}</Button>
          </Group>
          {msg && (
            <details>
              <summary><Text span size="xs" c="dimmed">{t('details')}</Text></summary>
              <Code block mt={6} fz={11} style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>{msg}</Code>
            </details>
          )}
        </Stack>
      </Alert>
    </Center>
  );
}

/**
 * ErrorBoundary keeps a render error inside the part of the page that threw:
 * the layout and the navigation stay. A failed chunk load reloads the page
 * once, since it means a new build is out.
 */
export class ErrorBoundary extends Component<{ children: ReactNode }, { error: unknown }> {
  state: { error: unknown } = { error: null };

  static getDerivedStateFromError(error: unknown) {
    return { error };
  }

  componentDidCatch(error: unknown, info: ErrorInfo) {
    if (isChunkError(error) && reloadForNewVersion()) return;
    console.error(error, info.componentStack);
  }

  render() {
    if (this.state.error != null) return <Fallback error={this.state.error} onRetry={() => this.setState({ error: null })} />;
    return this.props.children;
  }
}

/** RouteError is the router's own last line, for anything thrown outside the boundaries. */
export function RouteError() {
  const error = useRouteError();
  useEffect(() => {
    if (isChunkError(error)) reloadForNewVersion();
  }, [error]);
  return <Fallback error={error} onRetry={() => window.location.reload()} />;
}
