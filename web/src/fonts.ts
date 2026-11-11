/**
 * Fonts a user can pick. Each loads on demand, so only the chosen ones are
 * downloaded. All of them carry Cyrillic.
 */
export interface UfFont {
  id: string;
  name: string;
  family: string; // CSS font-family name
  load?: () => Promise<unknown>;
}

export const UI_FONTS: UfFont[] = [
  { id: 'inter', name: 'Inter', family: '"Inter Variable"', load: () => import('@fontsource-variable/inter') },
  { id: 'manrope', name: 'Manrope', family: '"Manrope Variable"', load: () => import('@fontsource-variable/manrope') },
  { id: 'onest', name: 'Onest', family: '"Onest Variable"', load: () => import('@fontsource-variable/onest') },
  { id: 'golos', name: 'Golos', family: '"Golos Text Variable"', load: () => import('@fontsource-variable/golos-text') },
  { id: 'plex', name: 'IBM Plex Sans', family: '"IBM Plex Sans Variable"', load: () => import('@fontsource-variable/ibm-plex-sans') },
  { id: 'roboto', name: 'Roboto', family: '"Roboto Variable"', load: () => import('@fontsource-variable/roboto') },
  { id: 'montserrat', name: 'Montserrat', family: '"Montserrat Variable"', load: () => import('@fontsource-variable/montserrat/index.css') },
  { id: 'system', name: 'System', family: 'system-ui, -apple-system, "Segoe UI"' },
];

/** Numbers either share the interface font (tabular figures) or use a monospace one. */
export const NUM_FONTS: UfFont[] = [
  { id: 'ui', name: '', family: '' },
  { id: 'jetbrains', name: 'JetBrains Mono', family: '"JetBrains Mono Variable"', load: () => import('@fontsource-variable/jetbrains-mono/index.css') },
  { id: 'plex-mono', name: 'IBM Plex Mono', family: '"IBM Plex Mono"',
    load: () => Promise.all([import('@fontsource/ibm-plex-mono/400.css'), import('@fontsource/ibm-plex-mono/500.css'), import('@fontsource/ibm-plex-mono/600.css')]) },
  { id: 'roboto-mono', name: 'Roboto Mono', family: '"Roboto Mono Variable"', load: () => import('@fontsource-variable/roboto-mono') },
  { id: 'fira', name: 'Fira Code', family: '"Fira Code Variable"', load: () => import('@fontsource-variable/fira-code') },
];

export const DEFAULT_UI_FONT = 'inter';
export const DEFAULT_NUM_FONT = 'ui';

const find = (list: UfFont[], id: string | undefined, def: string) => list.find((f) => f.id === id) ?? list.find((f) => f.id === def)!;

/** applyFonts loads the picked fonts and points the page's font variables at them. */
export function applyFonts(ui?: string, num?: string) {
  const u = find(UI_FONTS, ui, DEFAULT_UI_FONT);
  const n = find(NUM_FONTS, num, DEFAULT_NUM_FONT);
  u.load?.().catch(() => {});
  n.load?.().catch(() => {});
  const root = document.documentElement.style;
  root.setProperty('--uf-font', u.family);
  root.setProperty('--uf-mono', n.id === 'ui' ? u.family : n.family);
}

/** Preloading every font for the picker's samples: called only when it opens. */
export function loadAllFonts() {
  for (const f of [...UI_FONTS, ...NUM_FONTS]) f.load?.().catch(() => {});
}
