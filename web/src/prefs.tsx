import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { api } from './api';

/** Prefs are a user's view settings, kept on the hub so every device shows the same. */
export interface Prefs {
  theme?: string;
  signal?: string; // indicator palette id (themes.ts SIGNALS)
  density?: 'compact' | 'normal' | 'comfortable';
  columns?: string[]; // visible server table columns, in order
  widths?: Record<string, number>; // column widths in px
  sort?: [string, boolean]; // column, descending
  status?: 'all' | 'online' | 'offline' | 'problems';
  font?: string; // interface font id (fonts.ts)
  numFont?: string; // numbers font id: "ui" or a monospace one
  bar?: BarStyle; // how usage bars are drawn in the systems table
  checks?: { groupBy?: 'group' | 'type' | 'status' | 'country' | 'tag'; collapsed?: string[]; view?: 'list' | 'tiles' };
}

export type BarStyle = 'pill' | 'line' | 'thin' | 'segments' | 'gradient' | 'ring' | 'none';

const LOCAL = 'uf-prefs';

function readLocal(): Prefs {
  try {
    return JSON.parse(localStorage.getItem(LOCAL) ?? '{}') as Prefs;
  } catch {
    return {};
  }
}

function writeLocal(p: Prefs) {
  try {
    localStorage.setItem(LOCAL, JSON.stringify(p));
  } catch {
    /* storage unavailable: the hub copy still holds */
  }
}

const Ctx = createContext<{ prefs: Prefs; setPrefs: (patch: Partial<Prefs>) => void }>({ prefs: {}, setPrefs: () => {} });

/**
 * PrefsProvider starts from the browser's copy (no flash of the wrong theme),
 * then takes the hub's once signed in; changes go to both, the hub's
 * debounced so dragging a column edge is one request.
 */
export function PrefsProvider({ children, user }: { children: ReactNode; user?: number }) {
  const [prefs, set] = useState<Prefs>(readLocal);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  // Whose prefs are loaded: signing out forgets it, so the next user (or the
  // same one again) gets theirs from the hub, not the previous user's.
  const loaded = useRef<number | undefined>(undefined);

  useEffect(() => {
    if (user == null) {
      loaded.current = undefined;
      if (timer.current) clearTimeout(timer.current); // a pending save belongs to the account that left
      return;
    }
    if (loaded.current === user) return;
    loaded.current = user;
    api<Prefs>('GET', '/api/v1/me/prefs')
      .then((server) => {
        if (loaded.current !== user) return; // signed out meanwhile
        const next = server ?? {};
        set(next);
        writeLocal(next);
      })
      .catch(() => {});
  }, [user]);

  const setPrefs = useCallback((patch: Partial<Prefs>) => {
    set((cur) => {
      const next = { ...cur, ...patch };
      writeLocal(next);
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => {
        api('PUT', '/api/v1/me/prefs', next).catch(() => {});
      }, 600);
      return next;
    });
  }, []);

  const value = useMemo(() => ({ prefs, setPrefs }), [prefs, setPrefs]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export const usePrefs = () => useContext(Ctx);
