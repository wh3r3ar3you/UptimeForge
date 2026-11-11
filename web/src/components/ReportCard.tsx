import { Alert, Box, Button, Group, Modal, Select, Switch, Text } from '@mantine/core';
import { IconEye, IconReportAnalytics, IconSend } from '@tabler/icons-react';
import { useState } from 'react';
import { api, type HubConfig } from '../api';
import { useT } from '../i18n';
import { notifyError, notifyOk } from './Misc';
import { SettingsCard } from './SettingsKit';

const L = {
  ru: {
    title: 'Недельный отчёт', desc: 'Раз в неделю в Telegram: аптайм и как он изменился, что лежало дольше всех, простой нод, блокировки, инциденты, онлайн и трафик.',
    on: 'Присылать отчёт', day: 'День', at: 'Время', tz: 'по часовому поясу хаба', preview: 'Показать сейчас', send: 'Отправить сейчас', sent: 'Отчёт отправлен',
    save_first: 'Сохраните настройки, чтобы отправить.', template: 'Текст отчёта меняется в разделе «Шаблоны».',
    sun: 'Воскресенье', mon: 'Понедельник', tue: 'Вторник', wed: 'Среда', thu: 'Четверг', fri: 'Пятница', sat: 'Суббота',
  },
  en: {
    title: 'Weekly report', desc: 'Once a week in Telegram: uptime and its change, what was down the longest, nodes down, bans, incidents, users online and traffic.',
    on: 'Send the report', day: 'Day', at: 'Time', tz: 'in the hub\'s time zone', preview: 'Show now', send: 'Send now', sent: 'Report sent',
    save_first: 'Save the settings to send.', template: 'The report text is set under Templates.',
    sun: 'Sunday', mon: 'Monday', tue: 'Tuesday', wed: 'Wednesday', thu: 'Thursday', fri: 'Friday', sat: 'Saturday',
  },
};

const DAYS = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'] as const;
const TIMES = Array.from({ length: 24 }, (_, h) => `${String(h).padStart(2, '0')}:00`);

/** ReportCard sets when the weekly report goes out, and shows or sends it now. */
export function ReportCard({ draft, update, managed, dirty }: {
  draft: HubConfig; update: (fn: (d: HubConfig) => void) => void; managed?: boolean; dirty: boolean;
}) {
  const t = useT(L);
  const r = draft.alerts.report ?? {};
  const [shown, setShown] = useState<string | null>(null);
  const [busy, setBusy] = useState('');
  const set = (patch: Partial<NonNullable<HubConfig['alerts']['report']>>) => update((d) => {
    const next = { ...d.alerts.report, ...patch };
    d.alerts.report = next.off || next.day || next.at ? next : undefined;
  });
  const show = async () => {
    setBusy('show');
    try {
      setShown((await api<{ rich: string }>('GET', '/api/v1/report/preview')).rich);
    } catch (e) {
      notifyError(e);
    } finally {
      setBusy('');
    }
  };
  const send = async () => {
    setBusy('send');
    try {
      await api('POST', '/api/v1/report/send');
      notifyOk(t('sent'));
    } catch (e) {
      notifyError(e);
    } finally {
      setBusy('');
    }
  };
  return (
    <SettingsCard icon={IconReportAnalytics} title={t('title')} description={t('desc')} managed={managed}>
      <Switch label={t('on')} checked={!r.off} disabled={managed} onChange={(e) => set({ off: !e.currentTarget.checked || undefined })} />
      <Group gap="md" align="flex-end">
        <Select label={t('day')} w={180} disabled={managed || r.off} value={r.day ?? 'sun'} allowDeselect={false}
          data={DAYS.map((d) => ({ value: d, label: t(d) }))} onChange={(v) => set({ day: v === 'sun' ? undefined : v ?? undefined })} />
        <Select label={t('at')} w={120} disabled={managed || r.off} value={r.at ?? '20:00'} allowDeselect={false} searchable
          data={TIMES.includes(r.at ?? '20:00') ? TIMES : [...TIMES, r.at!]} onChange={(v) => set({ at: v === '20:00' ? undefined : v ?? undefined })} />
        <Text size="xs" c="dimmed" pb={8}>{t('tz')}</Text>
      </Group>
      <Group gap="xs">
        <Button size="xs" variant="default" leftSection={<IconEye size={14} />} loading={busy === 'show'} onClick={show}>{t('preview')}</Button>
        <Button size="xs" variant="light" leftSection={<IconSend size={14} />} loading={busy === 'send'} disabled={dirty} onClick={send}>{t('send')}</Button>
        {dirty && <Text size="xs" c="dimmed">{t('save_first')}</Text>}
      </Group>
      <Text size="xs" c="dimmed">{t('template')}</Text>
      <Modal opened={shown != null} onClose={() => setShown(null)} size="lg" title={<Text fw={700}>{t('title')}</Text>}>
        {shown ? <Box className="uf-tg-preview" dangerouslySetInnerHTML={{ __html: shown }} /> : <Alert>…</Alert>}
      </Modal>
    </SettingsCard>
  );
}
