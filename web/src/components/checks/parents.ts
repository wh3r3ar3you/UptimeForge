import { useMemo } from 'react';
import { useChecks, useProbes, useRemnawave } from '../../api';

// Names change rarely: these read the cache and let the pages that show the
// lists keep them fresh, instead of polling three endpoints of their own.
const cached = { live: false } as const;

/**
 * useParentLabels names a check's parents for people: a check by its name,
 * rw:<uuid> as the panel node, probe:<id> as the server, node:<name> as is.
 */
export function useParentLabels() {
  const checks = useChecks(cached);
  const probes = useProbes(cached);
  const rw = useRemnawave(cached);
  return useMemo(() => {
    const names = new Map<string, string>();
    for (const c of checks.data?.checks ?? []) names.set(c.id, c.name);
    for (const p of probes.data?.probes ?? []) names.set(`probe:${p.id}`, `🖥 ${p.name}`);
    for (const n of rw.data?.nodes ?? []) names.set(`rw:${n.uuid}`, `🛰 ${n.name}`);
    return (key: string) => names.get(key) ?? (key.startsWith('node:') ? `🛰 ${key.slice(5)}` : key);
  }, [checks.data, probes.data, rw.data]);
}

/** useParentOptions are the choices for a hand-set dependency. */
export function useParentOptions(self?: string) {
  const checks = useChecks(cached);
  const probes = useProbes(cached);
  const rw = useRemnawave(cached);
  return useMemo(() => [
    { group: 'checks', items: (checks.data?.checks ?? []).filter((c) => !c.parent && !c.subscription && c.id !== self).map((c) => ({ value: c.id, label: c.name })) },
    { group: 'nodes', items: (rw.data?.nodes ?? []).map((n) => ({ value: `node:${n.name}`, label: `🛰 ${n.name}` })) },
    { group: 'servers', items: (probes.data?.probes ?? []).filter((p) => p.mode === 'agent').map((p) => ({ value: `probe:${p.id}`, label: `🖥 ${p.name}` })) },
  ], [checks.data, probes.data, rw.data, self]);
}
