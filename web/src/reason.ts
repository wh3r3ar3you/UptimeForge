import { bytes, num } from './format';
import type { Lang } from './i18n';

const R: Record<Lang, Record<string, string>> = {
  ru: {
    timeout: 'таймаут', refused: 'соединение отклонено', reset: 'сброс соединения', tls: 'ошибка TLS', handshake: 'обрыв на рукопожатии',
    stalled: 'обрыв после {0}', dns: 'ошибка DNS', dns_mismatch: 'подмена DNS', unreachable: 'сеть недоступна', keyword: 'нет ключевого слова',
    http_status: 'ответ {0}', egress: 'не тот IP выхода', cert_expiring: 'сертификат скоро истечёт', faketls: 'неверный секрет, не MTProxy или сбиты часы пробы',
    config: 'ошибка конфигурации', offline: 'нет связи', baseline: 'нет интернета', error: 'ошибка',
    unavailable: "недоступно на этой пробе", auth: "неверные учётные данные", query: "ошибка запроса", bad_response: "неожиданный ответ", not_found: "не найдено", redirects: "слишком много редиректов", value_mismatch: "значение не совпало", grpc_status: "ошибка gRPC", sip_status: "ответ SIP не 2xx", ntp_offset: "часы расходятся", unhealthy: "сервис сообщает о проблеме", not_running: "не запущено", no_message: "нет сообщений в топике", host_key: "ключ сервера не совпал", rate_limited: "исчерпан лимит API", remote_failed: "ошибка на удалённой пробе", push_down: "задача сообщила об ошибке", no_heartbeat: "нет сигнала", manual_down: "отключено вручную",
    users_drop: 'онлайн резко упал', node_down: 'нода не на связи с панелью',
    update_missing: 'не вернулся на новую версию', update_rolled_back: 'обновление откатилось', update_unstable: 'постоянно перезапускается на новой версии',
    panic: 'проверка аварийно завершилась', subscription: 'подписка недоступна',
    via: 'сломан upstream-прокси пробы', egress_unknown: 'не удалось определить IP выхода',
    host_cpu: 'загрузка ЦП выше порога', host_mem: 'память выше порога', host_disk: 'диск выше порога', host_swap: 'swap выше порога',
    host_steal: 'steal выше порога', host_iowait: 'iowait выше порога', host_load: 'load average выше порога',
  },
  en: {
    timeout: 'timeout', refused: 'connection refused', reset: 'connection reset', tls: 'TLS error', handshake: 'handshake failed',
    stalled: 'stalled after {0}', dns: 'DNS error', dns_mismatch: 'DNS spoofing', unreachable: 'network unreachable', keyword: 'keyword missing',
    http_status: 'answered {0}', egress: 'wrong exit IP', cert_expiring: 'certificate expires soon', faketls: "wrong secret, not an MTProxy, or the probe's clock is off",
    config: 'config error', offline: 'no connection', baseline: 'no internet', error: 'error',
    unavailable: "not available on this probe", auth: "wrong credentials", query: "query failed", bad_response: "unexpected response", not_found: "not found", redirects: "too many redirects", value_mismatch: "value mismatch", grpc_status: "gRPC error", sip_status: "SIP answer is not 2xx", ntp_offset: "clock offset too large", unhealthy: "service reports a problem", not_running: "not running", no_message: "no message on the topic", host_key: "server key mismatch", rate_limited: "API limit reached", remote_failed: "failed on the remote probe", push_down: "the job reported a failure", no_heartbeat: "no heartbeat", manual_down: "set to down by hand",
    users_drop: 'users online collapsed', node_down: 'node lost by the panel',
    update_missing: 'not back on the new version', update_rolled_back: 'update rolled back', update_unstable: 'keeps restarting on the new version',
    panic: 'the check crashed', subscription: 'subscription unavailable',
    via: "probe's upstream proxy is broken", egress_unknown: 'exit IP could not be determined',
    host_cpu: 'CPU over the threshold', host_mem: 'memory over the threshold', host_disk: 'disk over the threshold', host_swap: 'swap over the threshold',
    host_steal: 'steal over the threshold', host_iowait: 'iowait over the threshold', host_load: 'load average over the threshold',
  },
};

