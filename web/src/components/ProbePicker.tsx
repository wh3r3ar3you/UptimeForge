import { Alert, Group, MultiSelect, Text } from '@mantine/core';
import { IconLock } from '@tabler/icons-react';
import { useMemo, type ReactNode } from 'react';
import { isChecker, useProbes, type CatalogEntry, type CheckConfig, type ProbeRow } from '../api';
import { flag } from '../format';
import { urlCarriesSecret } from '../secrets';
import { countWord, useLang, useT } from '../i18n';
import { status } from '../theme';
import { Dot } from './Status';

const L = {
  ru: {
    infra: 'Наша инфраструктура', checkers: 'Агенты проверок', other: 'Другие селекторы',
    all_checkers: 'Все агенты проверок', country: 'страна', kind: 'тип', isp: 'оператор', point: 'точка',
    also: 'и ещё {0} {1}', w_checker: 'агент проверок|агента проверок|агентов проверок',
    untrusted: 'нужен доверенный агент: проверка несёт секреты', offline: 'офлайн', no_net: 'без интернета',
    covers: 'Проверят: инфраструктура {0}, агенты проверок {1}', covers_empty: 'Пусто — вся инфраструктура, которая выполняет проверки. Агенты проверок сюда не входят: их выбирают явно.',
    skipped: 'Недоверенные агенты ({0}) эту проверку не получат: она несёт секреты (VPN-ссылку, пароль). Отметьте агента доверенным во вкладке «Агенты проверок».',
  },
  en: {
    infra: 'Our infrastructure', checkers: 'Checker agents', other: 'Other selectors',
    all_checkers: 'All checker agents', country: 'country', kind: 'kind', isp: 'operator', point: 'point',
    also: 'plus {0} {1}', w_checker: 'checker agent|checker agents',
    untrusted: 'needs a trusted agent: the check carries secrets', offline: 'offline', no_net: 'no internet',
    covers: 'Will run on: infrastructure {0}, checker agents {1}', covers_empty: 'Empty means all infrastructure that runs checks. Checker agents are not included: pick them explicitly.',
    skipped: 'Untrusted agents ({0}) will not get this check: it carries secrets (a VPN link, a password). Mark the agent trusted on the Checker agents tab.',
  },
};

/**
 * carriesSecrets mirrors checks.CarriesSecrets: a check an untrusted checker
 * agent must not see. Xray always has a link, subscription or outbound;
 * MTProto with a link or secret; any catalog field marked secret with a value;
 * an HTTP URL or a catalog URL field with a credential in it.
 */
export function carriesSecrets(d: CheckConfig, catalog: CatalogEntry[]): boolean {
  if (d.type === 'xray') return true;
  if (d.type === 'mtproto' && (d.mtproto?.link || d.mtproto?.secret)) return true;
  const entries = catalog.filter((e) => e.type === d.type);
  const secret = new Set(entries.flatMap((e) => (e.fields ?? []).filter((f) => f.secret).map((f) => f.name)));
  const urls = new Set(entries.flatMap((e) => (e.fields ?? []).filter((f) => f.url).map((f) => f.name)));
  if (d.type === 'http' && urlCarriesSecret(d.http?.url ?? '')) return true;
  const filled = (v: unknown) => v != null && v !== '' && !(typeof v === 'object' && Object.keys(v as object).length === 0);
  const own = (d as unknown as Record<string, Record<string, unknown> | undefined>)[d.type];
  if (own && typeof own === 'object') {
    for (const [k, v] of Object.entries(own)) if (secret.has(k) && filled(v)) return true;
  }
  for (const [k, v] of Object.entries(d.params ?? {})) {
    if (v && (!entries.length || secret.has(k))) return true;
    if (v && urls.has(k) && v.split(/\n/).some(urlCarriesSecret)) return true;
  }
  return false;
}

const match = (p: ProbeRow, sel: string) => {
  if (sel === '*') return true;
  const [k, v] = sel.split('=');
  return p.labels?.[k] === v;
};

/**
 * covered says which probes a check with these selectors runs on, as the hub
 * decides (CheckConfig.runsOn): infrastructure that runs checks when nothing
 * is picked, a checker only through a selector other than "*".
 */
function covered(probes: ProbeRow[], value: string[], exclude: string[], secrets: boolean) {
  const runs = (p: ProbeRow) => p.mode !== 'agent' || !!p.config.probing;
  const infra: ProbeRow[] = [], checkers: ProbeRow[] = [], skipped: ProbeRow[] = [];
  for (const p of probes) {
    if (exclude.some((s) => match(p, s))) continue;
    if (isChecker(p)) {
      if (!value.some((s) => s !== '*' && match(p, s))) continue;
      (secrets && !p.config.trusted ? skipped : checkers).push(p);
    } else if (value.length === 0 ? runs(p) : (runs(p) || value.includes(`id=${p.id}`)) && value.some((s) => match(p, s))) {
      infra.push(p);
    }
  }
  return { infra, checkers, skipped };
}

type Option = { value: string; label: string; disabled?: boolean };
type Hint = { kind?: string; probe?: ProbeRow; untrusted?: boolean };

/**
 * ProbePicker chooses where a check runs, in two groups: our infrastructure
 * and checker agents (volunteer probe points). "selectors" yields hub
 * selectors (country=…, isp=…, role=checker, id=…); "ids" yields probe ids for
 * a one-off run. With secrets, untrusted checker agents cannot be picked.
 */
