import type { CheckRow, ProbeStatus } from '../../api';

export type GroupBy = 'group' | 'type' | 'status' | 'country' | 'tag';
export type StatusFilter = 'all' | 'problems' | 'ok' | 'paused';

export interface CheckGroup {
  key: string;
  title: string;
  type: string; // the most common check type in it, for the icon
  rows: CheckRow[];
  problems: number;
  uptime?: number;
}

export const isProblem = (r: CheckRow) => ['partial', 'blocked', 'down'].includes(r.verdict);
export const isOK = (r: CheckRow) => !r.paused && r.verdict === 'ok';

export interface GroupLabels {
  noGroup: string; noTags: string; noCountry: string; multiCountry: string;
  status: Record<'problems' | 'ok' | 'paused' | 'nodata', string>;
  type: (t: string) => string;
  country: (code: string) => string;
}

function statusKey(r: CheckRow): 'problems' | 'ok' | 'paused' | 'nodata' {
  if (r.paused) return 'paused';
  if (isProblem(r)) return 'problems';
  return r.verdict === 'ok' ? 'ok' : 'nodata';
}

/** countryOf is where a check looks from: the region all its probes share, if they do. */
function countryOf(r: CheckRow, probes: Map<string, ProbeStatus>): string {
  const regions = new Set(r.probes.map((c) => probes.get(c.probe)?.region ?? '').filter(Boolean));
  return regions.size === 1 ? [...regions][0] : regions.size > 1 ? '*' : '';
}

/**
 * groupChecks splits top-level checks (subscription configs travel with
 * their subscription) into sections: problems first, then by title. With
 * grouping by tag a check appears under each of its tags.
 */
export function groupChecks(rows: CheckRow[], by: GroupBy, probes: ProbeStatus[], l: GroupLabels): CheckGroup[] {
  const byId = new Map(probes.map((p) => [p.id, p]));
  const groups = new Map<string, CheckGroup>();
  const put = (key: string, title: string, r: CheckRow) => {
    let g = groups.get(key);
    if (!g) groups.set(key, (g = { key, title, type: '', rows: [], problems: 0 }));
    g.rows.push(r);
  };
  for (const r of rows) {
    if (r.parent) continue;
    switch (by) {
      case 'group':
        put(`g:${r.group ?? ''}`, r.group || l.noGroup, r);
        break;
      case 'type':
        put(`t:${r.type}`, l.type(r.type), r);
        break;
      case 'status': {
        const k = statusKey(r);
        put(`s:${k}`, l.status[k], r);
        break;
      }
      case 'country': {
        const c = countryOf(r, byId);
        put(`c:${c}`, c === '*' ? l.multiCountry : c ? l.country(c) : l.noCountry, r);
        break;
      }
      case 'tag':
        if (!r.tags?.length) put('tag:', l.noTags, r);
        for (const tag of r.tags ?? []) put(`tag:${tag}`, `#${tag}`, r);
        break;
    }
  }
  const out = [...groups.values()];
  for (const g of out) {
    g.problems = g.rows.filter(isProblem).length;
    const ups = g.rows.filter((r) => !r.subscription && r.uptime_24h != null).map((r) => r.uptime_24h!);
    g.uptime = ups.length ? ups.reduce((a, b) => a + b, 0) / ups.length : undefined;
    const types = new Map<string, number>();
    for (const r of g.rows) types.set(r.type, (types.get(r.type) ?? 0) + 1);
    g.type = [...types.entries()].sort((a, b) => b[1] - a[1])[0]?.[0] ?? '';
  }
  return out.sort((a, b) => Number(b.problems > 0) - Number(a.problems > 0) || a.title.localeCompare(b.title));
}

/** matches says whether a check passes the search box and the status chips. */
export function matches(r: CheckRow, needle: string, st: StatusFilter): boolean {
  if (st === 'problems' && !isProblem(r)) return false;
  if (st === 'ok' && !isOK(r)) return false;
  if (st === 'paused' && !r.paused) return false;
  if (!needle) return true;
  return `${r.name} ${r.target ?? ''} ${r.id} ${(r.tags ?? []).join(' ')}`.toLowerCase().includes(needle);
}
