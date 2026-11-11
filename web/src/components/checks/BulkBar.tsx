import { Autocomplete, Button, Group, Paper, Popover, Stack, TagsInput, Text } from '@mantine/core';
import { modals } from '@mantine/modals';
import { IconFolder, IconPlayerPause, IconPlayerPlay, IconTag, IconTrash, IconX } from '@tabler/icons-react';
import { useState } from 'react';
import { api, queryClient } from '../../api';
import { useCommon } from '../../i18n';
import { notifyError, notifyOk } from '../Misc';
import type { ChecksT } from './text';

type Action = { action: 'pause' | 'resume' | 'group' | 'tags' | 'delete'; group?: string; add_tags?: string[]; remove_tags?: string[] };

/** BulkBar acts on the selected checks: pause, resume, move, tag, delete. */
export function BulkBar({ ids, groups, tags, t, onDone }: { ids: string[]; groups: string[]; tags: string[]; t: ChecksT; onDone: () => void }) {
  const c = useCommon();
  const [group, setGroup] = useState('');
  const [add, setAdd] = useState<string[]>([]);
  const [remove, setRemove] = useState<string[]>([]);
  const run = async (a: Action) => {
    try {
      const res = await api<{ skipped: string[] }>('POST', '/api/v1/checks/bulk', { ids, ...a });
      await queryClient.invalidateQueries();
      notifyOk(res.skipped.length ? t('skipped', res.skipped.join(', ')) : t('done'));
      onDone();
    } catch (e) {
      notifyError(e);
    }
  };
  const remove_ = () => modals.openConfirmModal({
    title: t('delete'), children: <Text size="sm">{t('confirm_delete', ids.length)}</Text>,
    labels: { confirm: t('delete'), cancel: c('cancel') }, confirmProps: { color: 'red' }, onConfirm: () => run({ action: 'delete' }),
  });
  return (
    <Paper className="uf-bulk-bar" shadow="lg" radius="lg" withBorder p="sm">
      <Group gap="xs" wrap="wrap">
        <Text size="sm" fw={650} mr="xs">{t('selected', ids.length)}</Text>
        <Button size="xs" variant="default" leftSection={<IconPlayerPause size={14} />} onClick={() => run({ action: 'pause' })}>{t('pause')}</Button>
        <Button size="xs" variant="default" leftSection={<IconPlayerPlay size={14} />} onClick={() => run({ action: 'resume' })}>{t('resume')}</Button>
        <Popover position="top" withinPortal shadow="md">
          <Popover.Target><Button size="xs" variant="default" leftSection={<IconFolder size={14} />}>{t('set_group')}</Button></Popover.Target>
          <Popover.Dropdown>
            <Stack gap="xs" w={240}>
              <Autocomplete size="xs" data={groups} value={group} onChange={setGroup} placeholder={t('group_ph')} />
              <Button size="xs" onClick={() => run({ action: 'group', group: group.trim() })}>{t('apply')}</Button>
            </Stack>
          </Popover.Dropdown>
        </Popover>
        <Popover position="top" withinPortal shadow="md">
          <Popover.Target><Button size="xs" variant="default" leftSection={<IconTag size={14} />}>{t('set_tags')}</Button></Popover.Target>
          <Popover.Dropdown>
            <Stack gap="xs" w={260}>
              <TagsInput size="xs" label={t('add_tags')} data={tags} value={add} onChange={setAdd} />
              <TagsInput size="xs" label={t('remove_tags')} data={tags} value={remove} onChange={setRemove} />
              <Button size="xs" disabled={!add.length && !remove.length} onClick={() => run({ action: 'tags', add_tags: add, remove_tags: remove })}>{t('apply')}</Button>
            </Stack>
          </Popover.Dropdown>
        </Popover>
        <Button size="xs" variant="light" color="red" leftSection={<IconTrash size={14} />} onClick={remove_}>{t('delete')}</Button>
        <Button size="xs" variant="subtle" color="gray" leftSection={<IconX size={14} />} onClick={onDone}>{t('clear')}</Button>
      </Group>
    </Paper>
  );
}
