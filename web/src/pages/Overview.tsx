import { Anchor, Badge, Box, Card, Group, Skeleton, Stack, Text, UnstyledButton } from '@mantine/core';
import { IconAlertTriangle, IconArrowDownRight, IconArrowUpRight, IconCircleCheck, IconCircleX } from '@tabler/icons-react';
import type { ReactNode } from 'react';
import { Link } from 'react-router';
import { enc, isChecker, useOverview, useProbes, type Incident, type Overview as OverviewData } from '../api';
import { Section } from '../components/Cards';
import { PageHeader, ProbeName, QueryError } from '../components/Misc';
import { RemnawaveHero } from '../components/RemnawaveHero';
import { SystemsTable } from '../components/SystemsTable';
import { KindIcon, VerdictBadge } from '../components/Status';
import { bitrate, clock, duration, since } from '../format';
import { plural, useCommon, useLang, useT } from '../i18n';
import { downText, dropText, hostText, reasonText } from '../reason';
import { status } from '../theme';

const L = {
  ru: {
    title: 'Главная', updated: 'обновлено {0}',
    state: 'Состояние', all_good: 'Всё работает', no_incidents: 'Инцидентов нет', all_good_sub: 'открытых инцидентов нет',
    problems: '{0} {1}', critical_sub: 'критичных: {0}', incident_one: 'инцидент', incident_few: 'инцидента', incident_many: 'инцидентов',
    checks_ok: 'Проверки работают', bad_sub: 'со сбоем: {0}', partial: 'частично', blocked: 'блок', down: 'не работают', paused: 'на паузе',
    servers_online: 'Серверы онлайн', no_net: 'без интернета', offline: 'офлайн', traffic: 'Трафик сейчас',
    servers: 'Серверы', more: 'ещё {0} →', table: 'таблицей →', f_all: 'Все', f_bad: 'С проблемами',
    recent: 'Последние инциденты', no_recent: 'Инцидентов ещё не было',
    resolved: 'длился {0}', ongoing: 'идёт {0}',
    vs_yesterday: 'ко вчера',
  },
  en: {
    title: 'Dashboard', updated: 'updated {0}',
    state: 'Status', all_good: 'All systems up', no_incidents: 'No incidents', all_good_sub: 'no open incidents',
    problems: '{0} {1}', critical_sub: 'critical: {0}', incident_one: 'incident', incident_few: 'incidents', incident_many: 'incidents',
    checks_ok: 'Checks up', bad_sub: '{0} failing', partial: 'partial', blocked: 'blocked', down: 'down', paused: 'paused',
    servers_online: 'Servers online', no_net: 'no internet', offline: 'offline', traffic: 'Traffic now',
    servers: 'Servers', more: '{0} more →', table: 'as a table →', f_all: 'All', f_bad: 'With problems',
    recent: 'Recent incidents', no_recent: 'No incidents yet',
    resolved: 'lasted {0}', ongoing: 'ongoing {0}',
    vs_yesterday: 'vs yesterday',
  },
};

function incidentLink(i: Incident) {
  if (i.key.startsWith('probe:') || i.key.startsWith('probe-net:') || i.key.startsWith('host:')) return `/probes/${enc(i.key.split(':')[1])}`;
  if (i.key.startsWith('sub:')) return `/checks/${enc(i.key.slice(4))}`;
  if (i.key.startsWith('rw:')) return '/remnawave';
  if (i.key.startsWith('update:')) return '/incidents';
  return `/checks/${enc(i.key)}`;
}

const MAX_OPEN = 5;

