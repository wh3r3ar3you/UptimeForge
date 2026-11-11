import { Box, Group, SimpleGrid, Text, Tooltip } from '@mantine/core';
import { IconCheck, IconX } from '@tabler/icons-react';
import type { Incident, Kind, ProbeLine } from '../api';
import { bytes, flag, ms } from '../format';
import { useLang, useT } from '../i18n';
import { reasonText } from '../reason';
import { status } from '../theme';

// Verdict explanations as the hub's Telegram formatter words them (internal/notify/format.go).
const L = {
  ru: {
    why_down: 'Не работает ни с одной пробы — проблема на стороне сервера или сервиса.',
    why_down_single: 'Проверяется только с одной пробы — подтвердить с других нечем.',
    why_blocked: 'Из {0} не работает, из других регионов — работает. Сервер жив, похоже на блокировку.',
    why_partial: 'Не работает только у части проб — вероятна блокировка или проблема у конкретного провайдера.',
    why_stalled: 'Обрыв после ~{0} — так выглядит фильтрация ТСПУ по объёму.',
    why_handshake: 'Соединение рвётся на рукопожатии — похоже на блокировку по SNI или протоколу.',
    why_timeout: 'Соединение не устанавливается — похоже на блокировку по IP.',
    why_offline: 'Агент не на связи с хабом. Его проверки исключены из консенсуса.',
    why_never: 'Агент ещё ни разу не подключался к хабу.',
    why_no_net: 'Базовые проверки не проходят — у пробы пропал интернет. Она исключена из консенсуса, чтобы не было ложных тревог.',
    why_subscription: 'Хаб не может скачать подписку: клиенты не обновят конфиги, а список проверяемых конфигов заморожен.',
    why_users_drop: 'Нода на связи с панелью, а клиенты до неё не доходят: похоже на блокировку ТСПУ.',
    why_users_gone: 'Нода потеряла связь с панелью, клиенты отвалились вместе с ней.',
    why_users_mass: 'Онлайн упал сразу на большинстве нод: похоже на сбой панели или сети, а не на блок одной ноды.',
    why_node_down: 'Панель не достаёт до ноды: сервер лежит, перезагружается или потерял сеть.',
    why_node_mass: 'Панель потеряла большинство нод сразу: похоже на сбой самой панели или её сети.',
    why_host_cpu: 'Процессор занят почти полностью: проверьте, что его грузит.',
    why_host_mem: 'Когда память кончится, ядро начнёт убивать процессы.',
    why_host_disk: 'На полном диске перестают писаться логи и базы.',
    why_host_swap: 'Сервер активно использует swap, всё замедляется.',
    why_host_steal: 'Гипервизор отдаёт процессор соседям: сервер тормозит не из-за своей нагрузки.',
    why_host_iowait: 'Процессор ждёт диск: узкое место в хранилище.',
    why_host_load: 'Задач в очереди больше, чем ядер.',
  },
  en: {
    why_down: 'Fails from every probe — the server or service itself is broken.',
    why_down_single: 'Only one probe checks this — nothing to confirm it with.',
    why_blocked: 'Fails from {0}, works from other regions. The server is alive; looks like blocking.',
    why_partial: 'Fails from some probes only — likely blocking or trouble at a specific ISP.',
    why_stalled: 'Transfer freezes after ~{0} — the signature of volume-based DPI.',
    why_handshake: 'Connections die during the handshake — looks like SNI/protocol blocking.',
    why_timeout: 'Connections never establish — looks like IP blocking.',
    why_offline: 'The agent lost its connection to the hub. Its checks are excluded from consensus.',
    why_never: 'The agent has never connected to the hub.',
    why_no_net: 'Baseline checks fail — the probe lost internet. It is excluded from consensus to avoid false alarms.',
    why_subscription: "The hub can't download the subscription: clients can't update configs and the checked config list is frozen.",
    why_users_drop: 'The node is connected to the panel, but clients no longer reach it: looks like a DPI ban.',
    why_users_gone: 'The node lost the panel, and its clients went with it.',
    why_users_mass: 'Users dropped on most nodes at once: looks like a panel or network failure, not one banned node.',
    why_node_down: 'The panel cannot reach the node: the server is down, rebooting or off the network.',
    why_node_mass: 'The panel lost most nodes at once: looks like a failure of the panel itself or its network.',
    why_host_cpu: 'The CPU is nearly saturated: see what loads it.',
    why_host_mem: 'When memory runs out, the kernel starts killing processes.',
    why_host_disk: 'On a full disk logs and databases stop writing.',
    why_host_swap: 'The server leans on swap and everything slows down.',
    why_host_steal: "The hypervisor gives the CPU to neighbours: the slowdown is not this server's load.",
    why_host_iowait: 'The CPU waits on the disk: storage is the bottleneck.',
    why_host_load: 'More tasks are queued than there are cores.',
  },
};

/** useReason words a failure the way the Telegram alert does (see reason.ts). */
export function useReason() {
  const { lang } = useLang();
  return (reason?: string, error?: string, size?: number) => reasonText(reason, error, lang, size);
}

interface Failure { ok: boolean; reason?: string; bytes?: number }

