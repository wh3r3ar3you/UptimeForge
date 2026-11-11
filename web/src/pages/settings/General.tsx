import {
  Alert, Badge, Group, NumberInput, SegmentedControl, Select, SimpleGrid, Skeleton, Stack, TagsInput, Text, TextInput,
} from '@mantine/core';
import { IconClockCog, IconRuler2, IconWorld } from '@tabler/icons-react';
import { useEffect, useMemo, useState } from 'react';
import { type Spec } from '../../api';
import { PageHeader } from '../../components/Misc';
import { DurationInput, SaveBar, SettingsCard, useSettingsDraft } from '../../components/SettingsKit';
import { useT } from '../../i18n';

const L = {
  ru: {
    title: 'Общие настройки', subtitle: 'Время, язык, адрес хаба и проверки по умолчанию',
    hub: 'Хаб', hub_desc: 'Как хаб показывает время и подписывает уведомления',
    tz: 'Часовой пояс', tz_hint: 'Для времени в уведомлениях и тихих часов', tz_now: 'Сейчас там {0}',
    lang: 'Язык уведомлений', lang_hint: 'Telegram и webhook. Язык интерфейса переключается вверху страницы',
    public_url: 'Публичный адрес', public_url_hint: 'Для ссылок «Открыть» в уведомлениях и команд установки агентов',
    region_label: 'Метка региона', region_label_hint: 'Метка пробы, по которой считается «блок в регионе» (обычно country)',
    checks: 'Проверки по умолчанию', checks_desc: 'Для проверок, где эти параметры не заданы явно',
    interval: 'Интервал', interval_hint: 'Как часто проверять', timeout: 'Таймаут', timeout_hint: 'Сколько ждать ответа', retries: 'Повторов до тревоги', retries_hint: 'Сбоев подряд, прежде чем тревожить',
    grace: 'Проба офлайн через', grace_hint: 'Ждём, пока агент переподключится',
    baseline: 'Проверка интернета на пробах', baseline_desc: 'Проба считается «с интернетом», если отвечает хотя бы одна цель. Без интернета проба исключается из консенсуса, чтобы её сбои не превратились в ложный «блок».',
    builtin: 'Встроенные цели', custom: 'Свои цели', builtin_hint: 'Смесь российских и зарубежных адресов — так пробы в сетях с белыми списками тоже считаются онлайн.',
    targets: 'Цели host:port', targets_hint: 'TCP-подключение; Enter после каждой', bad_target: 'Нужен формат host:port',
    other_specs: 'Ещё {0} проверок другого типа из конфигурации сохранятся как есть',
  },
  en: {
    title: 'General settings', subtitle: 'Time, language, hub address and check defaults',
    hub: 'Hub', hub_desc: 'How the hub shows time and words notifications',
    tz: 'Time zone', tz_hint: 'For times in notifications and quiet hours', tz_now: 'It is {0} there now',
    lang: 'Notification language', lang_hint: 'Telegram and webhook. The interface language is switched at the top of the page',
    public_url: 'Public URL', public_url_hint: 'For "Open" links in notifications and agent install commands',
    region_label: 'Region label', region_label_hint: 'The probe label that defines "blocked in a region" (usually country)',
    checks: 'Check defaults', checks_desc: 'For checks that do not set these explicitly',
    interval: 'Interval', interval_hint: 'How often to check', timeout: 'Timeout', timeout_hint: 'How long to wait for an answer', retries: 'Retries before alerting', retries_hint: 'Failures in a row before alerting',
    grace: 'Probe offline after', grace_hint: 'Time for an agent to reconnect',
    baseline: 'Probe internet check', baseline_desc: 'A probe "has internet" if any target answers. Without internet a probe is left out of consensus so its failures never turn into a false "blocked".',
    builtin: 'Built-in targets', custom: 'Custom targets', builtin_hint: 'A mix of Russian and foreign addresses, so probes on whitelist-only networks still count as online.',
    targets: 'host:port targets', targets_hint: 'A TCP connect; press Enter after each', bad_target: 'Use host:port',
    other_specs: '{0} more checks of other types from the config file are kept as they are',
  },
};

