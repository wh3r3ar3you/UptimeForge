import { createTheme, type MantineColorsTuple, type CSSVariablesResolver } from '@mantine/core';
import { theme as base } from './theme';

/** A theme is a handful of colours; Mantine palettes and chart colours are derived from them. */
export interface UfTheme {
  id: string;
  name: string;
  group: 'classic' | 'warm' | 'bright' | 'light';
  scheme: 'dark' | 'light';
  body: string; // page background
  surface: string; // cards, inputs, menus
  border: string;
  text: string;
  text2: string;
  dimmed: string;
  primary: string; // accent: buttons, active nav, focus
}

export const THEMES: UfTheme[] = [
  { id: 'forge', name: 'UptimeForge', group: 'classic', scheme: 'dark', body: '#121821', surface: '#19212c', border: '#2c3644', text: '#d6dde6', text2: '#aeb8c5', dimmed: '#8693a3', primary: '#20cfae' },
  { id: 'github-dark', name: 'GitHub Dark', group: 'classic', scheme: 'dark', body: '#0d1117', surface: '#161b22', border: '#30363d', text: '#e6edf3', text2: '#c9d1d9', dimmed: '#8b949e', primary: '#2f81f7' },
  { id: 'one-dark', name: 'One Dark', group: 'classic', scheme: 'dark', body: '#21252b', surface: '#282c34', border: '#3e4451', text: '#dcdfe4', text2: '#abb2bf', dimmed: '#7f848e', primary: '#61afef' },
  { id: 'light', name: 'Светлая', group: 'classic', scheme: 'light', body: '#f4f6f8', surface: '#ffffff', border: '#e3e6ea', text: '#14181f', text2: '#4b5361', dimmed: '#7a828d', primary: '#0c917a' },
  { id: 'catppuccin-mocha', name: 'Catppuccin Mocha', group: 'warm', scheme: 'dark', body: '#181825', surface: '#1e1e2e', border: '#313244', text: '#cdd6f4', text2: '#bac2de', dimmed: '#7f849c', primary: '#cba6f7' },
  { id: 'everforest-dark', name: 'Everforest Dark', group: 'warm', scheme: 'dark', body: '#272e33', surface: '#2e383c', border: '#414b50', text: '#d3c6aa', text2: '#bfb39a', dimmed: '#859289', primary: '#a7c080' },
  { id: 'vesper', name: 'Vesper', group: 'warm', scheme: 'dark', body: '#101010', surface: '#171717', border: '#2a2a2a', text: '#f2f2f2', text2: '#c8c8c8', dimmed: '#8b8b8b', primary: '#ffc799' },
  { id: 'dracula', name: 'Dracula', group: 'bright', scheme: 'dark', body: '#21222c', surface: '#282a36', border: '#44475a', text: '#f8f8f2', text2: '#e2e2dc', dimmed: '#7d88b8', primary: '#bd93f9' },
  { id: 'nuclear-dark', name: 'Nuclear Dark', group: 'bright', scheme: 'dark', body: '#1b2130', surface: '#222a3a', border: '#2f3a4f', text: '#e2e8f0', text2: '#c0c9d6', dimmed: '#7c8aa5', primary: '#ff2f92' },
  { id: 'shades-of-purple', name: 'Shades of Purple', group: 'bright', scheme: 'dark', body: '#1e1e3f', surface: '#2d2b55', border: '#433f7a', text: '#ffffff', text2: '#e0dcff', dimmed: '#a599e9', primary: '#fad000' },
  { id: 'vue-dark', name: 'Vue Dark', group: 'bright', scheme: 'dark', body: '#0f2429', surface: '#143238', border: '#22474e', text: '#e6f6f3', text2: '#c4e3de', dimmed: '#7fa9a3', primary: '#1fc8a8' },
  { id: 'night-owl-light', name: 'Night Owl Light', group: 'light', scheme: 'light', body: '#f0f0f0', surface: '#fbfbfb', border: '#d9d9d9', text: '#403f53', text2: '#5f5e72', dimmed: '#8c96a5', primary: '#4876d6' },
  { id: 'arduino-light', name: 'Arduino Light', group: 'light', scheme: 'light', body: '#eef2f2', surface: '#ffffff', border: '#d6e0e0', text: '#374146', text2: '#4e5b61', dimmed: '#7f8c8d', primary: '#00979c' },
  { id: 'achiever', name: 'Achiever', group: 'light', scheme: 'light', body: '#f3eeea', surface: '#fffbf8', border: '#e3d7cf', text: '#3a2f2a', text2: '#5c4e46', dimmed: '#8f7f75', primary: '#e9733d' },
];

