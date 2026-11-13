import { Alert, Badge, Box, Button, Card, Group, NavLink, SegmentedControl, Select, Skeleton, Stack, Text, Textarea, Tooltip } from '@mantine/core';
import { IconRestore, IconSend } from '@tabler/icons-react';
import { useQuery } from '@tanstack/react-query';
import { useEffect, useRef, useState } from 'react';
import { api } from '../../api';
import { notifyError, notifyOk, PageHeader } from '../../components/Misc';
import { SaveBar, useSettingsDraft } from '../../components/SettingsKit';
import { useT } from '../../i18n';

const L = {
  ru: {
    title: 'Шаблоны уведомлений', subtitle: 'Свой текст для каждого вида сообщения. Пусто — стандартный.',
    k_card: 'Карточка инцидента', k_resolved: 'Восстановление', k_escalated: 'Стало хуже (ответ)', k_remind: 'Напоминание', k_report: 'Недельный отчёт',
    kinds: 'Только для вида', kind_all: 'Для всех видов',
    lang: 'Язык', custom: 'свой', placeholders: 'Подстановки', placeholders_hint: 'Клик вставляет в текст. {?x}…{/x} показывается, только если x не пустой.',
    sample: 'Пример', preview: 'Предпросмотр', reset: 'Вернуть стандартный', test: 'Отправить тест', test_ok: 'Отправлено в основной чат',
    rich: 'Карточка (rich-разметка Telegram): h3, p, b, i, code, pre, blockquote, table, details, footer.', html: 'Ответ (HTML Telegram): b, i, u, s, code, pre, a, blockquote.',
    invalid: 'Telegram такое не примет', default_note: 'Сейчас стандартный шаблон. Измените текст, чтобы сделать свой.',
    s_node_down: 'Нода не на связи', s_users_drop: 'Ушли пользователи', s_blocked: 'Блок в регионе', s_down: 'Не работает', s_partial: 'Частично', s_host: 'Порог ЦП',
    s_probe_offline: 'Агент офлайн', s_subscription: 'Подписка', s_agent_update: 'Агенты не вернулись после обновления',
  },
  en: {
    title: 'Notification templates', subtitle: 'Your own text for each kind of message. Empty means the default.',
    k_card: 'Incident card', k_resolved: 'Recovery', k_escalated: 'Got worse (reply)', k_remind: 'Reminder', k_report: 'Weekly report',
    kinds: 'Only for kind', kind_all: 'For every kind',
    lang: 'Language', custom: 'custom', placeholders: 'Placeholders', placeholders_hint: 'A click inserts it. {?x}…{/x} shows only when x is not empty.',
    sample: 'Sample', preview: 'Preview', reset: 'Back to default', test: 'Send a test', test_ok: 'Sent to the main chat',
    rich: 'Card (Telegram rich markup): h3, p, b, i, code, pre, blockquote, table, details, footer.', html: 'Reply (Telegram HTML): b, i, u, s, code, pre, a, blockquote.',
    invalid: 'Telegram would refuse this', default_note: 'This is the default. Edit the text to make it your own.',
    s_node_down: 'Node down', s_users_drop: 'Users gone', s_blocked: 'Blocked in a region', s_down: 'Down', s_partial: 'Partial', s_host: 'CPU threshold',
    s_probe_offline: 'Agent offline', s_subscription: 'Subscription', s_agent_update: 'Agents did not come back after the update',
  },
};

type Info = { defaults: Record<string, string>; placeholders: string[]; keys: string[]; kinds: string[]; samples: string[] };
const RICH = ['card', 'resolved', 'report'];

