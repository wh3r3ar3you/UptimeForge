import { ActionIcon, Badge, Box, Button, Card, Group, Paper, Stack, Text, TextInput, Title, Tooltip } from '@mantine/core';
import { IconDeviceFloppy, IconLock, IconPlus, IconTrash, type Icon } from '@tabler/icons-react';
import { useQueryClient } from '@tanstack/react-query';
import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { api, useSettings, type ApiError, type HubConfig, type Settings } from '../api';
import { parseGoDuration } from '../format';
import { useCommon, useLang, useT } from '../i18n';
import { useLeaveGuard } from './guards';
import { notifyError, notifyOk } from './Misc';

const L = {
  ru: {
    unsaved: 'Есть несохранённые изменения', reset: 'Отменить', remove_header: 'Удалить заголовок', key: 'Заголовок', value: 'Значение', add_header: 'Добавить заголовок',
    bad_duration: 'Формат: 30s, 5m, 1h30m', sec: '{0} с', min: '{0} мин', hour: '{0} ч',
  },
  en: {
    unsaved: 'You have unsaved changes', reset: 'Discard', remove_header: 'Remove header', key: 'Header', value: 'Value', add_header: 'Add header',
    bad_duration: 'Format: 30s, 5m, 1h30m', sec: '{0}s', min: '{0}m', hour: '{0}h',
  },
};

/** The hub masks secrets with this value; sending it back keeps the stored secret. */
export const MASK = '••••••••';

const SECRET_SECTIONS = ['telegram', 'webhook', 'remnawave'] as const;

function clean(cfg: HubConfig, managed: Record<string, boolean>): HubConfig {
  const c: HubConfig = structuredClone(cfg);
  // Empty durations are not valid Go durations: leave them out so the hub uses its default.
  if (!c.offline_grace) delete c.offline_grace;
  if (!c.heartbeat?.interval) delete c.heartbeat.interval;
  if (!c.defaults?.interval) delete c.defaults.interval;
  if (!c.defaults?.timeout) delete c.defaults.timeout;
  if (c.alerts && !c.alerts.remind_every) delete c.alerts.remind_every;
  // Secret-bearing sections pinned by config.yaml are not ours to store: the
  // file wins anyway, and echoing the masked values back would save the mask.
  for (const s of SECRET_SECTIONS) if (managed[s]) c[s] = null;
  return c;
}

/**
 * useSettingsDraft keeps an editable copy of the hub settings. Saving PUTs the
 * whole config, as the API expects; untouched sections go back unchanged.
 */
export function useSettingsDraft() {
  const q = useSettings();
  const qc = useQueryClient();
  const c = useCommon();
  const [draft, setDraft] = useState<HubConfig | null>(null);
  const [base, setBase] = useState('');
  const [saving, setSaving] = useState(false);
  const dirty = draft != null && JSON.stringify(draft) !== base;
  useLeaveGuard(dirty && !saving);

  useEffect(() => {
    if (q.data && !dirty) {
      setDraft(structuredClone(q.data.config));
      setBase(JSON.stringify(q.data.config));
    }
    // Only when the server copy changes; a dirty draft is never overwritten.
  }, [q.data]);

  const update = (fn: (d: HubConfig) => void) =>
    setDraft((d) => {
      if (!d) return d;
      const n = structuredClone(d);
      fn(n);
      return n;
    });

  const save = async () => {
    if (!draft || !q.data) return false;
    setSaving(true);
    try {
      const res = await api<Settings>('PUT', '/api/v1/settings', clean(draft, q.data.managed ?? {}));
      qc.setQueryData(['settings'], res);
      setDraft(structuredClone(res.config));
      setBase(JSON.stringify(res.config));
      qc.invalidateQueries();
      notifyOk(c('saved'));
      return true;
    } catch (e) {
      notifyError(e as ApiError);
      return false;
    } finally {
      setSaving(false);
    }
  };

  const reset = () => {
    if (q.data) {
      setDraft(structuredClone(q.data.config));
      setBase(JSON.stringify(q.data.config));
    }
  };

  return { query: q, server: q.data, draft, update, dirty, saving, save, reset, managed: q.data?.managed ?? {} };
}

