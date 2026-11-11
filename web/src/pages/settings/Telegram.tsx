import {
  ActionIcon, Alert, Anchor, Box, Button, Checkbox, Group, List, NumberInput, PasswordInput, Select, SimpleGrid, Skeleton, Stack, Switch, Table,
  TagsInput, Text, TextInput, Tooltip,
} from '@mantine/core';
import { IconFilter, IconInfoCircle, IconSearch, IconSend } from '@tabler/icons-react';
import { useRef, useState } from 'react';
import { Link } from 'react-router';
import { api, useChecks, type HubConfig, type TelegramRoute } from '../../api';
import { TelegramRules } from './TelegramRules';
import { notifyError, notifyOk, PageHeader } from '../../components/Misc';
import { MASK, SaveBar, SettingsCard, useSettingsDraft } from '../../components/SettingsKit';
import { TelegramLogo } from '../../components/TelegramLogo';
import { confirmThen } from '../../components/guards';
import { useCommon, useT } from '../../i18n';

const L = {
  ru: {
    title: 'Telegram', subtitle: 'Бот, чат и куда приходит каждое событие',
    bot: 'Бот и основной чат', bot_desc: 'Одно сообщение на инцидент: оно обновляется по ходу и закрывается при восстановлении.', enabled: 'Включено',
    off_title: 'Выключить Telegram?', off_confirm: 'Выключить',
    off_body: 'После сохранения хаб удалит токен бота, чаты, темы и правила. Чтобы включить снова, токен придётся ввести заново. До сохранения выключение можно отменить.',
    how: 'Как подключить', how1: 'Создайте бота у @BotFather и вставьте токен.', how2: 'Добавьте бота в группу администратором: иначе он не увидит сообщения и темы.',
    how3: 'Напишите в группу, а если нужны темы, то в каждую из них. Нажмите «Найти чаты и темы».', how4: 'Выберите основной чат, сохраните и отправьте тест.',
    token: 'Токен бота', token_kept: 'Сохранён. Оставьте пустым, чтобы не менять', chat: 'Основной чат', chat_hint: 'Группы и каналы начинаются с -100',
    find: 'Найти чаты и темы', find_none: 'Бот @{0} пока не видит чатов. Напишите в чат и попробуйте ещё раз', find_pick: 'Бот @{0} видит эти чаты. Нажмите, чтобы сделать основным:',
    topics_found: 'тем: {0}', test: 'Отправить тест', test_ok: 'Отправлено, проверьте чат', test_dirty: 'Сначала сохраните изменения',
    tag_rules: 'Правила по меткам', tag_rules_desc: 'Проверяются сверху вниз до маршрутов событий: первое подходящее правило решает, куда уйдёт сообщение. Например, всё с меткой critical — в отдельную тему со звуком.',
    routes: 'События', routes_desc: 'Разные события можно отправлять в разные темы и чаты. Пусто значит основной чат без темы.',
    topic_how: 'Как указать тему: в Telegram откройте тему, нажмите на любое сообщение в ней → «Копировать ссылку» и вставьте ссылку в колонку «Тема». Хаб сам возьмёт из неё чат и номер темы.',
    topic_ph: 'номер или ссылка', looks_topic: 'Похоже на номер темы: id группы начинается с -100', move_topic: 'В тему',
    link_public: 'По ссылке на публичную группу не узнать её id: выберите чат через «Найти чаты и темы» или впишите id',
    err_chat: 'Бот не видит этот чат. Проверьте id (у групп он начинается с -100) и что бот добавлен в группу.',
    err_thread: 'В этом чате нет такой темы. Проверьте номер темы или вставьте ссылку на неё заново.',
    err_rights: 'У бота нет прав писать сюда. Сделайте его администратором группы.',
    err_kicked: 'Бота удалили из этого чата или заблокировали. Добавьте его снова.',
    err_token: 'Telegram не принял токен бота. Проверьте его в @BotFather.',
    ev: 'Событие', on: 'Вкл', route_chat: 'Чат', main_chat: 'основной', topic: 'Тема', no_topic: 'без темы', silent: 'Без звука', try: 'Отправить тест в это место',
    g_problems: 'Проверки', g_servers: 'Серверы', g_flow: 'Ход инцидента',
    e_down: '🔴 Не работает', d_down: 'не отвечает ни с одной пробы', e_blocked: '🟠 Блок в стране', d_blocked: 'из одной страны не работает, из других работает',
    e_partial: '🟡 Частично', d_partial: 'сбой у части проб', e_subscription: '📡 Подписка', d_subscription: 'хаб не может скачать подписку',
    e_probe_offline: '🔌 Сервер офлайн', d_probe_offline: 'агент не на связи с хабом или нода с панелью Remnawave', e_probe_no_net: '🛰 Нет интернета', d_probe_no_net: 'у сервера пропал интернет', e_host: '📈 Порог метрики', d_host: 'ЦП, память, диск, steal или iowait дольше порога',
    g_rw: 'Remnawave', e_users_drop: '📉 Ушли пользователи', d_users_drop: 'онлайн на ноде резко упал: похоже на бан ТСПУ',
    e_resolved: '🟢 Восстановление', d_resolved: 'проблема ушла', e_remind: '⏰ Напоминание', d_remind: 'никто не принял инцидент', e_report: '📊 Недельный отчёт', d_report: 'воскресенье, по умолчанию 20:00',
    other_rules: 'Тихие часы и частота напоминаний настраиваются в разделе', rules: 'Правила', lang_note: 'Язык сообщений меняется в разделе', general: 'Общие',
    advanced: 'Если Telegram недоступен с сервера', api_url: 'Адрес Bot API', api_url_hint: 'Свой Bot API-сервер или зеркало', proxy: 'Прокси', proxy_hint: 'Через него хаб ходит в Telegram',
    admins: 'Кто может закрывать и глушить из чата', admins_hint: 'Telegram user id через Enter. «Принял» может нажать любой участник чата, а «Решено» и «Заглушить» — только эти люди; пока список пуст, эти кнопки не работают. Свой id покажет бот @userinfobot', admins_ph: 'например 123456789',
  },
  en: {
    title: 'Telegram', subtitle: 'The bot, the chat, and where each event goes',
    bot: 'Bot and main chat', bot_desc: 'One message per incident: it updates as things change and closes on recovery.', enabled: 'Enabled',
    off_title: 'Turn Telegram off?', off_confirm: 'Turn off',
    off_body: 'On save the hub deletes the bot token, chats, topics and rules. To turn it back on you will have to enter the token again. Until you save, this can be undone.',
    how: 'How to connect', how1: 'Create a bot with @BotFather and paste its token.', how2: 'Add the bot to the group as an admin, or it will not see messages and topics.',
    how3: 'Post in the group and, if you use topics, in each of them. Press Find chats and topics.', how4: 'Pick the main chat, save and send a test.',
    token: 'Bot token', token_kept: 'Saved. Leave empty to keep it', chat: 'Main chat', chat_hint: 'Groups and channels start with -100',
    find: 'Find chats and topics', find_none: 'Bot @{0} sees no chats yet. Post in a chat and try again', find_pick: 'Bot @{0} sees these chats. Press one to make it the main chat:',
    topics_found: 'topics: {0}', test: 'Send test', test_ok: 'Sent, check the chat', test_dirty: 'Save your changes first',
    tag_rules: 'Rules by tag', tag_rules_desc: 'Checked top to bottom before the event routes: the first rule that matches decides where a message goes. For example, everything tagged critical to its own topic, with sound.',
    routes: 'Events', routes_desc: 'Send different events to different topics and chats. Empty means the main chat, no topic.',
    topic_how: 'To pick a topic: open it in Telegram, tap any message in it → Copy link, and paste the link into the Topic column. The hub takes the chat and the topic number from it.',
    topic_ph: 'number or link', looks_topic: 'Looks like a topic number: group ids start with -100', move_topic: 'To topic',
    link_public: 'A link to a public group does not carry its id: pick the chat with Find chats and topics or type the id',
    err_chat: 'The bot cannot see this chat. Check the id (group ids start with -100) and that the bot is in the group.',
    err_thread: 'There is no such topic in this chat. Check the topic number or paste its link again.',
    err_rights: 'The bot may not post here. Make it an admin of the group.',
    err_kicked: 'The bot was removed from this chat or blocked. Add it again.',
    err_token: 'Telegram rejected the bot token. Check it with @BotFather.',
    ev: 'Event', on: 'On', route_chat: 'Chat', main_chat: 'main', topic: 'Topic', no_topic: 'no topic', silent: 'Silent', try: 'Send a test to this place',
    g_problems: 'Checks', g_servers: 'Servers', g_flow: 'Incident flow',
    e_down: '🔴 Down', d_down: 'fails from every probe', e_blocked: '🟠 Blocked in a country', d_blocked: 'fails from one country, works from others',
    e_partial: '🟡 Partial', d_partial: 'fails from some probes', e_subscription: '📡 Subscription', d_subscription: 'the hub cannot download it',
    e_probe_offline: '🔌 Server offline', d_probe_offline: 'the agent lost the hub, or the Remnawave panel lost a node', e_probe_no_net: '🛰 No internet', d_probe_no_net: 'the server lost internet', e_host: '📈 Metric threshold', d_host: 'CPU, memory, disk, steal or iowait past the threshold',
    g_rw: 'Remnawave', e_users_drop: '📉 Users gone', d_users_drop: 'users online on a node collapsed: looks like a DPI ban',
    e_resolved: '🟢 Recovery', d_resolved: 'the problem is gone', e_remind: '⏰ Reminder', d_remind: 'nobody acknowledged the incident', e_report: '📊 Weekly report', d_report: 'Sunday, 20:00 by default',
    other_rules: 'Quiet hours and reminder frequency are in', rules: 'Rules', lang_note: 'The message language is in', general: 'General',
    advanced: 'If Telegram is unreachable from the server', api_url: 'Bot API URL', api_url_hint: 'Your own Bot API server or a mirror', proxy: 'Proxy', proxy_hint: 'The hub reaches Telegram through it',
    admins: 'Who may resolve and mute from the chat', admins_hint: 'Telegram user ids, press Enter after each. Anyone in the chat may acknowledge; Resolved and Mute work only for these people, and not at all while the list is empty. @userinfobot shows your id', admins_ph: 'e.g. 123456789',
  },
};

