import dayjs from 'dayjs';
import relativeTime from 'dayjs/plugin/relativeTime';
import 'dayjs/locale/ru';
import type { Lang } from './i18n';

dayjs.extend(relativeTime);

const IEC = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];

export function bytes(n?: number | null, digits = 2): string {
  if (n == null || !isFinite(n)) return '—';
  let i = 0;
  let v = Math.abs(n);
  while (v >= 1024 && i < IEC.length - 1) {
    v /= 1024;
    i++;
  }
  return `${n < 0 ? '-' : ''}${v.toFixed(i === 0 ? 0 : digits)} ${IEC[i]}`;
}

/** Network rates are decimal bits per second, like link speeds. */
export function bitrate(bytesPerSec?: number | null): string {
  if (bytesPerSec == null || !isFinite(bytesPerSec)) return '—';
  const bits = bytesPerSec * 8;
  if (bits >= 1e9) return `${(bits / 1e9).toFixed(2)} Gbps`;
  if (bits >= 1e6) return `${(bits / 1e6).toFixed(1)} Mbps`;
  if (bits >= 1e3) return `${(bits / 1e3).toFixed(0)} Kbps`;
  return `${bits.toFixed(0)} bps`;
}

export function ms(v?: number | null): string {
  if (v == null || !isFinite(v)) return '—';
  if (v >= 10_000) return `${(v / 1000).toFixed(1)} s`;
  if (v >= 100) return `${v.toFixed(0)} ms`;
  return `${v.toFixed(v >= 10 ? 0 : 1)} ms`;
}

export function pct(v?: number | null, digits = 2): string {
  if (v == null || !isFinite(v)) return '—';
  const p = v * 100;
  if (p >= 99.995 && p < 100) return '99.99%';
  return `${p.toFixed(p === 100 ? 0 : digits)}%`;
}

// Numbers follow the UI language (1 234 in Russian, 1,234 in English); the
// language provider sets it, so callers need not pass the language around.
let numberFormat = new Intl.NumberFormat('en-US');

export function setNumberLang(lang: Lang) {
  numberFormat = new Intl.NumberFormat(lang === 'ru' ? 'ru-RU' : 'en-US');
}

/** num formats a count in the UI language. */
export function num(n?: number | null): string {
  if (n == null || !isFinite(n)) return '—';
  return numberFormat.format(n);
}

export function compact(n: number): string {
  if (Math.abs(n) >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (Math.abs(n) >= 1e3) return `${(n / 1e3).toFixed(n >= 1e4 ? 0 : 1)}K`;
  return String(Math.round(n));
}

export function duration(seconds: number, lang: Lang): string {
  const ru = lang === 'ru';
  const d = Math.floor(seconds / 86400), h = Math.floor((seconds % 86400) / 3600), m = Math.floor((seconds % 3600) / 60);
  const u = ru ? { d: 'д', h: 'ч', m: 'мин' } : { d: 'd', h: 'h', m: 'm' };
  if (d > 0) return `${d} ${u.d}${h ? ` ${h} ${u.h}` : ''}`;
  if (h > 0) return `${h} ${u.h}${m ? ` ${m} ${u.m}` : ''}`;
  if (m > 0) return `${m} ${u.m}`;
  return ru ? '<1 мин' : '<1m';
}

export function days(seconds: number, lang: Lang): string {
  const d = Math.floor(seconds / 86400);
  if (d < 1) return duration(seconds, lang);
  if (lang === 'ru') {
    const m10 = d % 10, m100 = d % 100;
    const w = m10 === 1 && m100 !== 11 ? 'день' : m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14) ? 'дня' : 'дней';
    return `${d} ${w}`;
  }
  return `${d} ${d === 1 ? 'day' : 'days'}`;
}

export function ago(iso: string | number | undefined, lang: Lang): string {
  if (!iso) return '—';
  return dayjs(iso).locale(lang).fromNow();
}

export function clock(iso: string | number, withDate = false): string {
  const d = dayjs(iso);
  return withDate || dayjs().diff(d, 'hour') > 20 ? d.format('DD.MM HH:mm') : d.format('HH:mm');
}

export function since(iso: string, lang: Lang): string {
  return duration(Math.max(0, dayjs().diff(dayjs(iso), 'second')), lang);
}

/** Flag turns an ISO country code into its emoji flag. */
export function flag(code?: string): string {
  if (!code || code.length !== 2) return '';
  const A = 0x1f1e6;
  const c = code.toUpperCase();
  if (!/^[A-Z]{2}$/.test(c)) return '';
  return String.fromCodePoint(A + c.charCodeAt(0) - 65, A + c.charCodeAt(1) - 65);
}

/** Parse "60s", "5m", "1h30m" into seconds. */
export function parseGoDuration(s?: string): number {
  if (!s) return 0;
  let total = 0;
  for (const m of s.matchAll(/([\d.]+)(h|ms|m|s)/g)) {
    const v = parseFloat(m[1]);
    total += m[2] === 'h' ? v * 3600 : m[2] === 'm' ? v * 60 : m[2] === 's' ? v : v / 1000;
  }
  return total;
}

export function goDuration(s?: string): string {
  if (!s) return '';
  return s.replace(/(\d)m0s$/, '$1m').replace(/(\d)h0m$/, '$1h');
}

const COUNTRIES = ('ad ae af ag ai al am ao ar as at au aw az ba bb bd be bf bg bh bi bj bm bn bo br bs bt bw by bz ca cd cf cg ch ci cl cm cn co cr cu cv cy cz ' +
  'de dj dk dm do dz ec ee eg er es et fi fj fm fo fr ga gb gd ge gh gi gl gm gn gq gr gt gu gw gy hk hn hr ht hu id ie il im in iq ir is it je jm jo jp ' +
  'ke kg kh ki km kn kp kr kw ky kz la lb lc li lk lr ls lt lu lv ly ma mc md me mg mh mk ml mm mn mo mr ms mt mu mv mw mx my mz ' +
  'na ne ng ni nl no np nr nz om pa pe pg ph pk pl pr ps pt pw py qa ro rs ru rw sa sb sc sd se sg si sk sl sm sn so sr ss st sv sy sz ' +
  'td tg th tj tl tm tn to tr tt tv tw tz ua ug us uy uz va vc ve vg vn vu ws xk ye za zm zw').split(' ');

/** Every country as a select option: flag and localized name, sorted by name. */
export function countryOptions(lang: string): { value: string; label: string }[] {
  const names = new Intl.DisplayNames([lang], { type: 'region' });
  return COUNTRIES.map((c) => ({ c, name: names.of(c.toUpperCase()) ?? c.toUpperCase() }))
    .sort((a, b) => a.name.localeCompare(b.name, lang))
    .map(({ c, name }) => ({ value: c, label: `${flag(c)} ${name}` }));
}

/** Finds a country by its code or its name in either UI language, whichever the UI shows. */
export function countryMatches(code: string, search: string): boolean {
  const s = search.trim().toLowerCase();
  if (!s) return true;
  if (code === s) return true;
  return ['ru', 'en'].some((l) => (new Intl.DisplayNames([l], { type: 'region' }).of(code.toUpperCase()) ?? '').toLowerCase().includes(s));
}
