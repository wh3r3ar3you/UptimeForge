import {
  Alert, Grid, Group, SegmentedControl, Select, Skeleton, Stack, Switch, Text, TextInput, PasswordInput } from '@mantine/core';
import {
  IconBellRinging, IconHeartbeat, IconMoonStars, IconWebhook,
} from '@tabler/icons-react';
import { useEffect, useRef, useState } from 'react';
import { type HubConfig } from '../../api';
import { HostRulesCard } from '../../components/HostRulesCard';
import { ReportCard } from '../../components/ReportCard';
import { confirmThen } from '../../components/guards';
import { PageHeader } from '../../components/Misc';
import { compactDuration, KeyValueEditor, SaveBar, SettingsCard, useSettingsDraft } from '../../components/SettingsKit';
import { useCommon, useT } from '../../i18n';

const L = {
  ru: {
    title: 'Правила уведомлений', subtitle: 'Когда приходят тревоги, тихие часы и webhook',
    off_title: 'Выключить webhook?', off_confirm: 'Выключить',
    off_body: 'После сохранения хаб удалит адрес webhook и его заголовки (в них бывают токены). Чтобы включить снова, их придётся ввести заново. До сохранения это можно отменить.',
    enabled: 'Включено', wh: 'Webhook', wh_desc: 'Каждое событие инцидента (открыт, обновлён, закрыт, напоминание) уходит POST-запросом с JSON — для Discord/Slack-мостов, n8n или своего бота.',
    wh_url: 'URL', wh_secret: 'Секрет подписи', wh_secret_hint: 'Необязательно. Каждый запрос подписывается: X-UptimeForge-Signature: sha256=<HMAC тела>', headers: 'Заголовки', headers_hint: 'Например Authorization. Значения с token/secret/authorization в имени скрыты',
    policy: 'Политика тревог', policy_desc: 'Общая для всех каналов',
    quiet: 'Тихие часы', quiet_hint: 'В это время предупреждения и восстановления приходят без звука. Критичные инциденты звучат всегда.',
    from: 'С', to: 'До', remind: 'Напоминать о непринятых', remind_hint: 'Пока инцидент открыт и никто не нажал «Принял»',
    off: 'Не напоминать', custom: 'другое: {0}',
    hb: 'Heartbeat', hb_desc: 'Страховка на случай, когда падает сам хаб',
    hb_why: 'Если сервер с UptimeForge выключится целиком, предупредить об этом будет некому. Хаб регулярно пингует внешний сервис (Healthchecks.io, Better Stack, Uptime Kuma Push), пока сам здоров; когда пинги прекращаются — тревогу поднимает уже он.',
    hb_url: 'URL для пинга', hb_interval: 'Как часто',
    s30: 'каждые 30 с', m1: 'каждую минуту', m2: 'каждые 2 мин', m5: 'каждые 5 мин', m10: 'каждые 10 мин',
  },
  en: {
    title: 'Notification rules', subtitle: 'Where alerts go and how',
    off_title: 'Turn the webhook off?', off_confirm: 'Turn off',
    off_body: 'On save the hub deletes the webhook URL and its headers (which often hold tokens). To turn it back on you will have to enter them again. Until you save, this can be undone.',
    enabled: 'Enabled', wh: 'Webhook', wh_desc: 'Every incident event (open, update, resolve, reminder) is POSTed as JSON — for Discord/Slack bridges, n8n or your own bot.',
    wh_url: 'URL', wh_secret: 'Signing secret', wh_secret_hint: 'Optional. Every request is signed: X-UptimeForge-Signature: sha256=<HMAC of the body>', headers: 'Headers', headers_hint: 'E.g. Authorization. Values whose name has token/secret/authorization are hidden',
    policy: 'Alert policy', policy_desc: 'Shared by every channel',
    quiet: 'Quiet hours', quiet_hint: 'Warnings and recoveries arrive silently during these hours. Critical incidents always make a sound.',
    from: 'From', to: 'To', remind: 'Remind about unacknowledged', remind_hint: 'While the incident is open and nobody pressed Acknowledge',
    off: 'Off', custom: 'other: {0}',
    hb: 'Heartbeat', hb_desc: 'A safety net for when the hub itself goes down',
    hb_why: 'If the UptimeForge server dies completely, nothing is left to tell you. While healthy, the hub pings an external service (Healthchecks.io, Better Stack, Uptime Kuma Push); when the pings stop, that service raises the alarm.',
    hb_url: 'Ping URL', hb_interval: 'How often',
    s30: 'every 30s', m1: 'every minute', m2: 'every 2 min', m5: 'every 5 min', m10: 'every 10 min',
  },
};