function OpenIncident({ inc }: { inc: Incident }) {
  const { lang } = useLang();
  const t = useT(L);
  // A probe incident names the probe itself, so its only failing line would repeat the title.
  const failing = inc.key.startsWith('probe') || inc.host || inc.drop ? [] : (inc.lines ?? []).filter((l) => !l.ok);
  return (
    <Group
      className="uf-incident"
      justify="space-between"
      gap="xs"
      px="md"
      py={8}
      style={{ ['--uf-kind' as string]: status[(inc.kind as keyof typeof status)] ?? status.partial }}
    >
      <Group gap="sm" style={{ minWidth: 0 }}>
        <VerdictBadge kind={inc.kind} region={inc.region} />
        <Anchor component={Link} to={incidentLink(inc)} size="sm" fw={600} c="var(--mantine-color-text)" truncate>{inc.name}</Anchor>
        {inc.drop && (inc.kind !== 'node_down' || downText(inc, lang)) && (
          <Badge variant="default" radius="sm" size="md" styles={{ root: { textTransform: 'none', fontWeight: 500 } }}>
            <Text span size="xs" ff="monospace">{inc.kind === 'node_down' ? downText(inc, lang) : dropText(inc.drop, lang)}</Text>
          </Badge>
        )}
        {inc.host && (
          <Badge variant="default" radius="sm" size="md" styles={{ root: { textTransform: 'none', fontWeight: 500 } }}>
            <Text span size="xs" ff="monospace">{hostText(inc.host, lang)}</Text>
          </Badge>
        )}
        {failing.slice(0, 4).map((l) => (
          <Badge key={l.probe} variant="default" radius="sm" size="md" styles={{ root: { textTransform: 'none', fontWeight: 500 } }}>
            <ProbeName name={l.name} region={l.region} size="xs" />
            <Text span size="xs" c="dimmed"> · {reasonText(l.reason, l.error, lang)}</Text>
          </Badge>
        ))}
        {failing.length > 4 && <Text size="xs" c="dimmed">+{failing.length - 4}</Text>}
      </Group>
      <Text size="xs" c="dimmed" style={{ whiteSpace: 'nowrap' }}>{clock(inc.started)} · {t('ongoing', since(inc.started, lang))}</Text>
    </Group>
  );
}

function StripCell({ to, label, value, color, icon, sub }: { to: string; label: string; value: ReactNode; color?: string; icon?: ReactNode; sub?: ReactNode }) {
  return (
    <UnstyledButton component={Link} to={to} className="uf-strip-cell">
      {icon}
      <Box style={{ minWidth: 0 }}>
        <Text size="xs" c="dimmed" fw={500} truncate>{label}</Text>
        <Group gap={8} wrap="nowrap">
          <Text ff="monospace" fw={700} size="md" c={color} style={{ whiteSpace: 'nowrap' }}>{value}</Text>
          {sub && <Text size="xs" c="dimmed" truncate>{sub}</Text>}
        </Group>
      </Box>
    </UnstyledButton>
  );
}

/** Strip is the whole picture in one thin line, so the systems table gets the screen. */
function Strip({ data }: { data: OverviewData }) {
  const t = useT(L);
  const { lang } = useLang();
  const k = data.counts;
  const incidents = k.incidents ?? 0, critical = k.critical ?? 0;
  const color = incidents === 0 ? status.ok : critical > 0 ? status.down : status.partial;
  const word = lang === 'ru' ? plural(incidents, t('incident_one'), t('incident_few'), t('incident_many')) : incidents === 1 ? t('incident_one') : t('incident_many');
  const I = incidents === 0 ? IconCircleCheck : critical > 0 ? IconCircleX : IconAlertTriangle;
  // Servers are our infrastructure: a checker agent going offline is no news here.
  const servers = (data.probes ?? []).filter((p) => !isChecker(p));
  const online = servers.filter((p) => p.online).length;
  const offline = servers.length - online;
  const bad = (k.down ?? 0) + (k.blocked ?? 0) + (k.partial ?? 0);
  return (
    <Box className="uf-strip">
      <StripCell to="/incidents" label={t('state')} color={color} icon={<I size={22} color={color} style={{ flex: 'none' }} />}
        value={incidents > 0 ? t('problems', incidents, word) : t('all_good')} sub={incidents > 0 ? t('critical_sub', critical) : undefined} />
      <StripCell to="/checks" label={t('checks_ok')} value={`${k.ok ?? 0} / ${k.checks ?? 0}`} color={bad ? status.down : undefined}
        sub={bad ? t('bad_sub', bad) : (k.paused ? `${t('paused')} ${k.paused}` : undefined)} />
      <StripCell to="/" label={t('servers_online')} value={`${online} / ${servers.length}`} color={offline ? status.down : undefined}
        sub={offline ? `${t('offline')} ${offline}` : undefined} />
      <StripCell to="/" label={t('traffic')} value={bitrate(data.traffic.rx + data.traffic.tx)}
        sub={<Group gap={6} wrap="nowrap" component="span"><IconArrowDownRight size={12} />{bitrate(data.traffic.rx)}<IconArrowUpRight size={12} />{bitrate(data.traffic.tx)}</Group>} />
    </Box>
  );
}