export function ProbePicker({ value, onChange, mode, secrets, exclude, summary: withSummary, label, description, placeholder }: {
  value: string[]; onChange: (v: string[]) => void; mode: 'selectors' | 'ids'; secrets?: boolean;
  /** summary says under the field where the check will run; exclude is the check's own exclusions, for it. */
  summary?: boolean; exclude?: string[];
  label?: ReactNode; description?: ReactNode; placeholder?: string;
}) {
  const t = useT(L);
  const { lang } = useLang();
  const probesQ = useProbes({ live: false });
  const probes = useMemo(() => probesQ.data?.probes ?? [], [probesQ.data]);

  const { data, hints } = useMemo(() => {
    const hints = new Map<string, Hint>();
    const infra = probes.filter((p) => !isChecker(p));
    const checkers = probes.filter(isChecker);
    const untrusted = (p: ProbeRow) => !!secrets && isChecker(p) && !p.config.trusted;
    const point = (p: ProbeRow): Option => {
      const v = mode === 'ids' ? p.id : `id=${p.id}`;
      hints.set(v, { probe: p, untrusted: untrusted(p) });
      return { value: v, label: `${flag(p.region || p.network?.country) || '📍'} ${p.name}`, disabled: untrusted(p) || (mode === 'ids' && !p.online) };
    };
    const byLabel = (ps: ProbeRow[], k: string) => [...new Set(ps.map((p) => p.labels?.[k]).filter(Boolean) as string[])].sort();
    const sel = (k: string, v: string, text: string): Option => {
      hints.set(`${k}=${v}`, { kind: k });
      return { value: `${k}=${v}`, label: text };
    };
    const groups: { group: string; items: Option[] }[] = [];
    if (mode === 'ids') {
      groups.push({ group: t('infra'), items: infra.map(point) }, { group: t('checkers'), items: checkers.map(point) });
    } else {
      // A country label picks checkers too: say so where the same country has both.
      const runs = infra.filter((p) => p.mode !== 'agent' || p.config.probing);
      const infraCountries = byLabel(runs, 'country');
      const also = (k: string, v: string) => {
        const n = checkers.filter((p) => p.labels?.[k] === v).length;
        return n ? ` · ${t('also', n, countWord(lang, n, t('w_checker')))}` : '';
      };
      groups.push({
        group: t('infra'),
        items: [
          ...infraCountries.map((x) => sel('country', x, `${flag(x)} ${x.toUpperCase()}${also('country', x)}`)),
          ...byLabel(runs, 'kind').map((x) => sel('kind', x, `${x}${also('kind', x)}`)),
          ...infra.map(point),
        ],
      });
      if (checkers.length) {
        hints.set('role=checker', { kind: 'role' });
        groups.push({
          group: t('checkers'),
          items: [
            { value: 'role=checker', label: t('all_checkers') },
            ...byLabel(checkers, 'country').filter((x) => !infraCountries.includes(x)).map((x) => sel('country', x, `${flag(x)} ${x.toUpperCase()}`)),
            ...byLabel(checkers, 'isp').map((x) => sel('isp', x, x)),
            ...checkers.map(point),
          ],
        });
      }
    }
    // Selectors set elsewhere (YAML, an older form) stay visible and removable.
    const known = new Set(groups.flatMap((g) => g.items.map((i) => i.value)));
    const rest = value.filter((v) => !known.has(v));
    if (rest.length) groups.push({ group: t('other'), items: rest.map((v) => ({ value: v, label: v })) });
    return { data: groups.filter((g) => g.items.length), hints };
  }, [probes, mode, secrets, value, t, lang]);

  const cover = mode === 'selectors' && withSummary ? covered(probes, value, exclude ?? [], !!secrets) : null;
  const skipped = mode === 'ids'
    ? probes.filter((p) => value.includes(p.id) && secrets && isChecker(p) && !p.config.trusted)
    : cover?.skipped ?? [];
  const summary = cover && (value.length === 0 ? t('covers_empty') : t('covers', cover.infra.length, cover.checkers.length));

  return (
    <>
      <MultiSelect
        label={label}
        description={[description, summary].filter(Boolean).length ? <>{description}{description && summary ? ' ' : ''}{summary}</> : undefined}
        placeholder={value.length ? undefined : placeholder}
        data={data}
        value={value}
        onChange={onChange}
        searchable
        clearable
        hidePickedOptions={false}
        maxDropdownHeight={360}
        renderOption={({ option }) => {
          const h = hints.get(option.value);
          const p = h?.probe;
          return (
            <Group gap={8} wrap="nowrap">
              {p && mode === 'ids' && <Dot color={!p.online ? status.down : !p.internet ? status.partial : status.ok} />}
              <Text size="sm">{option.label}</Text>
              {h?.kind && h.kind !== 'role' && <Text size="xs" c="dimmed">{t(h.kind as 'country')}</Text>}
              {p && isChecker(p) && p.network?.org && <Text size="xs" c="dimmed" truncate maw={180}>{p.network.org}</Text>}
              {p && mode === 'ids' && !p.online && <Text size="xs" c="dimmed">{t('offline')}</Text>}
              {p && mode === 'ids' && p.online && !p.internet && <Text size="xs" c="dimmed">{t('no_net')}</Text>}
              {h?.untrusted && <Group gap={4} wrap="nowrap"><IconLock size={12} /><Text size="xs" c="dimmed">{t('untrusted')}</Text></Group>}
            </Group>
          );
        }}
      />
      {skipped.length > 0 && (
        <Alert color="yellow" variant="light" icon={<IconLock size={16} />} py="xs">
          <Text size="sm">{t('skipped', skipped.map((p) => p.name).join(', '))}</Text>
        </Alert>
      )}
    </>
  );
}
