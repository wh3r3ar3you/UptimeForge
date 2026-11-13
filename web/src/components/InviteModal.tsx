import { Alert, Button, Group, Modal, Stack, Switch, Text, TextInput } from '@mantine/core';
import { IconAlertCircle, IconAlertTriangle, IconClockHour4 } from '@tabler/icons-react';
import { useState } from 'react';
import { api, ApiError, queryClient, type InviteCreated } from '../api';
import { ago, clock } from '../format';
import { useCommon, useLang, useT } from '../i18n';
import { CopyBlock } from './Misc';
import { privateHub } from './ProbeForm';

const L = {
  ru: {
    title: 'Добавить точку', ready: 'Команда для установки',
    intro: 'Человек выполняет одну команду на своей машине с Linux (с Docker или без). Через минуту точка появится во вкладке с флагом и оператором.',
    name: 'Название', name_ph: 'Например: Дом · МТС · Казань', name_hint: 'Можно оставить пустым — возьмём имя машины',
    trusted: 'Доверенный', trusted_hint: 'Получит проверки с секретами: VPN-ссылки, подписки, пароли. Включайте только для тех, кому доверяете. Можно включить позже.',
    create: 'Создать приглашение',
    run: 'Выполнить на машине (нужен root):',
    expires: 'Действует до {0} ({1}). Код одноразовый.',
    secret: 'Не публикуйте команду в открытых чатах и каналах, пока её не выполнили: кто первым её запустит, тот и станет вашей точкой. Передайте лично.',
    plain_http: 'Хаб открыт по http: скрипт отказывается работать без https. Включите HTTPS и задайте UF_PUBLIC_URL с https://.',
    no_command: 'Не удалось собрать команду: у хаба нет публичного адреса. Задайте Public URL в настройках. Код приглашения: {0}',
    once: 'Код показан один раз: хаб хранит только его хеш.', done: 'Готово',
    private_hub: 'Хаб указан как {0}: это внутренний адрес, машины в других сетях до него не достанут. Задайте публичный адрес в настройках → Public URL.',
  },
  en: {
    title: 'Add a point', ready: 'Install command',
    intro: 'The person runs one command on their Linux machine (with or without Docker). A minute later the point shows up on this tab with its flag and operator.',
    name: 'Name', name_ph: 'E.g. Home · Vodafone · Berlin', name_hint: 'May stay empty: the machine’s hostname is used',
    trusted: 'Trusted', trusted_hint: 'Gets checks with secrets: VPN links, subscriptions, passwords. Only for people you trust. Can be turned on later.',
    create: 'Create invite',
    run: 'Run on the machine (as root):',
    expires: 'Valid until {0} ({1}). The code works once.',
    secret: 'Do not post the command in open chats or channels before it is used: whoever runs it first becomes your point. Hand it over privately.',
    plain_http: 'The hub is served over http: the script refuses to run without https. Enable HTTPS and set UF_PUBLIC_URL to https://.',
    no_command: 'Could not build the command: the hub has no public address. Set Public URL in the settings. Invite code: {0}',
    once: 'The code is shown once: the hub keeps only its hash.', done: 'Done',
    private_hub: 'The hub address is {0}: a private address that machines in other networks cannot reach. Set a public address in Settings → Public URL.',
  },
};

/**
 * InviteModal creates a one-time invite for a checker agent and shows the
 * command that installs it. The hub keeps only the code's hash, so once the
 * command is on screen only "Done" closes the modal.
 */
export function InviteModal({ opened, onClose, hubURL }: { opened: boolean; onClose: () => void; hubURL: string }) {
  const t = useT(L);
  const c = useCommon();
  const { lang } = useLang();
  const [name, setName] = useState('');
  const [trusted, setTrusted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const [made, setMade] = useState<InviteCreated | null>(null);

  const close = () => {
    setName('');
    setTrusted(false);
    setErr('');
    setMade(null);
    onClose();
  };

  const create = async () => {
    setErr('');
    setBusy(true);
    try {
      setMade(await api<InviteCreated>('POST', '/api/v1/checkers/invites', { name: name.trim(), trusted }));
      await queryClient.invalidateQueries({ queryKey: ['invites'] });
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal opened={opened} onClose={close} size="lg" title={<Text fw={700} fz="lg">{made ? t('ready') : t('title')}</Text>}
      closeOnClickOutside={!made} closeOnEscape={!made} withCloseButton={!made}>
      {made ? (
        <Stack gap="sm">
          {made.command ? (
            <>
              <Text size="sm">{t('run')}</Text>
              <CopyBlock value={made.command} />
            </>
          ) : (
            <Alert color="red" icon={<IconAlertCircle size={16} />}>{t('no_command', made.code)}</Alert>
          )}
          <Group gap={6} wrap="nowrap">
            <IconClockHour4 size={15} style={{ flex: 'none', opacity: 0.6 }} />
            <Text size="sm" c="dimmed">{t('expires', clock(made.expires, true), ago(made.expires, lang))}</Text>
          </Group>
          <Alert color="yellow" icon={<IconAlertTriangle size={16} />}>{t('secret')} {t('once')}</Alert>
          {made.warning && <Alert color="red" icon={<IconAlertCircle size={16} />}>{t('plain_http')}</Alert>}
          {privateHub(hubURL) && <Alert color="red" icon={<IconAlertCircle size={16} />}>{t('private_hub', hubURL)}</Alert>}
          <Group justify="flex-end"><Button onClick={close}>{t('done')}</Button></Group>
        </Stack>
      ) : (
        <Stack gap="md">
          <Text size="sm" c="dimmed">{t('intro')}</Text>
          {err && <Alert color="red" icon={<IconAlertCircle size={16} />}>{err}</Alert>}
          <TextInput label={t('name')} placeholder={t('name_ph')} description={t('name_hint')} value={name} maxLength={80}
            onChange={(e) => setName(e.currentTarget.value)} onKeyDown={(e) => { if (e.key === 'Enter') create(); }} data-autofocus />
          <Switch label={t('trusted')} description={t('trusted_hint')} color="orange" checked={trusted} onChange={(e) => setTrusted(e.currentTarget.checked)} />
          <Group justify="flex-end">
            <Button variant="default" onClick={close}>{c('cancel')}</Button>
            <Button onClick={create} loading={busy}>{t('create')}</Button>
          </Group>
        </Stack>
      )}
    </Modal>
  );
}
