import { Box, Card, Center, Collapse, Divider, Group, SegmentedControl, Skeleton, Stack, Text, Title, UnstyledButton } from '@mantine/core';
import { useColorScheme, useMediaQuery } from '@mantine/hooks';
import {
  IconAlertTriangleFilled, IconChevronDown, IconCircleCheckFilled, IconCircleXFilled, IconHelpCircleFilled, IconWorldOff,
} from '@tabler/icons-react';
import { useQuery } from '@tanstack/react-query';
import dayjs from 'dayjs';
import { useEffect, useLayoutEffect, useMemo, useState, type ReactNode } from 'react';
import { useParams } from 'react-router';
import { api, ApiError, enc, type UptimeDay } from '../api';
import { UptimeBars } from '../components/UptimeBars';
import { useLang, useT } from '../i18n';
import { status as colors } from '../theme';

type State = 'operational' | 'degraded' | 'outage' | 'unknown';

interface PublicItem { name: string; status: State; uptime_90d?: number; days: UptimeDay[] | null }
interface PublicNode { name?: string; country: string; status: State }
interface PublicLocation { flag: string; country: string; name?: string; status: State; uptime_90d?: number; days?: UptimeDay[]; nodes?: PublicNode[] }
interface PublicPage {
  title: string; description?: string; status: State; updated: string;
  groups?: { name: string; items: PublicItem[] | null }[] | null;
  kind?: 'locations'; mode?: 'country' | 'node' | 'country_expand'; locations?: PublicLocation[];
}

const L = {
  ru: {
    all_ok: 'Все системы работают', degraded_all: 'Частичные неполадки', outage_all: 'Серьёзный сбой', unknown_all: 'Состояние неизвестно',
    operational: 'Работает', degraded: 'Частичный сбой', outage: 'Не работает', unknown: 'Нет данных',
    updated: 'Обновлено {0}', not_found: 'Страница не найдена', not_found_hint: 'Проверьте адрес — возможно, страницу удалили или переименовали.',
    error: 'Не удалось загрузить статус', powered: 'Работает на', empty: 'Здесь пока ничего не отслеживается',
    other: 'Другое',
    banner_ok: 'Все сервисы доступны и работают штатно.',
    banner_degraded: 'Часть сервисов работает с перебоями — подробности ниже.',
    banner_outage: 'Некоторые сервисы недоступны — подробности ниже.',
  },
  en: {
    all_ok: 'All systems operational', degraded_all: 'Partial outage', outage_all: 'Major outage', unknown_all: 'Status unknown',
    operational: 'Operational', degraded: 'Degraded', outage: 'Outage', unknown: 'No data',
    updated: 'Updated {0}', not_found: 'Page not found', not_found_hint: 'Check the address — the page may have been removed or renamed.',
    error: 'Could not load the status', powered: 'Powered by', empty: 'Nothing is tracked here yet',
    other: 'Other',
    banner_ok: 'Every service is available and working normally.',
    banner_degraded: 'Some services are having trouble — details below.',
    banner_outage: 'Some services are unavailable — details below.',
  },
};

// A group is as bad as its worst item; "no data" only wins when nothing else is known.
const rank: Record<State, number> = { outage: 3, degraded: 2, operational: 1, unknown: 0 };

function look(s: State) {
  switch (s) {
    case 'operational': return { color: colors.ok, icon: IconCircleCheckFilled, ink: '#fff' };
    case 'degraded': return { color: colors.partial, icon: IconAlertTriangleFilled, ink: '#1d1600' };
    case 'outage': return { color: colors.down, icon: IconCircleXFilled, ink: '#fff' };
  }
  return { color: colors.unknown, icon: IconHelpCircleFilled, ink: '#fff' };
}

/** Public visitors get their system colour scheme, not the admin's dark default. */
function useSystemScheme() {
  const sys = useColorScheme('dark', { getInitialValueInEffect: false });
  useLayoutEffect(() => {
    const el = document.documentElement;
    const prev = el.getAttribute('data-mantine-color-scheme');
    el.setAttribute('data-mantine-color-scheme', sys);
    return () => {
      if (prev) el.setAttribute('data-mantine-color-scheme', prev);
    };
  }, [sys]);
  return sys;
}

