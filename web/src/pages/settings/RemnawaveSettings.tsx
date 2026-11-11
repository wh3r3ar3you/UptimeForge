import {
  Alert, Button, Group, NumberInput, PasswordInput, Select, SimpleGrid, Skeleton, Stack, Switch, Text, TextInput,
} from '@mantine/core';
import { IconAffiliate, IconCircleCheck, IconPlugConnected, IconRefresh, IconTrendingDown } from '@tabler/icons-react';
import { useRef, useState } from 'react';
import { api, ApiError, useRemnawave, type HubConfig } from '../../api';
import { confirmThen } from '../../components/guards';
import { PageHeader } from '../../components/Misc';
import { compactDuration, KeyValueEditor, MASK, SaveBar, SettingsCard, useSettingsDraft } from '../../components/SettingsKit';
import { Dot } from '../../components/Status';
import { ago, num } from '../../format';
import { useCommon, useLang, useT } from '../../i18n';
import { status } from '../../theme';

const L = {
  ru: {
    title: 'Remnawave', subtitle: 'Подключение к панели по API-ключу и тревога, когда с ноды уходят пользователи',
    rw: 'Подключение', rw_desc: 'Хаб раз в 30 секунд читает из панели ноды, онлайн, трафик и нагрузку. Нужен только адрес панели и API-ключ.',
    enabled: 'Включено', rw_url: 'Адрес панели', rw_url_hint: 'Тот же, что открываете в браузере', rw_token: 'API-ключ',
    rw_token_hint: 'Remnawave → Настройки → API-токены → Создать', token_kept: 'Сохранён. Оставьте пустым, чтобы не менять',
    headers: 'Дополнительные заголовки', headers_hint: 'Если панель закрыта Cloudflare Access, basic-auth прокси или секретным заголовком',
    test: 'Проверить подключение', test_ok: 'Панель отвечает: нод {0}, на связи {1}, онлайн {2}', test_err: 'Не получилось',
    rw_ok: 'Подключено · {0}', rw_err: 'Ошибка', rw_wait: 'Ещё не опрашивалась', rw_saved_hint: 'После сохранения хаб подхватит панель в течение 30 секунд.',
    drop: 'Резкое падение онлайна', drop_desc: 'Если на ноде сидела тысяча человек, а через минуту остались единицы, нода жива, но клиенты до неё не доходят. Так выглядит блокировка ТСПУ. Тревога уходит в Telegram сразу.',
    drop_on: 'Следить за онлайном на нодах', percent: 'Падение от', percent_hint: 'Насколько онлайн должен упасть от обычного уровня',
    min_users: 'Нода от', min_users_hint: 'Меньшие ноды слишком шумные, их не трогаем', users: 'польз.',
    window: 'Обычный уровень за', window_hint: 'Медиана онлайна за это время до падения',
    how: 'Как считается: обычный уровень — медиана онлайна за окно. Тревога, если онлайн ниже порога и при этом доля ноды во всём онлайне упала так же: вечерний спад задевает все ноды и доли не меняет, а бан отнимает онлайн у одной. Плавный отток ловится за минуту. Обрыв в ноль за один опрос должен продержаться 5 минут: так выглядят моргания панели и перезапуск xray, а не бан. Если панель потеряла связь с нодой дольше 3 минут, это отдельная тревога «Нода не на связи с панелью», а не «ушли пользователи»; если на этом сервере стоит агент с тем же именем, хватит его тревоги. Пять минут после возврата ноды онлайн не оценивается: клиенты переподключаются. Отбой, когда вернулась хотя бы половина.',
    sync: 'Синхронизация с панелью', sync_desc: 'Что делать, когда ноду удалили в Remnawave.',
    prune_title: 'Ставить на паузу то, что удалили в панели?', prune_confirm: 'Включить',
    off_title: 'Отключить Remnawave?', off_confirm: 'Отключить',
    off_body: 'После сохранения хаб удалит адрес панели и API-ключ, и данные панели перестанут обновляться. Чтобы подключить снова, ключ придётся ввести заново. До сохранения это можно отменить.',
    prune: 'Ставить на паузу проверки нод, удалённых из панели',
    prune_hint: 'Проверки ноды встают на паузу с меткой node-deleted, когда нода пропадает из панели на три опроса подряд (1,5 минуты), и снимаются с паузы, если она вернётся. Нода, пересозданная с тем же именем или адресом, не считается удалённой. Пустой ответ панели или исчезновение большинства нод сразу ничего не трогают. Серверы, токены агентов и объекты из config.yaml не меняются.',
    m15: '15 мин', m30: '30 мин', h1: '1 час', h2: '2 часа', h3: '3 часа',
    need_rw: 'Сначала подключите панель.',
  },
  en: {
    title: 'Remnawave', subtitle: 'Connect the panel with an API key, and get alerted when users leave a node',
    rw: 'Connection', rw_desc: 'Every 30 seconds the hub reads nodes, online users, traffic and load from the panel. All it needs is the panel URL and an API key.',
    enabled: 'Enabled', rw_url: 'Panel URL', rw_url_hint: 'The address you open in the browser', rw_token: 'API key',
    rw_token_hint: 'Remnawave → Settings → API tokens → Create', token_kept: 'Saved. Leave empty to keep it',
    headers: 'Extra headers', headers_hint: 'When the panel sits behind Cloudflare Access, a basic-auth proxy or a secret header',
    test: 'Test connection', test_ok: 'The panel answers: {0} nodes, {1} connected, {2} online', test_err: 'It did not work',
    rw_ok: 'Connected · {0}', rw_err: 'Error', rw_wait: 'Not polled yet', rw_saved_hint: 'After saving, the hub picks the panel up within 30 seconds.',
    drop: 'Sudden drop in users', drop_desc: 'When a node had a thousand users and a minute later has a handful, the node is alive but clients no longer reach it. That is how a DPI ban looks. The alert goes to Telegram at once.',
    drop_on: 'Watch users online on nodes', percent: 'Drop of at least', percent_hint: 'How far users online must fall from the usual level',
    min_users: 'Nodes from', min_users_hint: 'Smaller nodes are too noisy to judge', users: 'users',
    window: 'Usual level over', window_hint: 'The median of users online over this time before the drop',
    how: 'How it works: the usual level is the median of users online over the window. The alert fires when users are under the line and the node\x27s share of all users fell as much: the evening lowers every node and keeps the shares, a ban takes users from one. A slow drain is caught within a minute. A fall to zero in one poll must hold five minutes: that is how panel hiccups and xray restarts look, not bans. A node the panel has lost for over three minutes is its own alert, "Node lost by the panel", not "users gone"; if an agent of the same name runs on that server, its alert is enough. For five minutes after a node is back users are not judged while clients reconnect. It clears once at least half are back.',
    sync: 'Sync with the panel', sync_desc: 'What to do when a node is deleted in Remnawave.',
    prune_title: 'Pause what is deleted in the panel?', prune_confirm: 'Turn on',
    off_title: 'Disconnect Remnawave?', off_confirm: 'Disconnect',
    off_body: 'On save the hub deletes the panel URL and API key, and the panel data stops updating. To connect again you will have to enter the key again. Until you save, this can be undone.',
    prune: 'Pause the checks of nodes deleted from the panel',
    prune_hint: 'A node\'s checks are paused and tagged node-deleted once it has been missing from the panel for three polls in a row (90 seconds), and resumed if it comes back. A node recreated with the same name or address does not count as deleted. An empty answer or most nodes vanishing at once changes nothing. Servers, agent tokens and objects from config.yaml are left alone.',
    m15: '15 min', m30: '30 min', h1: '1 hour', h2: '2 hours', h3: '3 hours',
    need_rw: 'Connect the panel first.',
  },
};