/** Templates edits the notification texts with a live preview on sample incidents. */
export default function Templates() {
  const t = useT(L);
  const s = useSettingsDraft();
  const info = useQuery({ queryKey: ['templates'], queryFn: () => api<Info>('GET', '/api/v1/templates') });
  const [lang, setLang] = useState<'ru' | 'en'>('ru');
  const [key, setKey] = useState('card');
  const [kind, setKind] = useState<string | null>(null);
  const [sample, setSample] = useState('node_down');
  const [preview, setPreview] = useState<{ rich?: string; html?: string; error?: string }>({});
  const area = useRef<HTMLTextAreaElement>(null);

  const full = kind && key === 'card' ? `card.${kind}` : key;
  const custom = s.draft?.templates?.[lang]?.[full];
  const text = custom ?? info.data?.defaults[key] ?? '';
  const setText = (v: string) => s.update((d) => {
    const all = { ...d.templates };
    const mine = { ...all[lang] };
    if (v && v !== info.data?.defaults[key]) mine[full] = v; else delete mine[full];
    if (Object.keys(mine).length) all[lang] = mine; else delete all[lang];
    d.templates = Object.keys(all).length ? all : undefined;
  });

  useEffect(() => {
    if (!info.data) return;
    const timer = setTimeout(() => {
      api<{ rich?: string; html?: string; error?: string }>('POST', '/api/v1/templates/preview', { lang, key: full, text, sample })
        .then(setPreview).catch((e) => setPreview({ error: String(e?.message ?? e) }));
    }, 350);
    return () => clearTimeout(timer);
  }, [text, lang, full, sample, info.data]);

  const insert = (name: string) => {
    const el = area.current;
    const token = `{${name}}`;
    if (!el) return setText(text + token);
    const at = el.selectionStart ?? text.length;
    setText(text.slice(0, at) + token + text.slice(el.selectionEnd ?? at));
    requestAnimationFrame(() => { el.focus(); el.setSelectionRange(at + token.length, at + token.length); });
  };
  const test = async () => {
    try {
      await api('POST', '/api/v1/templates/test', { lang, key: full, text, sample });
      notifyOk(t('test_ok'));
    } catch (e) {
      notifyError(e);
    }
  };

  if (!s.draft || !info.data) return <><PageHeader title={t('title')} /><Skeleton h={420} radius="lg" /></>;
  const customised = (k: string) => !!s.draft?.templates?.[lang]?.[k];

  return (
    <>
      <PageHeader title={t('title')} subtitle={t('subtitle')} />
      <Box style={{ display: 'grid', gridTemplateColumns: 'minmax(200px, 240px) minmax(0, 1fr) minmax(0, 1fr)', gap: 16, alignItems: 'start' }} className="uf-templates">
        <Card padding="xs">
          <SegmentedControl fullWidth size="xs" mb="xs" value={lang} onChange={(v) => setLang(v as 'ru' | 'en')} data={[{ value: 'ru', label: 'RU' }, { value: 'en', label: 'EN' }]} />
          {info.data.keys.map((k) => (
            <NavLink key={k} active={key === k} label={t(`k_${k}` as 'k_card')} onClick={() => { setKey(k); setKind(null); }}
              rightSection={customised(k) ? <Badge size="xs" variant="light">{t('custom')}</Badge> : undefined} />
          ))}
          {key === 'card' && (
            <Select mt="xs" size="xs" label={t('kinds')} placeholder={t('kind_all')} clearable value={kind} onChange={setKind}
              data={info.data.kinds.map((k) => ({ value: k, label: `${k}${customised(`card.${k}`) ? ' ★' : ''}` }))} />
          )}
        </Card>

        <Stack gap="sm">
          <Card padding="md">
            <Stack gap="xs">
              <Text size="xs" c="dimmed">{RICH.includes(key) ? t('rich') : t('html')}</Text>
              {!custom && <Text size="xs" c="dimmed">{t('default_note')}</Text>}
              <Textarea ref={area} autosize minRows={10} maxRows={24} value={text} onChange={(e) => setText(e.currentTarget.value)}
                styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)', fontSize: 12.5 } }} error={preview.error ? `${t('invalid')}: ${preview.error}` : undefined} />
              <Group gap="xs">
                <Button size="xs" variant="default" leftSection={<IconRestore size={14} />} disabled={!custom} onClick={() => setText('')}>{t('reset')}</Button>
                <Tooltip label={t('test')}><Button size="xs" variant="light" leftSection={<IconSend size={14} />} disabled={!!preview.error || s.dirty} onClick={test}>{t('test')}</Button></Tooltip>
              </Group>
            </Stack>
          </Card>
          <Card padding="md">
            <Text size="sm" fw={600}>{t('placeholders')}</Text>
            <Text size="xs" c="dimmed" mb="xs">{t('placeholders_hint')}</Text>
            <Group gap={6}>
              {info.data.placeholders.map((p) => <Badge key={p} variant="default" style={{ cursor: 'pointer', textTransform: 'none' }} onClick={() => insert(p)}>{`{${p}}`}</Badge>)}
            </Group>
          </Card>
        </Stack>

        <Card padding="md">
          <Group justify="space-between" mb="sm">
            <Text size="sm" fw={600}>{t('preview')}</Text>
            <Select size="xs" w={190} value={sample} onChange={(v) => v && setSample(v)} allowDeselect={false}
              data={info.data.samples.map((x) => ({ value: x, label: t(`s_${x}` as 's_down') }))} />
          </Group>
          {preview.error ? <Alert color="red" variant="light">{preview.error}</Alert> : (
            <Box className="uf-tg-preview" dangerouslySetInnerHTML={{ __html: (preview.rich || (preview.html ?? '').replace(/\n/g, '<br>')) }} />
          )}
        </Card>
      </Box>
      <SaveBar dirty={s.dirty} saving={s.saving} onSave={s.save} onReset={s.reset} />
    </>
  );
}