type Wh = NonNullable<HubConfig['webhook']>;

const REMIND = ['0s', '15m', '30m', '1h', '2h'];

const HHMM = /^([01]?\d|2[0-3]):[0-5]\d$/;

/** TimeField takes a 24-hour HH:MM regardless of the browser locale. */
function TimeField({ label, value, onChange }: { label: string; value: string; onChange: (v: string) => void }) {
  const [text, setText] = useState(value);
  useEffect(() => {
    if (HHMM.test(value) && value !== text) setText(value);
  }, [value]);
  const ok = HHMM.test(text);
  return (
    <TextInput
      label={label}
      value={text}
      placeholder="23:00"
      w={110}
      maxLength={5}
      error={!ok && text !== ''}
      onChange={(e) => {
        const v = e.currentTarget.value.replace(/[^\d:]/g, '');
        setText(v);
        if (HHMM.test(v)) onChange(v.padStart(5, '0'));
      }}
      styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)', textAlign: 'center' } }}
    />
  );
}

/** same keeps the server's spelling ("30m0s") when the choice means the same duration, so the form isn't dirty. */
function same(orig: string | undefined, v: string) {
  return orig && compactDuration(orig) === v ? orig : v;
}

export default function Notifications() {
  const t = useT(L);
  const c = useCommon();
  const s = useSettingsDraft();
  const { draft, update, managed, server } = s;
  const lastWh = useRef<Wh | null>(null);

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

  const wh = draft.webhook;
  const quiet = draft.alerts.quiet_hours ?? '';
  const [qFrom = '', qTo = ''] = quiet ? quiet.split('-') : [];
  const setQuiet = (from: string, to: string) => update((d) => { d.alerts.quiet_hours = `${from}-${to}`; });
  const remind = compactDuration(draft.alerts.remind_every ?? '30m');
  const remindData = REMIND.map((v) => ({ value: v, label: v === '0s' ? t('off') : v }));
  if (!REMIND.includes(remind)) remindData.push({ value: remind, label: t('custom', remind) });

  const hbInterval = compactDuration(draft.heartbeat.interval ?? '1m');
  const hbData = [['30s', 's30'], ['1m', 'm1'], ['2m', 'm2'], ['5m', 'm5'], ['10m', 'm10']].map(([v, k]) => ({ value: v, label: t(k as 's30') }));
  if (!hbData.some((o) => o.value === hbInterval)) hbData.push({ value: hbInterval, label: hbInterval });

  return (
    <>
      <PageHeader title={t('title')} subtitle={t('subtitle')} />
      <Stack gap="lg">
        <SettingsCard
          icon={IconWebhook}
          title={t('wh')}
          description={t('wh_desc')}
          managed={managed.webhook}
          right={
            <Switch
              label={t('enabled')}
              checked={!!wh}
              disabled={managed.webhook}
              onChange={(e) => {
                const on = e.currentTarget.checked;
                const apply = () => update((d) => {
                  if (on) d.webhook = lastWh.current ?? { url: '' };
                  else { lastWh.current = d.webhook ?? null; d.webhook = null; }
                });
                if (!on && server?.config.webhook) {
                  confirmThen({ title: t('off_title'), body: t('off_body'), confirm: t('off_confirm'), cancel: c('cancel'), onConfirm: apply });
                } else apply();
              }}
            />
          }
        >
          {wh && (
            <>
              <TextInput
                label={t('wh_url')}
                placeholder="https://n8n.example.com/webhook/uptimeforge"
                value={wh.url}
                onChange={(e) => { const v = e.currentTarget.value; update((d) => { if (d.webhook) d.webhook.url = v; }); }}
                styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)', fontSize: 13 } }}
              />
              <PasswordInput
                label={t('wh_secret')}
                description={t('wh_secret_hint')}
                value={wh.secret ?? ''}
                disabled={managed.webhook}
                onChange={(e) => { const v = e.currentTarget.value; update((d) => { if (d.webhook) d.webhook.secret = v || undefined; }); }}
              />
              <KeyValueEditor
                label={t('headers')}
                description={t('headers_hint')}
                value={wh.headers}
                disabled={managed.webhook}
                onChange={(h) => update((d) => { if (d.webhook) d.webhook.headers = Object.keys(h).length ? h : undefined; })}
              />
            </>
          )}
        </SettingsCard>

        <SettingsCard icon={IconBellRinging} title={t('policy')} description={t('policy_desc')} managed={managed.alerts}>
          <Stack gap="xs">
            <Switch
              label={t('quiet')}
              description={t('quiet_hint')}
              checked={!!quiet}
              onChange={(e) => {
                const on = e.currentTarget.checked;
                update((d) => { d.alerts.quiet_hours = on ? '23:00-08:00' : undefined; });
              }}
              thumbIcon={<IconMoonStars size={10} />}
            />
            {quiet && (
              <Group gap="md" pl={54} wrap="wrap">
                <TimeField label={t('from')} value={qFrom} onChange={(v) => setQuiet(v, qTo)} />
                <TimeField label={t('to')} value={qTo} onChange={(v) => setQuiet(qFrom, v)} />
              </Group>
            )}
          </Stack>
          <Stack gap={6}>
            <div>
              <Text size="sm" fw={500}>{t('remind')}</Text>
              <Text size="xs" c="dimmed">{t('remind_hint')}</Text>
            </div>
            <SegmentedControl
              value={remind}
              onChange={(v) => update((d) => { d.alerts.remind_every = same(server?.config.alerts.remind_every, v); })}
              data={remindData}
              style={{ alignSelf: 'flex-start' }}
              maw="100%"
            />
          </Stack>
        </SettingsCard>

        <HostRulesCard draft={draft} update={update} managed={managed.alerts} />

        <ReportCard draft={draft} update={update} managed={managed.alerts} dirty={s.dirty} />

        <SettingsCard icon={IconHeartbeat} title={t('hb')} description={t('hb_desc')} managed={managed.heartbeat}>
          <Alert variant="light" color="blue" icon={<IconHeartbeat size={18} />}>
            <Text size="sm">{t('hb_why')}</Text>
          </Alert>
          <Grid gutter="md">
            <Grid.Col span={{ base: 12, md: 8 }}>
            <TextInput
              label={t('hb_url')}
              placeholder="https://hc-ping.com/your-uuid"
              value={draft.heartbeat.url ?? ''}
              onChange={(e) => { const v = e.currentTarget.value; update((d) => { d.heartbeat.url = v || undefined; }); }}
              styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)', fontSize: 13 } }}
            />
            </Grid.Col>
            <Grid.Col span={{ base: 12, md: 4 }}>
            <Select
              label={t('hb_interval')}
              data={hbData}
              value={hbInterval}
              onChange={(v) => v && update((d) => { d.heartbeat.interval = same(server?.config.heartbeat.interval, v); })}
              allowDeselect={false}
            />
            </Grid.Col>
          </Grid>
        </SettingsCard>

        <SaveBar dirty={s.dirty} saving={s.saving} onSave={s.save} onReset={s.reset} />
      </Stack>
    </>
  );
}
