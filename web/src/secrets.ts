/**
 * The URL secret heuristics of the hub (checks.URLCarriesSecret, used by the
 * viewer redaction too): what is hidden from a viewer keeps a check off an
 * untrusted checker agent. Keep the three rules in sync with the Go side.
 */

/** A query parameter or JSON key that names a credential. */
function secretKey(k: string): boolean {
  k = k.toLowerCase();
  if (['id', 'uuid', 'auth', 'psk', 'key', 'seed', 'sid', 'shortid', 'shortids', 'privatekey', 'presharedkey'].includes(k)) return true;
  return k.includes('pass') || k.includes('secret') || k.includes('token');
}

/** 12+ letters, digits, - or _ with both letters and digits: /p_q7Xk2LmN9vRt4Z, not /telegram-widget. */
function tokenish(s: string): boolean {
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
