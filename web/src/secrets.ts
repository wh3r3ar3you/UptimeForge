/**
 * The URL secret heuristics of the hub (checks.URLCarriesSecret, used by the
 * viewer redaction too): what is hidden from a viewer keeps a check off an
 * untrusted checker agent. Keep the three rules in sync with the Go side.
 */

/** decoded is s percent-decoded, or null when it cannot be (assumed to hide a secret). */
function decoded(s: string): string | null {
  if (!s.includes('%')) return s;
  try {
    return decodeURIComponent(s);
  } catch {
    return null;
  }
}

/** A query parameter or JSON key that names a credential. */
function secretKey(raw: string): boolean {
  const d = decoded(raw.replace(/\+/g, ' '));
  if (d == null) return true;
  const k = d.toLowerCase();
  if (['id', 'uuid', 'auth', 'psk', 'key', 'seed', 'sid', 'shortid', 'shortids', 'privatekey', 'presharedkey'].includes(k)) return true;
  return k.includes('pass') || k.includes('secret') || k.includes('token');
}

/** Tokens with a shape of their own: a JSON web token and a Telegram bot token. */
const TOKEN_RES = [/^eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*$/, /^(bot)?[0-9]{6,}:[A-Za-z0-9_-]{30,}$/];

/**
 * 12+ letters, digits, - or _ with both letters and digits (base64 "=" padding
 * aside), a JSON web token or a Telegram bot token: /p_q7Xk2LmN9vRt4Z, not
 * /telegram-widget or jquery-3.7.1.min.js. Decoded first; undecodable counts.
 */
function tokenish(raw: string): boolean {
  const d = decoded(raw);
  if (d == null) return true;
  if (TOKEN_RES.some((re) => re.test(d))) return true;
  const s = d.replace(/=+$/, '');
  return s.length >= 12 && /^[A-Za-z0-9_-]+$/.test(s) && /[A-Za-z]/.test(s) && /[0-9]/.test(s);
}

/** A user part, a token-like path segment, or a secret query key or token-like value. */
export function urlCarriesSecret(raw: string): boolean {
  const s = raw.trim();
  const i = s.indexOf('://');
  if (i < 0) return false;
  let rest = s.slice(i + 3);
  const hash = rest.indexOf('#');
  if (hash >= 0) rest = rest.slice(0, hash);
  let query = '';
  const q = rest.indexOf('?');
  if (q >= 0) [rest, query] = [rest.slice(0, q), rest.slice(q + 1)];
  const slash = rest.indexOf('/');
  const authority = slash >= 0 ? rest.slice(0, slash) : rest;
  const path = slash >= 0 ? rest.slice(slash + 1) : '';
  if (authority.includes('@')) return true;
  if (path.split('/').some(tokenish)) return true;
  return query.split('&').some((kv) => {
    const eq = kv.indexOf('=');
    return eq >= 0 && (secretKey(kv.slice(0, eq)) || tokenish(kv.slice(eq + 1)));
  });
}