/** SettingsCard groups one settings section; a managed section is read-only and says so. */
export function SettingsCard({
  icon: I, title, description, managed, right, footer, children,
}: { icon: Icon; title: ReactNode; description?: ReactNode; managed?: boolean; right?: ReactNode; footer?: ReactNode; children: ReactNode }) {
  const c = useCommon();
  return (
    <Card padding="lg">
      <Stack gap="md">
        <Stack gap={6}>
          <Group justify="space-between" align="center" gap="sm" wrap="nowrap">
            <Group gap="md" wrap="nowrap" align="center" style={{ minWidth: 0 }}>
              <Box className="uf-icon-tile" style={{ ['--tint' as string]: '#20cfae', width: 40, height: 40 }}>
                <I size={20} stroke={1.7} />
              </Box>
              <Group gap={8} style={{ minWidth: 0 }}>
                <Title order={4} fw={650}>{title}</Title>
                {managed && (
                  <Tooltip label={c('managed_hint')}>
                    <Badge variant="light" color="gray" leftSection={<IconLock size={12} />} styles={{ root: { textTransform: 'none' } }}>
                      {c('managed')}
                    </Badge>
                  </Tooltip>
                )}
              </Group>
            </Group>
            {right && <Box style={{ flex: 'none' }}>{right}</Box>}
          </Group>
          {description && <Text size="sm" c="dimmed" pl={{ base: 0, sm: 56 }}>{description}</Text>}
        </Stack>
        <fieldset disabled={managed} style={{ border: 0, padding: 0, margin: 0, minWidth: 0 }}>
          <Stack gap="md">{children}</Stack>
        </fieldset>
        {footer}
      </Stack>
    </Card>
  );
}

/** SaveBar floats at the bottom of the viewport while there are unsaved changes. */
export function SaveBar({ dirty, saving, onSave, onReset }: { dirty: boolean; saving: boolean; onSave: () => void; onReset: () => void }) {
  const t = useT(L);
  const c = useCommon();
  if (!dirty) return null;
  return (
    <Paper
      withBorder
      shadow="lg"
      p="sm"
      radius="lg"
      style={{ position: 'sticky', bottom: 16, zIndex: 10, backdropFilter: 'blur(10px)', background: 'light-dark(rgba(255,255,255,0.92), rgba(25,33,44,0.92))' }}
    >
      <Group justify="space-between" gap="sm">
        <Text size="sm" fw={600} pl={4}>{t('unsaved')}</Text>
        <Group gap="xs">
          <Button variant="default" onClick={onReset} disabled={saving}>{t('reset')}</Button>
          <Button leftSection={<IconDeviceFloppy size={16} />} onClick={onSave} loading={saving}>{c('save')}</Button>
        </Group>
      </Group>
    </Paper>
  );
}