export const DEFAULT_THEME = 'forge';

export function findTheme(id?: string): UfTheme {
  return THEMES.find((t) => t.id === id) ?? THEMES[0];
}

function hex(c: string): [number, number, number] {
  const n = parseInt(c.slice(1), 16);
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
}

/** mix blends a into b: w=0 is a, w=1 is b. */
export function mix(a: string, b: string, w: number): string {
  const x = hex(a), y = hex(b);
  return '#' + x.map((v, i) => Math.round(v + (y[i] - v) * w).toString(16).padStart(2, '0')).join('');
}

function shades(c: string): MantineColorsTuple {
  return [
    mix(c, '#ffffff', 0.9), mix(c, '#ffffff', 0.75), mix(c, '#ffffff', 0.55), mix(c, '#ffffff', 0.35), mix(c, '#ffffff', 0.15),
    c, mix(c, '#000000', 0.12), mix(c, '#000000', 0.25), mix(c, '#000000', 0.38), mix(c, '#000000', 0.5),
  ];
}

/** darkTuple maps a dark theme onto Mantine's dark scale: [0] text … [6] cards … [7] page … [9] deepest. */
function darkTuple(t: UfTheme): MantineColorsTuple {
  return [
    t.text, t.text2, t.dimmed, mix(t.dimmed, t.border, 0.5), t.border, mix(t.border, t.surface, 0.5),
    t.surface, t.body, mix(t.body, '#000000', 0.2), mix(t.body, '#000000', 0.4),
  ];
}

export function mantineTheme(t: UfTheme) {
  return createTheme({
    ...base,
    colors: { ...base.colors, forge: shades(t.primary), ...(t.scheme === 'dark' ? { dark: darkTuple(t) } : {}) },
    primaryColor: 'forge',
    primaryShade: { light: t.scheme === 'light' ? 6 : 6, dark: 5 },
  });
}

/** Light themes are not a Mantine scale: their surfaces come in as variables. */
export function cssVars(t: UfTheme): CSSVariablesResolver {
  const none: Record<string, string> = {};
  return () => ({
    variables: {
      '--uf-bg': t.body,
      '--uf-surface': t.surface,
      // The sidebar sits a step below the page in dark themes, on the card colour in light ones.
      '--uf-nav': t.scheme === 'dark' ? mix(t.body, '#000000', 0.18) : t.surface,
      '--uf-border': t.border,
    },
    light: t.scheme === 'light'
      ? {
          '--mantine-color-body': t.surface,
          '--mantine-color-text': t.text,
          '--mantine-color-dimmed': t.dimmed,
          '--mantine-color-default-border': t.border,
          '--mantine-color-default': t.surface,
          '--mantine-color-default-hover': mix(t.surface, t.border, 0.5),
        }
      : none,
    dark: none,
  });
}

/** Chart colours of a theme. Series hues stay the validated categorical order per scheme. */
export function chartPalette(t: UfTheme) {
  const series = t.scheme === 'dark'
    ? ['#3987e5', '#d95926', '#199e70', '#c98500', '#d55181', '#008300', '#9085e9', '#e66767']
    : ['#2a78d6', '#eb6834', '#1baf7a', '#eda100', '#e87ba4', '#008300', '#6250d6', '#e34948'];
  return {
    series,
    surface: t.surface,
    grid: mix(t.border, t.surface, 0.35),
    axis: t.border,
    muted: t.dimmed,
    text: t.text,
    textSecondary: t.text2,
  };
}