/** reasonText turns a result's reason code (+ error) into a short phrase. */
export function reasonText(reason: string | undefined, error: string | undefined, lang: Lang, stalledBytes?: number): string {
  if (!reason) return error ?? '';
  const tpl = R[lang][reason];
  if (!tpl) return error || reason;
  if (reason === 'stalled') return tpl.replace('{0}', stalledBytes ? bytes(stalledBytes, 1) : error?.match(/after (.+)$/)?.[1] ?? '16 KB');
  if (reason === 'http_status') return tpl.replace('{0}', error?.replace(/^HTTP /, '') ?? '');
  return tpl;
}

const HOST: Record<Lang, Record<string, string>> = {
  ru: {
    cpu: 'Загрузка ЦП', mem: 'Память', disk: 'Диск', swap: 'Swap', steal: 'Steal', iowait: 'I/O wait', load: 'Load average',
    per_core: '{0} на ядро', line: '{0} {1} · порог {2}', line_for: '{0} {1} · порог {2} дольше {3}', peak: 'пик {0}',
  },
  en: {
    cpu: 'CPU', mem: 'Memory', disk: 'Disk', swap: 'Swap', steal: 'Steal', iowait: 'I/O wait', load: 'Load average',
    per_core: '{0} per core', line: '{0} {1} · threshold {2}', line_for: '{0} {1} · threshold {2} for {3}', peak: 'peak {0}',
  },
};

/** hostValue prints a host metric as the Telegram formatter does: percent, or load per core. */
export function hostValue(metric: string, v: number, lang: Lang): string {
  if (metric === 'load') return HOST[lang].per_core.replace('{0}', v.toFixed(2));
  return `${v.toFixed(v < 10 && v !== Math.trunc(v) ? 1 : 0)}%`;
}

/** hostText sums up a host threshold incident: "CPU 95% · threshold 90% for 5m". */
export function hostText(h: { metric: string; value: number; above: number; for_s?: number }, lang: Lang): string {
  const d = HOST[lang];
  const name = d[h.metric] ?? h.metric;
  const win = h.for_s ? (h.for_s >= 3600 ? `${h.for_s / 3600}h` : h.for_s >= 60 ? `${Math.round(h.for_s / 60)}m` : `${h.for_s}s`) : '';
  const tpl = win ? d.line_for : d.line;
  return tpl.replace('{0}', name).replace('{1}', hostValue(h.metric, h.value, lang)).replace('{2}', hostValue(h.metric, h.above, lang)).replace('{3}', win);
}

export function hostPeak(h: { metric: string; peak: number }, lang: Lang): string {
  return HOST[lang].peak.replace('{0}', hostValue(h.metric, h.peak, lang));
}

/** hostShort is the badge form: "CPU 95%". */
export function hostShort(h: { metric: string; value: number }, lang: Lang): string {
  return `${HOST[lang][h.metric] ?? h.metric} ${hostValue(h.metric, h.value, lang)}`;
}

const DROP: Record<Lang, { line: string; low: string; was: string }> = {
  ru: { line: 'Было {0} → сейчас {1}', low: 'минимум {0}', was: 'было {0} онлайн' },
  en: { line: 'Was {0} → now {1}', low: 'low of {0}', was: '{0} were online' },
};

/** downText is what a node the panel lost had: its panel message and the users it held. */
export function downText(inc: { drop?: { before: number }; lines?: { error?: string }[] }, lang: Lang): string {
  const parts = [inc.lines?.[0]?.error ?? ''];
  if (inc.drop?.before) parts.push(DROP[lang].was.replace('{0}', num(inc.drop.before)));
  return parts.filter(Boolean).join(' · ');
}

/** dropText states a users collapse: "Was 1 012 → now 2". */
export function dropText(d: { before: number; now: number }, lang: Lang): string {
  const n = (v: number) => num(v);
  return DROP[lang].line.replace('{0}', n(d.before)).replace('{1}', n(d.now));
}

export function dropLow(d: { low: number }, lang: Lang): string {
  return DROP[lang].low.replace('{0}', num(d.low));
}