export default function Overview() {
  const t = useT(L);
  const c = useCommon();
  const { lang } = useLang();
  const { data, isLoading, isError, error, refetch } = useOverview();
  const servers = useProbes();

  if (!data && isError) {
    return <Stack><PageHeader title={t('title')} /><QueryError error={error} onRetry={() => refetch()} /></Stack>;
  }
  if (isLoading || !data) {
    return (
      <Stack>
        <PageHeader title={t('title')} />
        <Skeleton h={84} radius="lg" />
        <Skeleton h={480} radius="lg" />
      </Stack>
    );
  }
  const rw = data.remnawave;

  const open = (data.incidents ?? []).filter((i) => !i.cause); // folded into their parent's card
  return (
    <>
    <Stack gap="md">
      {/* The panel's numbers lead the page: they are what the owner looks at first. */}
      {rw?.configured && <RemnawaveHero rw={rw} />}
      <Strip data={data} />

      {open.length > 0 && (
        <Card padding={0} style={{ overflow: 'hidden' }}>
          {open.slice(0, MAX_OPEN).map((i) => <OpenIncident key={i.id} inc={i} />)}
          {open.length > MAX_OPEN && (
            <Anchor component={Link} to="/incidents" size="sm" display="block" px="md" py={6}>{t('more', open.length - MAX_OPEN)}</Anchor>
          )}
        </Card>
      )}

      {servers.data && (
        <SystemsTable probes={servers.data.probes} hubURL={servers.data.hub_url} incidents={data.incidents} />
      )}

      <Section title={t('recent')}>
        <Card padding={0}>
          {data.recent.length === 0 ? (
            <Text c="dimmed" p="lg">{t('no_recent')}</Text>
          ) : (
            data.recent.map((i, n) => (
              <Group
                key={i.id}
                px="lg"
                py="sm"
                justify="space-between"
                wrap="nowrap"
                style={{ borderTop: n ? '1px solid var(--mantine-color-default-border)' : undefined }}
              >
                <Group gap="sm" wrap="nowrap" style={{ minWidth: 0 }}>
                  <KindIcon kind={i.resolved ? i.worst : i.kind} />
                  <Box style={{ minWidth: 0 }}>
                    <Anchor component={Link} to={incidentLink(i)} size="sm" fw={600} c="var(--mantine-color-text)" truncate display="block">{i.name}</Anchor>
                    <Text size="xs" c="dimmed" truncate>{c((i.resolved ? i.worst : i.kind) as 'ok')}{i.region ? ` · ${i.region.toUpperCase()}` : ''}{i.group ? ` · ${i.group}` : ''}</Text>
                  </Box>
                </Group>
                <Stack gap={0} align="flex-end" style={{ flex: 'none' }}>
                  <Text size="xs" ff="monospace">{clock(i.started, true)}</Text>
                  <Text size="xs" c="dimmed">
                    {i.resolved ? t('resolved', duration((Date.parse(i.resolved) - Date.parse(i.started)) / 1000, lang)) : t('ongoing', since(i.started, lang))}
                  </Text>
                </Stack>
              </Group>
            ))
          )}
        </Card>
      </Section>
    </Stack>
    </>
  );
}