function Logo({ size = 18 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 64 64" aria-hidden>
      <defs>
        <linearGradient id="ufps" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stopColor="#2ee6c4" /><stop offset="1" stopColor="#2a8cf0" /></linearGradient>
      </defs>
      <rect width="64" height="64" rx="16" fill="rgba(46,230,196,0.08)" stroke="rgba(46,230,196,0.35)" />
      <path d="M10 36h10l5-12 8 22 6-16 4 6h11" fill="none" stroke="url(#ufps)" strokeWidth="5" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

function StateLabel({ s, size = 'sm' }: { s: State; size?: 'sm' | 'md' }) {
  const t = useT(L);
  const l = look(s);
  const I = l.icon;
  return (
    <Group gap={6} wrap="nowrap" style={{ flex: 'none' }}>
      <I size={size === 'md' ? 18 : 16} color={l.color} />
      <Text size={size} fw={600}>{t(s)}</Text>
    </Group>
  );
}

function Footer() {
  const t = useT(L);
  const { lang, setLang } = useLang();
  return (
    <Group justify="space-between" py="lg" gap="sm">
      <Group gap={8} wrap="nowrap">
        <Text size="sm" c="dimmed">{t('powered')}</Text>
        <Logo />
        <Text size="sm" fw={700} className="uf-logo" fz={14}>UptimeForge</Text>
      </Group>
      <SegmentedControl size="xs" value={lang} onChange={(v) => setLang(v as 'ru' | 'en')} data={[{ label: 'RU', value: 'ru' }, { label: 'EN', value: 'en' }]} />
    </Group>
  );
}

/** names gives each row its client name, or its country's; a name shared by
 *  several rows gets a number. */
function useNames(rows: { country: string; name?: string }[]) {
  const t = useT(L);
  const { lang } = useLang();
  return useMemo(() => {
    const regions = new Intl.DisplayNames([lang], { type: 'region' });
    // Intl.DisplayNames throws on a malformed code: a bad country falls back to itself, not a blank public page.
    const country = (cc: string) => {
      try {
        return regions.of(cc.toUpperCase()) ?? cc;
      } catch {
        return cc;
      }
    };
    const base = rows.map((r) => r.name || (r.country ? country(r.country) : t('other')));
    const total = new Map<string, number>();
    for (const b of base) total.set(b, (total.get(b) ?? 0) + 1);
    const seen = new Map<string, number>();
    return base.map((b) => {
      if ((total.get(b) ?? 0) < 2) return b;
      const n = (seen.get(b) ?? 0) + 1;
      seen.set(b, n);
      return `${b} ${n}`;
    });
  }, [rows, lang, t]);
}

function LocationRow({ l, name, phone, first, expandable }: { l: PublicLocation; name: string; phone: boolean; first: boolean; expandable: boolean }) {
  const [open, setOpen] = useState(false);
  const nodes = l.nodes ?? [];
  const names = useNames(nodes);
  const canOpen = expandable && nodes.length > 0;
  const head = (
    <Group justify="space-between" gap="sm" wrap="nowrap">
      <Group gap="md" wrap="nowrap" style={{ minWidth: 0 }}>
        <Text fz={{ base: 30, sm: 36 }} lh={1} style={{ flex: 'none' }} aria-hidden>{l.flag || '🏳️'}</Text>
        <Stack gap={0} style={{ minWidth: 0 }}>
          <Text fw={650} fz={{ base: 'md', sm: 'lg' }} style={{ wordBreak: 'break-word' }}>{name}</Text>
        </Stack>
      </Group>
      <Group gap={6} wrap="nowrap" style={{ flex: 'none' }}>
        <StateLabel s={l.status} />
        {canOpen && <IconChevronDown size={18} style={{ transition: 'transform 150ms', transform: open ? 'rotate(180deg)' : undefined, opacity: 0.6 }} />}
      </Group>
    </Group>
  );
  return (
    <Box px={{ base: 'md', sm: 'lg' }} py="md" style={{ borderTop: first ? undefined : '1px solid var(--mantine-color-default-border)' }}>
      <Stack gap="xs">
        {canOpen ? <UnstyledButton onClick={() => setOpen((o) => !o)} aria-expanded={open}>{head}</UnstyledButton> : head}
        {l.days && <UptimeBars days={phone ? l.days.slice(-30) : l.days} height={30} />}
        {canOpen && (
          <Collapse in={open}>
            <Stack gap={6} pl={{ base: 4, sm: 52 }} pt={4}>
              {nodes.map((n, i) => (
                <Group key={i} justify="space-between" gap="sm" wrap="nowrap">
                  <Text size="sm" style={{ minWidth: 0, wordBreak: 'break-word' }}>{names[i]}</Text>
                  <StateLabel s={n.status} />
                </Group>
              ))}
            </Stack>
          </Collapse>
        )}
      </Stack>
    </Box>
  );
}

function LocationList({ rows, mode, phone }: { rows: PublicLocation[]; mode?: PublicPage['mode']; phone: boolean }) {
  const t = useT(L);
  const names = useNames(rows);
  if (rows.length === 0) return <Card padding="xl"><Text c="dimmed" ta="center">{t('empty')}</Text></Card>;
  return (
    <Card padding={0} radius="lg">
      {rows.map((l, i) => <LocationRow key={i} l={l} name={names[i]} phone={phone} first={i === 0} expandable={mode === 'country_expand'} />)}
    </Card>
  );
}

export default function PublicStatus() {
  const t = useT(L);
  const { slug = '' } = useParams();
  useSystemScheme();
  // 90 bars are too thin on a phone: show the last 30 days there, like statuspage does.
  const phone = useMediaQuery('(max-width: 36em)', false, { getInitialValueInEffect: false });
  const q = useQuery({
    queryKey: ['public-status', slug],
    queryFn: () => api<PublicPage>('GET', `/api/public/status/${enc(slug)}`),
    refetchInterval: 60_000,
    retry: (n, e) => !(e instanceof ApiError && e.status === 404) && n < 2,
  });
  const p = q.data;

  useEffect(() => {
    if (p?.title) document.title = p.title;
    return () => { document.title = 'UptimeForge'; };
  }, [p?.title]);

  const shell = (children: ReactNode) => (
    <Box mih="100vh" px="md" py={{ base: 32, sm: 56 }}>
      <Stack maw={860} mx="auto" gap="xl">{children}</Stack>
    </Box>
  );

  if (q.isLoading) {
    return shell(
      <>
        <Skeleton h={36} w="50%" />
        <Skeleton h={88} radius="lg" />
        <Skeleton h={260} radius="lg" />
      </>,
    );
  }
  if (q.isError || !p) {
    const nf = q.error instanceof ApiError && q.error.status === 404;
    return shell(
      <>
        <Center mih="60vh">
          <Stack align="center" gap="sm">
            <Box className="uf-icon-tile" style={{ ['--tint' as string]: colors.unknown, width: 56, height: 56 }}>
              <IconWorldOff size={28} stroke={1.5} />
            </Box>
            <Title order={3}>{nf ? t('not_found') : t('error')}</Title>
            <Text c="dimmed" size="sm" ta="center" maw={380}>{nf ? t('not_found_hint') : q.error?.message}</Text>
          </Stack>
        </Center>
        <Footer />
      </>,
    );
  }

  const overall = look(p.status);
  const OverallIcon = overall.icon;
  const headline = { operational: t('all_ok'), degraded: t('degraded_all'), outage: t('outage_all'), unknown: t('unknown_all') }[p.status] ?? t('unknown_all');
  const groups = (p.groups ?? []).filter((g) => g.items?.length);

  return shell(
    <>
      <Stack gap={6}>
        <Title order={1} fz={{ base: 26, sm: 32 }} fw={750} style={{ letterSpacing: '-0.01em' }}>{p.title}</Title>
        {p.description && <Text c="dimmed" size="md" style={{ whiteSpace: 'pre-line' }}>{p.description}</Text>}
      </Stack>

      <Box
        p={{ base: 'md', sm: 'lg' }}
        style={{
          borderRadius: 'var(--mantine-radius-lg)', background: overall.color, color: overall.ink,
          boxShadow: `0 10px 40px -18px ${overall.color}`,
        }}
      >
        <Group gap="md" wrap="nowrap" align="center">
          <OverallIcon size={34} style={{ flex: 'none' }} />
          <Stack gap={2} style={{ minWidth: 0 }}>
            <Text fw={700} fz={{ base: 18, sm: 22 }} lh={1.25} c={overall.ink}>{headline}</Text>
            <Text size="sm" c={overall.ink} style={{ opacity: 0.85 }}>
              {p.status === 'operational' ? t('banner_ok') : p.status === 'degraded' ? t('banner_degraded') : p.status === 'outage' ? t('banner_outage') : ''}
            </Text>
          </Stack>
        </Group>
      </Box>

      {p.kind === 'locations' ? (
        <LocationList rows={p.locations ?? []} mode={p.mode} phone={phone} />
      ) : groups.length === 0 ? (
        <Card padding="xl"><Text c="dimmed" ta="center">{t('empty')}</Text></Card>
      ) : (
        <Stack gap="lg">
          {groups.map((g, gi) => {
            const items = g.items ?? [];
            const worst = items.reduce<State>((w, it) => (rank[it.status] > rank[w] ? it.status : w), 'unknown');
            return (
              <Card key={gi} padding={0} radius="lg">
                {g.name && (
                  <>
                    <Group justify="space-between" px={{ base: 'md', sm: 'lg' }} py="md" gap="sm" wrap="nowrap">
                      <Text fw={700} fz="lg" style={{ minWidth: 0 }} truncate>{g.name}</Text>
                      <StateLabel s={worst} />
                    </Group>
                    <Divider />
                  </>
                )}
                <Stack gap={0}>
                  {items.map((it, i) => (
                    <Box key={i} px={{ base: 'md', sm: 'lg' }} py="md" style={{ borderTop: i ? '1px solid var(--mantine-color-default-border)' : undefined }}>
                      <Stack gap="xs">
                        <Group justify="space-between" gap="sm" wrap="nowrap">
                          <Text fw={600} style={{ minWidth: 0, wordBreak: 'break-word' }}>{it.name}</Text>
                          <StateLabel s={it.status} />
                        </Group>
                        <UptimeBars days={phone ? (it.days ?? []).slice(-30) : it.days ?? []} height={30} />
                      </Stack>
                    </Box>
                  ))}
                </Stack>
              </Card>
            );
          })}
        </Stack>
      )}

      <Stack gap={0}>
        <Text size="sm" c="dimmed" ta="right">{t('updated', dayjs(p.updated).format('DD.MM.YYYY HH:mm'))}</Text>
        <Divider mt="md" />
        <Footer />
      </Stack>
    </>,
  );
}