type Rw = NonNullable<HubConfig['remnawave']>;
type Check = { nodes: number; online: number; users: number };

const WINDOWS = [['15m', 'm15'], ['30m', 'm30'], ['1h', 'h1'], ['2h', 'h2'], ['3h', 'h3']] as const;

export default function RemnawaveSettings() {
  const t = useT(L);
  const c = useCommon();
  const { lang } = useLang();
  const s = useSettingsDraft();
  const rwq = useRemnawave();
  const { draft, update, managed, server } = s;
  const lastRw = useRef<Rw | null>(null);
  const [testing, setTesting] = useState(false);
  const [check, setCheck] = useState<{ ok: Check } | { err: string } | null>(null);

  if (s.query.isError) {
    return (<><PageHeader title={t('title')} /><Alert color="red">{s.query.error.message}</Alert></>);
  }
  if (!draft) {
    return (
      <>
        <PageHeader title={t('title')} subtitle={t('subtitle')} />
        <Stack gap="lg">{[0, 1].map((i) => <Skeleton key={i} h={220} radius="lg" />)}</Stack>
      </>
    );
  }

  const rw = draft.remnawave;
  const m = managed.remnawave;
  const rwTokenSaved = server?.config.remnawave?.token === MASK;
  const setRw = (fn: (r: Rw) => void) => update((d) => { if (d.remnawave) fn(d.remnawave); });
  const rwState = rwq.data;
  const drop = rw?.drop ?? {};
  const setDrop = (patch: Partial<NonNullable<Rw['drop']>>) => setRw((r) => { r.drop = { ...(r.drop ?? {}), ...patch }; });
  const win = compactDuration(drop.window ?? '1h') || '1h';

  const test = async () => {
    if (!rw) return;
    setTesting(true);
    setCheck(null);
    try {
      const ok = await api<Check>('POST', '/api/v1/settings/remnawave/test', { url: rw.url, token: rw.token, headers: rw.headers });
      setCheck({ ok });
    } catch (e) {
      setCheck({ err: e instanceof ApiError ? e.message : String(e) });
    } finally {
      setTesting(false);
    }
  };

  return (
    <>
      <PageHeader title={t('title')} subtitle={t('subtitle')} />
      <Stack gap="lg">
        <SettingsCard
          icon={IconAffiliate}
          title={t('rw')}
          description={t('rw_desc')}
          managed={m}
          right={
            <Switch
              label={t('enabled')}
              checked={!!rw}
              disabled={m}
              onChange={(e) => {
                const on = e.currentTarget.checked;
                const apply = () => update((d) => {
                  if (on) d.remnawave = lastRw.current ?? { url: '', token: '' };
                  else { lastRw.current = d.remnawave ?? null; d.remnawave = null; }
                });
                if (!on && server?.config.remnawave) {
                  confirmThen({ title: t('off_title'), body: t('off_body'), confirm: t('off_confirm'), cancel: c('cancel'), onConfirm: apply });
                } else apply();
              }}
            />
          }
        >
          {server?.config.remnawave && rwState?.configured && (
            <Group gap={8} wrap="nowrap" align="flex-start">
              <Dot color={rwState.error ? status.down : rwState.fetched ? status.ok : status.unknown} pulse={!rwState.error && !!rwState.fetched} />
              <Text size="sm" style={{ marginTop: -4, wordBreak: 'break-word' }}>
                {rwState.error ? (<><b>{t('rw_err')}:</b> {rwState.error}</>) : rwState.fetched ? t('rw_ok', ago(rwState.fetched, lang)) : t('rw_wait')}
              </Text>
            </Group>
          )}
          {rw && (
            <>
              <SimpleGrid cols={{ base: 1, md: 2 }} spacing="md">
                <TextInput
                  label={t('rw_url')}
                  description={t('rw_url_hint')}
                  placeholder="https://panel.example.com"
                  value={rw.url}
                  disabled={m}
                  onChange={(e) => { const v = e.currentTarget.value.trim(); setRw((r) => { r.url = v; }); }}
                  styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)', fontSize: 13 } }}
                />
                <PasswordInput
                  label={t('rw_token')}
                  description={t('rw_token_hint')}
                  placeholder={rw.token === MASK ? t('token_kept') : 'eyJhbGciOi…'}
                  value={rw.token === MASK ? '' : rw.token}
                  disabled={m}
                  onChange={(e) => { const v = e.currentTarget.value.trim(); setRw((r) => { r.token = v || (rwTokenSaved ? MASK : ''); }); }}
                  autoComplete="off"
                  styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)', fontSize: 13 } }}
                />
              </SimpleGrid>
              <KeyValueEditor
                label={t('headers')}
                description={t('headers_hint')}
                value={rw.headers}
                disabled={m}
                onChange={(h) => setRw((r) => { r.headers = Object.keys(h).length ? h : undefined; })}
              />
              <Group gap="sm" wrap="wrap">
                <Button variant="light" leftSection={<IconPlugConnected size={16} />} loading={testing} disabled={!rw.url || (!rw.token && !rwTokenSaved)} onClick={test}>
                  {t('test')}
                </Button>
                {check && 'ok' in check && (
                  <Group gap={6} wrap="nowrap" c="teal">
                    <IconCircleCheck size={16} />
                    <Text size="sm">{t('test_ok', check.ok.nodes, check.ok.online, num(check.ok.users))}</Text>
                  </Group>
                )}
                {check && 'err' in check && <Text size="sm" c="red"><b>{t('test_err')}:</b> {check.err}</Text>}
              </Group>
              {s.dirty && JSON.stringify(rw) !== JSON.stringify(server?.config.remnawave ?? null) && (
                <Text size="xs" c="dimmed">{t('rw_saved_hint')}</Text>
              )}
            </>
          )}
        </SettingsCard>

        <SettingsCard icon={IconTrendingDown} title={t('drop')} description={t('drop_desc')} managed={m}>
          {!rw ? <Text size="sm" c="dimmed">{t('need_rw')}</Text> : (
            <>
              <Switch label={t('drop_on')} checked={!drop.off} disabled={m} onChange={(e) => setDrop({ off: !e.currentTarget.checked })} />
              <SimpleGrid cols={{ base: 1, sm: 3 }} spacing="md">
                <NumberInput
                  label={t('percent')} description={t('percent_hint')} value={drop.percent ?? 80} min={10} max={100} step={5} suffix="%"
                  disabled={m || drop.off} onChange={(v) => typeof v === 'number' && setDrop({ percent: v })}
                />
                <NumberInput
                  label={t('min_users')} description={t('min_users_hint')} value={drop.min_users ?? 30} min={1} step={10}
                  suffix={` ${t('users')}`} disabled={m || drop.off} onChange={(v) => typeof v === 'number' && setDrop({ min_users: v })}
                />
                <Select
                  label={t('window')} description={t('window_hint')} value={win} allowDeselect={false} disabled={m || drop.off}
                  data={WINDOWS.map(([v, k]): { value: string; label: string } => ({ value: v, label: t(k) })).concat(WINDOWS.some(([v]) => v === win) ? [] : [{ value: win, label: win }])}
                  onChange={(v) => v && setDrop({ window: v })}
                />
              </SimpleGrid>
              <Text size="xs" c="dimmed">{t('how')}</Text>
            </>
          )}
        </SettingsCard>

        <SettingsCard icon={IconRefresh} title={t('sync')} description={t('sync_desc')} managed={m}>
          {!rw ? <Text size="sm" c="dimmed">{t('need_rw')}</Text> : (
            <Switch label={t('prune')} description={t('prune_hint')} checked={!!rw.prune} disabled={m}
              onChange={(e) => {
                const on = e.currentTarget.checked;
                const apply = () => setRw((r) => { r.prune = on || undefined; });
                // Pruning deletes servers and checks with their history: switching it on deserves a second look.
                if (on) confirmThen({ title: t('prune_title'), body: t('prune_hint'), confirm: t('prune_confirm'), cancel: c('cancel'), onConfirm: apply });
                else apply();
              }} />
          )}
        </SettingsCard>

        <SaveBar dirty={s.dirty} saving={s.saving} onSave={s.save} onReset={s.reset} />
      </Stack>
    </>
  );
}
