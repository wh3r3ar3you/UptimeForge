import { Text } from '@mantine/core';
import { modals } from '@mantine/modals';
import { useCallback, useEffect, useRef } from 'react';
import { useBlocker } from 'react-router';
import { useT } from '../i18n';

const L = {
  ru: {
    title: 'Выйти без сохранения?', body: 'Введённое не сохранено и пропадёт.', discard: 'Не сохранять', keep: 'Продолжить правку',
    leave_title: 'Уйти со страницы?', leave_body: 'На этой странице есть несохранённые изменения. Если уйти, они пропадут.', leave: 'Уйти',
  },
  en: {
    title: 'Discard your changes?', body: 'What you entered is not saved and will be lost.', discard: 'Discard', keep: 'Keep editing',
    leave_title: 'Leave this page?', leave_body: 'This page has unsaved changes. They are lost if you leave.', leave: 'Leave',
  },
};

/**
 * useGuardedClose keeps a form modal from losing input: while it is dirty an
 * outside click or Esc does nothing, and closing (the X, Cancel) asks first.
 * Spread modalProps on the Modal and call requestClose from Cancel.
 */
export function useGuardedClose(dirty: boolean, onClose: () => void) {
  const t = useT(L);
  const requestClose = useCallback(() => {
    if (!dirty) return onClose();
    modals.openConfirmModal({
      title: <Text fw={650}>{t('title')}</Text>,
      children: <Text size="sm">{t('body')}</Text>,
      labels: { confirm: t('discard'), cancel: t('keep') },
      confirmProps: { color: 'red' },
      onConfirm: onClose,
    });
  }, [dirty, onClose, t]);
  return { requestClose, modalProps: { onClose: requestClose, closeOnClickOutside: !dirty, closeOnEscape: !dirty } };
}

/** confirmThen asks before an action whose effect is easy to miss (a token deleted on save, nodes pruned). */
export function confirmThen({ title, body, confirm, cancel, danger = true, onConfirm }: {
  title: string; body: string; confirm: string; cancel: string; danger?: boolean; onConfirm: () => void;
}) {
  modals.openConfirmModal({
    title: <Text fw={650}>{title}</Text>,
    children: <Text size="sm">{body}</Text>,
    labels: { confirm, cancel },
    confirmProps: danger ? { color: 'red' } : undefined,
    onConfirm,
  });
}

/**
 * useLeaveGuard holds the user on a page with unsaved changes: navigating
 * inside the app asks first, closing or reloading the tab gets the
 * browser's own prompt.
 */
export function useLeaveGuard(dirty: boolean) {
  const t = useT(L);
  const blocker = useBlocker(({ currentLocation, nextLocation }) => dirty && currentLocation.pathname !== nextLocation.pathname);
  const asking = useRef(false);

  useEffect(() => {
    if (blocker.state !== 'blocked' || asking.current) return;
    asking.current = true;
    let decided = false;
    const decide = (leave: boolean) => {
      if (decided) return;
      decided = true;
      if (leave) blocker.proceed?.();
      else blocker.reset?.();
    };
    modals.openConfirmModal({
      title: <Text fw={650}>{t('leave_title')}</Text>,
      children: <Text size="sm">{t('leave_body')}</Text>,
      labels: { confirm: t('leave'), cancel: t('keep') },
      confirmProps: { color: 'red' },
      onConfirm: () => decide(true),
      onCancel: () => decide(false),
      onClose: () => {
        asking.current = false;
        decide(false); // Esc or a click outside means stay
      },
    });
  }, [blocker, t]);

  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = ''; // older browsers need it set to show the prompt
    };
    window.addEventListener('beforeunload', warn);
    return () => window.removeEventListener('beforeunload', warn);
  }, [dirty]);
}