/** useWhy explains a verdict in one sentence, like the 🧠 line of the alert. */
export function useWhy() {
  const t = useT(L);
  return (kind: Kind | string, lines: Failure[], opts: { region?: string; type?: string; note?: string; connected?: boolean } = {}) => {
    if (kind === 'node_down') return opts.note === 'mass' ? t('why_node_mass') : t('why_node_down');
    if (kind === 'users_drop' || opts.type === 'users') {
      return opts.note === 'mass' ? t('why_users_mass') : opts.connected === false ? t('why_users_gone') : t('why_users_drop');
    }
    if (opts.type === 'subscription') return t('why_subscription');
    if (kind === 'probe_offline') return opts.note === 'never' ? t('why_never') : t('why_offline');
    if (kind === 'probe_no_net') return t('why_no_net');
    if (kind === 'host' || opts.type === 'host') {
      const r = lines.find((l) => l.reason?.startsWith('host_'))?.reason;
      return r ? t(`why_${r}` as 'why_host_cpu') : '';
    }
    const failing = lines.filter((l) => !l.ok);
    let s = '';
    if (kind === 'down') s = lines.length === 1 ? t('why_down_single') : t('why_down');
    else if (kind === 'blocked') s = t('why_blocked', (opts.region ?? '').toUpperCase());
    else if (kind === 'partial') s = t('why_partial');
    if ((kind === 'blocked' || kind === 'partial') && failing.length) {
      const all = (...rs: string[]) => failing.every((l) => rs.includes(l.reason ?? ''));
      if (all('stalled')) s += ' ' + t('why_stalled', bytes(Math.max(...failing.map((l) => l.bytes ?? 0)), 1));
      else if (all('handshake', 'reset', 'tls')) s += ' ' + t('why_handshake');
      else if (all('timeout')) s += ' ' + t('why_timeout');
    }
    return s;
  };
}

/**
 * classify mirrors the hub's consensus (internal/hub/verdict.go): nobody fails → ok,
 * everybody fails → down, every probe of one region fails and only them → blocked,
 * anything else → partial.
 */
export function classify(lines: { ok: boolean; region?: string }[]): { kind: Kind; region?: string } {
  if (!lines.length) return { kind: 'unknown' };
  const failing = lines.filter((l) => !l.ok);
  if (!failing.length) return { kind: 'ok' };
  if (failing.length === lines.length) return { kind: 'down' };
  let region = '';
  for (const l of failing) {
    if (!l.region || (region && l.region !== region)) return { kind: 'partial' };
    region = l.region;
  }
  if (lines.some((l) => l.region === region && l.ok)) return { kind: 'partial' };
  return { kind: 'blocked', region };
}

export function OkMark({ ok, size = 18 }: { ok: boolean; size?: number }) {
  const c = ok ? status.ok : status.down;
  const I = ok ? IconCheck : IconX;
  return (
    <Box
      component="span"
      style={{
        width: size, height: size, borderRadius: 6, flex: 'none', display: 'inline-grid', placeItems: 'center',
        background: `color-mix(in srgb, ${c} 16%, transparent)`, color: c,
      }}
    >
      <I size={size - 6} stroke={3} />
    </Box>
  );
}

/** ProbeLines lists every probe's view of an incident: failures first, like the Telegram alert. */
export function ProbeLines({ inc }: { inc: Pick<Incident, 'lines'> }) {
  const reason = useReason();
  const lines = [...(inc.lines ?? [])].sort((a, b) => Number(a.ok) - Number(b.ok));
  if (!lines.length) return null;
  return (
    <SimpleGrid cols={{ base: 1, sm: 2 }} spacing={6} verticalSpacing={6}>
      {lines.map((l: ProbeLine) => (
        <Group key={l.probe} gap={8} wrap="nowrap" align="flex-start" style={{ minWidth: 0 }}>
          <Box pt={1}><OkMark ok={l.ok} /></Box>
          <Box style={{ minWidth: 0, flex: 1 }}>
            <Group gap={6} wrap="nowrap" style={{ minWidth: 0 }}>
              <Text size="sm" fw={l.ok ? 500 : 650} truncate>
                <Box component="span" mr={6}>{flag(l.region) || '📍'}</Box>{l.name}
              </Text>
              {l.ok && l.latency_ms != null && (
                <Text size="xs" c="dimmed" className="uf-num" style={{ flex: 'none' }}>{ms(l.latency_ms)}</Text>
              )}
            </Group>
            {!l.ok && (
              <Tooltip label={l.error || l.reason} multiline maw={420} disabled={!l.error} events={{ hover: true, focus: true, touch: true }}>
                <Text size="xs" c="dimmed" lineClamp={1} style={{ wordBreak: 'break-all' }}>
                  <Text span size="xs" fw={600} c="var(--mantine-color-text)">{reason(l.reason, l.error, l.bytes)}</Text>
                  {l.error && l.reason && reason(l.reason, l.error, l.bytes) !== l.error ? ` · ${l.error}` : ''}
                </Text>
              </Tooltip>
            )}
          </Box>
        </Group>
      ))}
    </SimpleGrid>
  );
}
