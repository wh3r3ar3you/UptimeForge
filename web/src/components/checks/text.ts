import { useCallback } from 'react';
import { countWord, useLang, useT } from '../../i18n';

/** The checks page dictionary, shared by its components. */
export const L = {
  ru: {
    title: 'Проверки', subtitle: '{0} · {1}', add: 'Добавить проверку', search: 'Имя, адрес, метка…',
    w_check: 'проверка|проверки|проверок', w_probe: 'проба|пробы|проб', w_config: 'конфиг|конфига|конфигов',
    menu: 'Действия: {0}', expand: 'Показать конфиги', collapse: 'Скрыть конфиги', yaml: 'yaml', select_group: 'Выбрать все в группе «{0}»',
    group_by: 'Группировать', by_group: 'Группа', by_type: 'Тип', by_status: 'Статус', by_country: 'Страна', by_tag: 'Метка',
    all: 'Все', problems: 'Проблемы', ok: 'Работают', paused: 'Пауза', nodata: 'Нет данных',
    list: 'Список', tiles: 'Плитки', no_group: 'Без группы', no_tags: 'Без меток', no_country: 'Без страны', multi_country: 'Несколько стран',
    all_ok: 'все работают', n_problems: '{0} с проблемами', uptime: 'аптайм 24ч', latency: 'задержка',
    nothing: 'Ничего не найдено', empty: 'Проверок пока нет', empty_hint: 'Добавьте VPN-конфиг, подписку, Telegram-прокси или любой сервис.',
    bar_tip: '{0}: {1} из {2} успешно', bar_none: '{0}: нет данных', bar_incident: 'инцидент: {0}',
    open: 'Открыть', edit: 'Изменить', duplicate: 'Дублировать', pause: 'Поставить на паузу', resume: 'Снять с паузы', delete: 'Удалить',
    copy: '{0} (копия)', duplicated: 'Создана копия', confirm_delete: 'Удалить проверки ({0})? История и инциденты останутся.',
    selected: 'Выбрано: {0}', set_group: 'В группу', set_tags: 'Метки', add_tags: 'Добавить метки', remove_tags: 'Убрать метки', apply: 'Применить',
    clear: 'Снять выбор', done: 'Готово', skipped: 'Пропущены (заданы в config.yaml): {0}', group_ph: 'Название группы',
  },
  en: {
    title: 'Checks', subtitle: '{0} · {1}', add: 'Add check', search: 'Name, address, tag…',
    w_check: 'check|checks', w_probe: 'probe|probes', w_config: 'config|configs',
    menu: 'Actions: {0}', expand: 'Show configs', collapse: 'Hide configs', yaml: 'yaml', select_group: 'Select all in “{0}”',
    group_by: 'Group by', by_group: 'Group', by_type: 'Type', by_status: 'Status', by_country: 'Country', by_tag: 'Tag',
    all: 'All', problems: 'Problems', ok: 'Working', paused: 'Paused', nodata: 'No data',
    list: 'List', tiles: 'Tiles', no_group: 'No group', no_tags: 'No tags', no_country: 'No country', multi_country: 'Several countries',
    all_ok: 'all working', n_problems: '{0} with problems', uptime: 'uptime 24h', latency: 'latency',
    nothing: 'Nothing found', empty: 'No checks yet', empty_hint: 'Add a VPN config, a subscription, a Telegram proxy or any service.',
    bar_tip: '{0}: {1} of {2} succeeded', bar_none: '{0}: no data', bar_incident: 'incident: {0}',
    open: 'Open', edit: 'Edit', duplicate: 'Duplicate', pause: 'Pause', resume: 'Resume', delete: 'Delete',
    copy: '{0} (copy)', duplicated: 'Copy created', confirm_delete: 'Delete checks ({0})? History and incidents stay.',
    selected: 'Selected: {0}', set_group: 'Move to group', set_tags: 'Tags', add_tags: 'Add tags', remove_tags: 'Remove tags', apply: 'Apply',
    clear: 'Clear selection', done: 'Done', skipped: 'Skipped (set in config.yaml): {0}', group_ph: 'Group name',
  },
};

export const useChecksT = () => useT(L);
export type ChecksT = ReturnType<typeof useChecksT>;

/** useCount says "5 проверок" / "1 check": a number with its noun in the right form. */
export function useCount() {
  const t = useChecksT();
  const { lang } = useLang();
  return useCallback((n: number, word: 'w_check' | 'w_probe' | 'w_config') => `${n} ${countWord(lang, n, t(word))}`, [lang, t]);
}
export type Count = ReturnType<typeof useCount>;
