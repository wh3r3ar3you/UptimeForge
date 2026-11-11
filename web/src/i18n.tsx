import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from 'react';
import { setNumberLang } from './format';

export type Lang = 'ru' | 'en';
type Dict = Record<string, string>;
export type Messages<T extends Dict> = { ru: T; en: { [K in keyof T]: string } };

const LangContext = createContext<{ lang: Lang; setLang: (l: Lang) => void }>({ lang: 'en', setLang: () => {} });

function initialLang(): Lang {
  try {
    const saved = localStorage.getItem('uf-lang');
    if (saved === 'ru' || saved === 'en') return saved;
  } catch {
    /* storage unavailable */
  }
  return navigator.language?.toLowerCase().startsWith('ru') ? 'ru' : 'en';
}

/** applyLang points everything outside React at the UI language: the page's lang (screen readers, hyphenation), number formatting. */
function applyLang(l: Lang) {
  document.documentElement.lang = l;
  setNumberLang(l);
}

export function LangProvider({ children }: { children: ReactNode }) {
  const [lang, set] = useState<Lang>(() => {
    const l = initialLang();
    applyLang(l); // before the first render, so numbers come out right at once
    return l;
  });
  const setLang = useCallback((l: Lang) => {
    applyLang(l);
    set(l);
    try {
      localStorage.setItem('uf-lang', l);
    } catch {
      /* ignore */
    }
  }, []);
  const value = useMemo(() => ({ lang, setLang }), [lang, setLang]);
  return <LangContext.Provider value={value}>{children}</LangContext.Provider>;
}

export const useLang = () => useContext(LangContext);

function fill(s: string, args: (string | number)[]) {
  return args.length ? s.replace(/\{(\d+)\}/g, (_, i) => String(args[Number(i)] ?? '')) : s;
}

/** useT binds a page's dictionary: t('key', arg0, arg1) with {0} {1} placeholders. */
export function useT<T extends Dict>(m: Messages<T>) {
  const { lang } = useLang();
  return useCallback(
    (key: keyof T, ...args: (string | number)[]) => fill((m[lang] as Dict)[key as string] ?? (m.en as Dict)[key as string] ?? String(key), args),
    [lang, m],
  );
}

/** Russian plural: plural(n, 'проба', 'пробы', 'проб'). */
export function plural(n: number, one: string, few: string, many: string) {
  const m10 = n % 10, m100 = n % 100;
  if (m10 === 1 && m100 !== 11) return one;
  if (m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14)) return few;
  return many;
}

/**
 * countWord picks a noun form for n from a dictionary entry: "проверка|проверки|проверок"
 * in Russian (one, few, many), "check|checks" in English.
 */
export function countWord(lang: Lang, n: number, forms: string): string {
  const f = forms.split('|');
  if (lang === 'ru') return plural(n, f[0], f[1] ?? f[0], f[2] ?? f[1] ?? f[0]);
  return n === 1 ? f[0] : f[1] ?? f[0];
}

export const common = {
  ru: {
    ok: 'Работает', partial: 'Частично', blocked: 'Блок', down: 'Не работает', unknown: 'Нет данных', paused: 'На паузе',
    probe_offline: 'Проба офлайн', probe_no_net: 'Нет интернета', host: 'Порог метрики', users_drop: 'Ушли пользователи', node_down: 'Нода не на связи с панелью',
    critical: 'Критично', warning: 'Предупреждение',
    online: 'Онлайн', offline: 'Офлайн',
    save: 'Сохранить', cancel: 'Отмена', delete: 'Удалить', edit: 'Изменить', add: 'Добавить', close: 'Закрыть',
    copy: 'Копировать', copied: 'Скопировано', refresh: 'Обновить', loading: 'Загрузка…', error: 'Ошибка',
    saved: 'Сохранено', deleted: 'Удалено', confirm_delete: 'Удалить «{0}»? Это действие не отменить.',
    managed: 'Из config.yaml', managed_hint: 'Задано в config.yaml — меняется только там',
    never: 'никогда', now: 'сейчас', ago: '{0} назад', all: 'Все', none: 'Нет', search: 'Поиск…',
    ack: 'Принять', mute: 'Заглушить', acked_by: 'Принял {0}', muted_until: 'Заглушено до {0}',
    probe: 'Проба', probes: 'Пробы', check: 'Проверка', checks: 'Проверки', status: 'Статус',
    latency: 'Задержка', uptime: 'Аптайм', since: 'с {0}', reason: 'Причина',
  },
  en: {
    ok: 'Up', partial: 'Partial', blocked: 'Blocked', down: 'Down', unknown: 'No data', paused: 'Paused',
    probe_offline: 'Probe offline', probe_no_net: 'No internet', host: 'Metric threshold', users_drop: 'Users gone', node_down: 'Node lost by the panel',
    critical: 'Critical', warning: 'Warning',
    online: 'Online', offline: 'Offline',
    save: 'Save', cancel: 'Cancel', delete: 'Delete', edit: 'Edit', add: 'Add', close: 'Close',
    copy: 'Copy', copied: 'Copied', refresh: 'Refresh', loading: 'Loading…', error: 'Error',
    saved: 'Saved', deleted: 'Deleted', confirm_delete: 'Delete “{0}”? This cannot be undone.',
    managed: 'From config.yaml', managed_hint: 'Defined in config.yaml — edit it there',
    never: 'never', now: 'now', ago: '{0} ago', all: 'All', none: 'None', search: 'Search…',
    ack: 'Acknowledge', mute: 'Mute', acked_by: 'Acknowledged by {0}', muted_until: 'Muted until {0}',
    probe: 'Probe', probes: 'Probes', check: 'Check', checks: 'Checks', status: 'Status',
    latency: 'Latency', uptime: 'Uptime', since: 'since {0}', reason: 'Reason',
  },
};

export const useCommon = () => useT(common);