/** KeyValueEditor edits a string map (HTTP headers) as rows. */
export function KeyValueEditor({
  value, onChange, disabled, label, description,
}: { value?: Record<string, string> | null; onChange: (v: Record<string, string>) => void; disabled?: boolean; label?: string; description?: string }) {
  const t = useT(L);
  // Rows keep their order and allow a half-typed (empty-key) row while editing.
  const [rows, setRows] = useState<[string, string][]>(() => Object.entries(value ?? {}));
  const external = JSON.stringify(value ?? {});
  useEffect(() => {
    const mine = JSON.stringify(Object.fromEntries(rows.filter(([k]) => k.trim())));
    if (mine !== external) setRows(Object.entries(value ?? {}));
  }, [external]);
  const emit = (r: [string, string][]) => {
    setRows(r);
    onChange(Object.fromEntries(r.filter(([k]) => k.trim()).map(([k, v]) => [k.trim(), v])));
  };
  return (
    <Stack gap={6}>
      {label && (
        <Box>
          <Text size="sm" fw={500}>{label}</Text>
          {description && <Text size="xs" c="dimmed">{description}</Text>}
        </Box>
      )}
      {rows.map(([k, v], i) => (
        <Group key={i} gap="xs" wrap="nowrap" align="flex-start">
          <TextInput
            placeholder={t('key')}
            value={k}
            onChange={(e) => emit(rows.map((r, j) => (j === i ? [e.currentTarget.value, r[1]] : r)))}
            style={{ flex: '1 1 40%' }}
            styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)', fontSize: 13 } }}
            disabled={disabled}
          />
          <TextInput
            placeholder={t('value')}
            value={v}
            onChange={(e) => emit(rows.map((r, j) => (j === i ? [r[0], e.currentTarget.value] : r)))}
            style={{ flex: '1 1 60%' }}
            styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)', fontSize: 13 } }}
            disabled={disabled}
          />
          <ActionIcon variant="subtle" color="gray" size={36} onClick={() => emit(rows.filter((_, j) => j !== i))} disabled={disabled} aria-label={t('remove_header')}>
            <IconTrash size={16} />
          </ActionIcon>
        </Group>
      ))}
      <Box>
        <Button variant="subtle" size="xs" leftSection={<IconPlus size={14} />} onClick={() => setRows([...rows, ['', '']])} disabled={disabled}>
          {t('add_header')}
        </Button>
      </Box>
    </Stack>
  );
}

const GO_DURATION = /^(\d+(\.\d+)?(h|m|s|ms))+$/;

/** humanDuration renders a Go duration ("1m30s") for people. */
export function useHumanDuration() {
  const t = useT(L);
  return (s?: string) => {
    const v = parseGoDuration(s);
    if (!v) return '';
    const h = Math.floor(v / 3600), m = Math.floor((v % 3600) / 60), sec = Math.round(v % 60);
    return [h && t('hour', h), m && t('min', m), sec && t('sec', sec)].filter(Boolean).join(' ');
  };
}

/** DurationInput edits a Go duration string, showing it in words. */
export function DurationInput({
  label, description, value, onChange, placeholder, disabled,
}: { label: string; description?: string; value?: string; onChange: (v: string) => void; placeholder?: string; disabled?: boolean }) {
  const t = useT(L);
  const human = useHumanDuration();
  const { lang } = useLang();
  const [text, setText] = useState(compactDuration(value));
  useEffect(() => {
    if (parseGoDuration(text) !== parseGoDuration(value)) setText(compactDuration(value));
  }, [value]);
  const valid = text === '' || GO_DURATION.test(text.trim());
  const words = useMemo(() => (valid && text ? human(text.trim()) : ''), [valid, text, lang]);
  return (
    <TextInput
      label={label}
      description={description}
      placeholder={placeholder}
      value={text}
      disabled={disabled}
      error={valid ? undefined : t('bad_duration')}
      rightSection={words ? <Text size="xs" c="dimmed" pr={8} style={{ whiteSpace: 'nowrap' }}>{words}</Text> : undefined}
      rightSectionWidth={words ? Math.min(120, words.length * 7 + 16) : undefined}
      rightSectionPointerEvents="none"
      onChange={(e) => {
        const v = e.currentTarget.value.replace(/\s+/g, '');
        setText(v);
        if (v === '' || GO_DURATION.test(v)) onChange(v);
      }}
      styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)' } }}
    />
  );
}

/** compactDuration turns Go's "1m0s" / "1h0m0s" into "1m" / "1h". */
export function compactDuration(s?: string) {
  if (!s) return '';
  return s.replace(/(\d)m0s$/, '$1m').replace(/(\d)h0m$/, '$1h').replace(/^0s$/, '0s');
}