// The hub's built-in baseline (internal/hub/config.go); `baseline: null` means these.
const BUILTIN = ['77.88.8.8:53', '1.1.1.1:443', '8.8.8.8:443'];
const HOSTPORT = /^(\[[0-9a-fA-F:.]+\]|[^\s:/[\]]+):\d{1,5}$/;

function timeZones(): string[] {
  let list: string[] = [];
  try {
    list = Intl.supportedValuesOf('timeZone');
  } catch {
    /* old browser */
  }
  return list.includes('UTC') ? list : ['UTC', ...list];
}

function nowIn(tz?: string) {
  try {
    return new Intl.DateTimeFormat('ru-RU', { timeZone: tz || 'UTC', hour: '2-digit', minute: '2-digit' }).format(new Date());
  } catch {
    return '';
  }
}

function isBuiltin(b: Spec[] | null) {
  if (b == null) return true;
  const tcp = b.filter((s) => s.type === 'tcp' && s.tcp);
  return tcp.length === b.length && tcp.length === BUILTIN.length && tcp.every((s, i) => s.tcp!.address === BUILTIN[i]);
}

export default function General() {
  const t = useT(L);
  const s = useSettingsDraft();
  const { draft, update, managed, server } = s;
  const zones = useMemo(timeZones, []);
  // "custom" mode survives an empty list while the user is typing.
  const [customMode, setCustomMode] = useState<boolean | null>(null);
  const [badTarget, setBadTarget] = useState('');
  useEffect(() => {
    if (draft && customMode === null) setCustomMode(!isBuiltin(draft.baseline));
  }, [draft, customMode]);
  useEffect(() => {
    if (!s.dirty && server) setCustomMode(!isBuiltin(server.config.baseline));
  }, [server, s.dirty]);

  if (s.query.isError) {
    return (<><PageHeader title={t('title')} /><Alert color="red">{s.query.error.message}</Alert></>);
  }
  if (!draft) {
    return (
      <>
        <PageHeader title={t('title')} subtitle={t('subtitle')} />
        <Stack gap="lg">{[0, 1, 2].map((i) => <Skeleton key={i} h={220} radius="lg" />)}</Stack>
      </>
    );
  }

  const g = managed.general;
  const baseline = draft.baseline;
  const tcpTargets = (baseline ?? []).filter((x) => x.type === 'tcp' && x.tcp).map((x) => x.tcp!.address);
  const otherSpecs = (baseline ?? []).filter((x) => !(x.type === 'tcp' && x.tcp));

  const tzData = draft.timezone && !zones.includes(draft.timezone) ? [draft.timezone, ...zones] : zones;

  return (
    <>
      <PageHeader title={t('title')} subtitle={t('subtitle')} />
      <Stack gap="lg">
        <SettingsCard icon={IconWorld} title={t('hub')} description={t('hub_desc')} managed={g}>
          <SimpleGrid cols={{ base: 1, md: 2 }} spacing="md">
            <Select
              label={t('tz')}
              description={draft.timezone ? t('tz_now', nowIn(draft.timezone)) : t('tz_hint')}
              data={tzData}
              value={draft.timezone || 'UTC'}
              onChange={(v) => v && update((d) => { d.timezone = v; })}
              searchable
              allowDeselect={false}
              limit={60}
              nothingFoundMessage="—"
              disabled={g}
            />
            <Stack gap={4}>
              <div>
                <Text size="sm" fw={500}>{t('lang')}</Text>
                <Text size="xs" c="dimmed">{t('lang_hint')}</Text>
              </div>
              <SegmentedControl
                value={draft.lang || 'ru'}
                onChange={(v) => update((d) => { d.lang = v; })}
                data={[{ value: 'ru', label: 'Русский' }, { value: 'en', label: 'English' }]}
                disabled={g}
                style={{ alignSelf: 'flex-start' }}
              />
            </Stack>
            <TextInput
              label={t('public_url')}
              description={t('public_url_hint')}
              placeholder="https://uptime.example.com"
              value={draft.public_url ?? ''}
              onChange={(e) => { const v = e.currentTarget.value; update((d) => { d.public_url = v || undefined; }); }}
              styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)', fontSize: 13 } }}
            />
            <TextInput
              label={t('region_label')}
              description={t('region_label_hint')}
              placeholder="country"
              value={draft.region_label ?? ''}
              onChange={(e) => { const v = e.currentTarget.value.trim(); update((d) => { d.region_label = v || undefined; }); }}
              styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)' } }}
            />
          </SimpleGrid>
        </SettingsCard>

        <SettingsCard icon={IconClockCog} title={t('checks')} description={t('checks_desc')} managed={g}>
          <SimpleGrid cols={{ base: 1, xs: 2, md: 4 }} spacing="md">
            <DurationInput
              label={t('interval')}
              description={t('interval_hint')}
              placeholder="1m"
              value={draft.defaults.interval}
              onChange={(v) => update((d) => { d.defaults.interval = v || undefined; })}
              disabled={g}
            />
            <DurationInput
              label={t('timeout')}
              description={t('timeout_hint')}
              placeholder="15s"
              value={draft.defaults.timeout}
              onChange={(v) => update((d) => { d.defaults.timeout = v || undefined; })}
              disabled={g}
            />
            <NumberInput
              label={t('retries')}
              description={t('retries_hint')}
              value={draft.defaults.retries ?? 2}
              onChange={(v) => update((d) => { d.defaults.retries = typeof v === 'number' ? v : undefined; })}
              min={0}
              max={20}
              allowDecimal={false}
              disabled={g}
            />
            <DurationInput
              label={t('grace')}
              description={t('grace_hint')}
              placeholder="90s"
              value={draft.offline_grace}
              onChange={(v) => update((d) => { d.offline_grace = v || undefined; })}
              disabled={g}
            />
          </SimpleGrid>
        </SettingsCard>

        <SettingsCard icon={IconRuler2} title={t('baseline')} description={t('baseline_desc')} managed={g}>
          <SegmentedControl
            value={customMode ? 'custom' : 'builtin'}
            onChange={(v) => {
              const custom = v === 'custom';
              setCustomMode(custom);
              update((d) => {
                d.baseline = custom
                  ? (tcpTargets.length && !isBuiltin(d.baseline) ? d.baseline : BUILTIN.map((a) => ({ id: '', type: 'tcp', tcp: { address: a } })))
                  : null;
              });
            }}
            data={[{ value: 'builtin', label: t('builtin') }, { value: 'custom', label: t('custom') }]}
            disabled={g}
            style={{ alignSelf: 'flex-start' }}
            maw="100%"
          />
          {!customMode ? (
            <Stack gap={6}>
              <Group gap={6}>
                {BUILTIN.map((a) => <Badge key={a} variant="default" size="lg" ff="monospace" styles={{ root: { textTransform: 'none', fontWeight: 500 } }}>{a}</Badge>)}
              </Group>
              <Text size="xs" c="dimmed">{t('builtin_hint')}</Text>
            </Stack>
          ) : (
            <Stack gap={6}>
              <TagsInput
                label={t('targets')}
                description={t('targets_hint')}
                placeholder="1.1.1.1:443"
                value={tcpTargets}
                error={badTarget ? `${t('bad_target')}: ${badTarget}` : undefined}
                onChange={(v) => {
                  const bad = v.find((x) => !HOSTPORT.test(x.trim()));
                  setBadTarget(bad ?? '');
                  const ok = v.map((x) => x.trim()).filter((x) => HOSTPORT.test(x));
                  update((d) => {
                    d.baseline = [...ok.map((a) => ({ id: '', type: 'tcp', tcp: { address: a } })), ...otherSpecs];
                  });
                }}
                splitChars={[',', ' ']}
                disabled={g}
                styles={{ pill: { fontFamily: 'var(--mantine-font-family-monospace)' } }}
              />
              {otherSpecs.length > 0 && <Text size="xs" c="dimmed">{t('other_specs', otherSpecs.length)}</Text>}
            </Stack>
          )}
        </SettingsCard>

        <SaveBar dirty={s.dirty} saving={s.saving} onSave={s.save} onReset={s.reset} />
      </Stack>
    </>
  );
}