type Tg = NonNullable<HubConfig['telegram']>;
type FoundChat = { id: number; title: string; type: string; topics?: { id: number; name?: string }[] };

const GROUPS = [
  { key: 'g_problems', events: ['down', 'blocked', 'partial', 'subscription'] },
  { key: 'g_servers', events: ['probe_offline', 'probe_no_net', 'host'] },
  { key: 'g_rw', events: ['users_drop'] },
  { key: 'g_flow', events: ['resolved', 'remind', 'report'] },
] as const;

const mono = { input: { fontFamily: 'var(--mantine-font-family-monospace)' } };

/**
 * parseTopicLink reads a Telegram link to a topic or to a message in it:
 * t.me/c/<chat>/<topic>[/<message>] gives both, a public t.me/<name>/<topic>/<message>
 * only the topic (its chat id is not in the link).
 */
export function parseTopicLink(text: string): { chat?: number; topic: number } | null {
  const u = text.trim().replace(/^https?:\/\//, '').replace(/^(www\.)?telegram\.me\//, 't.me/');
  let m = u.match(/^t\.me\/c\/(\d+)\/(\d+)(?:\/\d+)?\/?(?:\?.*)?$/);
  if (m) return { chat: Number(`-100${m[1]}`), topic: Number(m[2]) };
  m = u.match(/^t\.me\/[A-Za-z][\w]{3,}\/(\d+)\/\d+\/?(?:\?.*)?$/);
  if (m) return { topic: Number(m[1]) };
  return null;
}

/** A positive id this small is not a group: it is most likely a topic number typed into the chat column. */
const looksLikeTopic = (id?: number) => !!id && id > 0 && id < 10_000_000;

function num(v: string | number): number | undefined {
  const n = typeof v === 'number' ? v : parseInt(v, 10);
  return Number.isFinite(n) && n !== 0 ? n : undefined;
}

export default function Telegram() {
  const t = useT(L);
  const c = useCommon();
  const s = useSettingsDraft();
  const { draft, update, managed, server } = s;
  const lastTg = useRef<Tg | null>(null);
  const checks = useChecks({ live: false });
  const [testing, setTesting] = useState('');
  const [finding, setFinding] = useState(false);
  const [found, setFound] = useState<{ bot: string; chats: FoundChat[] } | null>(null);

  if (s.query.isError) return <><PageHeader title={t('title')} /><Alert color="red">{s.query.error.message}</Alert></>;
  if (!draft) return <><PageHeader title={t('title')} /><Skeleton h={420} radius="lg" /></>;

  const tg = draft.telegram;
  const tokenSaved = server?.config.telegram?.token === MASK;
  const setTg = (fn: (x: Tg) => void) => update((d) => { if (d.telegram) fn(d.telegram); });
  const setRoute = (ev: string, patch: Partial<TelegramRoute>) => setTg((x) => {
    const r: TelegramRoute = { ...x.routes?.[ev], ...patch };
    const routes = { ...x.routes };
    for (const k of Object.keys(r) as (keyof TelegramRoute)[]) if (!r[k]) delete r[k];
    if (Object.keys(r).length) routes[ev] = r; else delete routes[ev];
    x.routes = Object.keys(routes).length ? routes : undefined;
  });

  const test = async (event = '') => {
    setTesting(event || 'main');
    try {
      await api('POST', `/api/v1/settings/telegram/test${event ? `?event=${event}` : ''}`);
      notifyOk(t('test_ok'));
    } catch (e) {
      const msg = String((e as Error)?.message ?? e).toLowerCase();
      const hint = msg.includes('thread not found') || msg.includes('topic') ? 'err_thread'
        : msg.includes('chat not found') ? 'err_chat'
        : msg.includes('not enough rights') || msg.includes('have no rights') || msg.includes('administrator') ? 'err_rights'
        : msg.includes('kicked') || msg.includes('blocked') || msg.includes('not a member') ? 'err_kicked'
        : msg.includes('unauthorized') || msg.includes('401') ? 'err_token' : '';
      notifyError(hint ? new Error(`${t(hint)} (${(e as Error).message})`) : e);
    } finally {
      setTesting('');
    }
  };
  const findChats = async () => {
    setFinding(true);
    try {
      setFound(await api('POST', '/api/v1/settings/telegram/chats', { token: tg?.token, api_url: tg?.api_url, proxy: tg?.proxy }));
    } catch (e) {
      notifyError(e);
    } finally {
      setFinding(false);
    }
  };

  const saved = !!server?.config.telegram && !s.dirty;
  const chatData = (found?.chats ?? []).map((c) => ({ value: String(c.id), label: c.title || String(c.id) }));

  return (
    <>
      <PageHeader title={t('title')} subtitle={t('subtitle')} />
      <Stack gap="lg">
        <SettingsCard
          icon={TelegramLogo as never}
          title={t('bot')}
          description={t('bot_desc')}
          managed={managed.telegram}
          footer={server?.config.telegram && (
            <Group>
              <Tooltip label={t('test_dirty')} disabled={!s.dirty}>
                <Button variant="light" leftSection={<IconSend size={16} />} onClick={() => test()} loading={testing === 'main'} disabled={s.dirty}>{t('test')}</Button>
              </Tooltip>
            </Group>
          )}
          right={
            <Switch
              label={t('enabled')}
              checked={!!tg}
              disabled={managed.telegram}
              onChange={(e) => {
                const on = e.currentTarget.checked;
                const apply = () => update((d) => {
                  if (on) d.telegram = lastTg.current ?? { token: '', chat_id: 0 };
                  else { lastTg.current = d.telegram ?? null; d.telegram = null; }
                });
                // Saved with the bot off, the hub drops the token: say so while it can still be kept.
                if (!on && server?.config.telegram) {
                  confirmThen({ title: t('off_title'), body: t('off_body'), confirm: t('off_confirm'), cancel: c('cancel'), onConfirm: apply });
                } else apply();
              }}
            />
          }
        >
          {tg && (
            <Stack gap="md">
              {!tokenSaved && (
                <Box>
                  <Text size="sm" fw={600} mb={4}>{t('how')}</Text>
                  <List type="ordered" size="sm" spacing={2}>
                    {(['how1', 'how2', 'how3', 'how4'] as const).map((k) => <List.Item key={k}>{t(k)}</List.Item>)}
                  </List>
                </Box>
              )}
              <SimpleGrid cols={{ base: 1, md: 2 }} spacing="md">
                <PasswordInput
                  label={t('token')}
                  placeholder={tg.token === MASK ? t('token_kept') : '123456789:AAE…'}
                  value={tg.token === MASK ? '' : tg.token}
                  onChange={(e) => { const v = e.currentTarget.value; setTg((x) => { x.token = v || (tokenSaved ? MASK : ''); }); }}
                  autoComplete="off"
                  styles={mono}
                />
                <NumberInput
                  label={t('chat')}
                  description={t('chat_hint')}
                  placeholder="-1001234567890"
                  value={tg.chat_id || ''}
                  onChange={(v) => setTg((x) => { x.chat_id = num(v) ?? 0; })}
                  allowDecimal={false}
                  hideControls
                  styles={mono}
                />
              </SimpleGrid>
              <Stack gap={6}>
                <Group>
                  <Button variant="light" size="xs" leftSection={<IconSearch size={14} />} loading={finding} disabled={!tg.token || managed.telegram} onClick={findChats}>{t('find')}</Button>
                </Group>
                {found && (
                  <>
                    <Text size="sm">{t(found.chats.length ? 'find_pick' : 'find_none', found.bot)}</Text>
                    <Group gap="xs">
                      {found.chats.map((ch) => (
                        <Button key={ch.id} size="xs" variant={tg.chat_id === ch.id ? 'filled' : 'default'} onClick={() => setTg((x) => { x.chat_id = ch.id; })}>
                          {ch.title || ch.id} · {ch.type}{ch.topics?.length ? ` · ${t('topics_found', ch.topics.length)}` : ''}
                        </Button>
                      ))}
                    </Group>
                  </>
                )}
              </Stack>
              <TagsInput
                label={t('admins')}
                description={t('admins_hint')}
                placeholder={tg.admins?.length ? '' : t('admins_ph')}
                value={(tg.admins ?? []).map(String)}
                onChange={(v) => setTg((x) => {
                  const ids = v.map((a) => parseInt(a.trim(), 10)).filter((n) => Number.isFinite(n));
                  x.admins = ids.length ? [...new Set(ids)] : undefined;
                })}
                splitChars={[',', ' ']}
                clearable
              />
            </Stack>
          )}
        </SettingsCard>

        {tg && (
          <SettingsCard icon={IconFilter} title={t('tag_rules')} description={t('tag_rules_desc')} managed={managed.telegram}>
            <TelegramRules
              rules={tg.rules ?? []}
              onChange={(rules) => setTg((x) => { x.rules = rules.length ? rules : undefined; })}
              tags={[...new Set((checks.data?.checks ?? []).flatMap((c) => c.tags ?? []))].sort()}
              groups={[...new Set((checks.data?.checks ?? []).map((c) => c.group ?? '').filter(Boolean))].sort()}
              events={GROUPS.flatMap((g) => [...g.events])}
              eventLabel={(ev) => t(`e_${ev}` as 'title')}
              disabled={managed.telegram}
            />
          </SettingsCard>
        )}

        {tg && (
          <SettingsCard icon={IconSend} title={t('routes')} description={t('routes_desc')} managed={managed.telegram}>
            <Alert variant="light" color="blue" icon={<IconInfoCircle size={18} />} py="xs">
              <Text size="sm">{t('topic_how')}</Text>
            </Alert>
            <Box className="uf-scroll-x">
              <Table className="uf-table" verticalSpacing={6} horizontalSpacing="xs" miw={760}>
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>{t('ev')}</Table.Th>
                    <Table.Th w={56}>{t('on')}</Table.Th>
                    <Table.Th w={190}>{t('route_chat')}</Table.Th>
                    <Table.Th w={190}>{t('topic')}</Table.Th>
                    <Table.Th w={84}>{t('silent')}</Table.Th>
                    <Table.Th w={44} />
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {GROUPS.flatMap((g) => [
                    <Table.Tr key={g.key} className="uf-mx-group"><Table.Td colSpan={6}><Text size="xs" fw={700} c="dimmed" tt="uppercase">{t(g.key)}</Text></Table.Td></Table.Tr>,
                    ...g.events.map((ev) => {
                      const r = tg.routes?.[ev] ?? {};
                      const chatId = r.chat_id || tg.chat_id;
                      const topics = found?.chats.find((c) => c.id === chatId)?.topics ?? [];
                      const topicData = topics.map((x) => ({ value: String(x.id), label: x.name ? `${x.name} · ${x.id}` : String(x.id) }));
                      if (r.topic && !topics.some((x) => x.id === r.topic)) topicData.push({ value: String(r.topic), label: String(r.topic) });
                      return (
                        <Table.Tr key={ev} style={r.off ? { opacity: 0.55 } : undefined}>
                          <Table.Td>
                            <Text size="sm" fw={600}>{t(`e_${ev}`)}</Text>
                            <Text size="xs" c="dimmed">{t(`d_${ev}`)}</Text>
                          </Table.Td>
                          <Table.Td><Switch size="sm" aria-label={t('on')} checked={!r.off} onChange={(e) => setRoute(ev, { off: !e.currentTarget.checked })} /></Table.Td>
                          <Table.Td>
                            {chatData.length > 0 ? (
                              <Select size="xs" clearable placeholder={t('main_chat')} data={r.chat_id && !chatData.some((c) => c.value === String(r.chat_id)) ? [...chatData, { value: String(r.chat_id), label: String(r.chat_id) }] : chatData} value={r.chat_id ? String(r.chat_id) : null} onChange={(v) => setRoute(ev, { chat_id: num(v ?? ''), topic: undefined })} />
                            ) : (
                              <Group gap={4} wrap="nowrap">
                                <Tooltip label={t('looks_topic')} disabled={!looksLikeTopic(r.chat_id)}>
                                  <NumberInput size="xs" hideControls allowDecimal={false} placeholder={t('main_chat')} value={r.chat_id ?? ''}
                                    error={looksLikeTopic(r.chat_id)} onChange={(v) => setRoute(ev, { chat_id: num(v) })} styles={mono} style={{ flex: 1 }} />
                                </Tooltip>
                                {looksLikeTopic(r.chat_id) && (
                                  <Button size="compact-xs" variant="light" color="orange" onClick={() => setRoute(ev, { topic: r.chat_id, chat_id: undefined })}>{t('move_topic')}</Button>
                                )}
                              </Group>
                            )}
                          </Table.Td>
                          <Table.Td>
                            {topics.length > 0 ? (
                              <Select size="xs" clearable placeholder={t('no_topic')} data={topicData} value={r.topic ? String(r.topic) : null} onChange={(v) => setRoute(ev, { topic: num(v ?? '') })} />
                            ) : (
                              <TextInput size="xs" placeholder={t('topic_ph')} value={r.topic ? String(r.topic) : ''} styles={mono}
                                onChange={(e) => {
                                  const v = e.currentTarget.value.trim();
                                  const link = parseTopicLink(v);
                                  if (link) setRoute(ev, link.chat ? { chat_id: link.chat, topic: link.topic } : { topic: link.topic });
                                  else if (/^\d*$/.test(v)) setRoute(ev, { topic: num(v) });
                                }}
                                onPaste={(e) => {
                                  const link = parseTopicLink(e.clipboardData.getData('text'));
                                  if (!link) return;
                                  e.preventDefault();
                                  setRoute(ev, link.chat ? { chat_id: link.chat, topic: link.topic } : { topic: link.topic });
                                  if (!link.chat) notifyOk(t('link_public'));
                                }} />
                            )}
                          </Table.Td>
                          <Table.Td><Checkbox size="sm" aria-label={t('silent')} checked={!!r.silent} onChange={(e) => setRoute(ev, { silent: e.currentTarget.checked })} /></Table.Td>
                          <Table.Td>
                            <Tooltip label={saved ? t('try') : t('test_dirty')}>
                              <ActionIcon variant="subtle" color="gray" aria-label={t('try')} disabled={!saved || !!r.off} loading={testing === ev} onClick={() => test(ev)}><IconSend size={15} /></ActionIcon>
                            </Tooltip>
                          </Table.Td>
                        </Table.Tr>
                      );
                    }),
                  ])}
                </Table.Tbody>
              </Table>
            </Box>
            <Text size="xs" c="dimmed" mt="sm">
              {t('other_rules')} <Anchor component={Link} to="/settings/notifications" inherit>{t('rules')}</Anchor>. {t('lang_note')} <Anchor component={Link} to="/settings/general" inherit>{t('general')}</Anchor>.
            </Text>
          </SettingsCard>
        )}

        {tg && (
          <SettingsCard icon={IconSearch} title={t('advanced')} managed={managed.telegram}>
            <SimpleGrid cols={{ base: 1, md: 2 }} spacing="md">
              <TextInput label={t('api_url')} description={t('api_url_hint')} placeholder="https://api.telegram.org" value={tg.api_url ?? ''} onChange={(e) => { const v = e.currentTarget.value; setTg((x) => { x.api_url = v || undefined; }); }} />
              <TextInput label={t('proxy')} description={t('proxy_hint')} placeholder="socks5://user:pass@host:1080" value={tg.proxy ?? ''} onChange={(e) => { const v = e.currentTarget.value; setTg((x) => { x.proxy = v || undefined; }); }} />
            </SimpleGrid>
          </SettingsCard>
        )}
        <SaveBar dirty={s.dirty} saving={s.saving} onSave={s.save} onReset={s.reset} />
      </Stack>
    </>
  );
}
