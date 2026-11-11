import { createTheme, type MantineColorsTuple, rem } from '@mantine/core';

// Brand accent: teal leaning to cyan, the "forge glow" of the logo.
const forge: MantineColorsTuple = [
  '#e2fdf8', '#c5f6ec', '#94eedb', '#5fe4c9', '#38d9b9', '#20cfae', '#14b597', '#0c917a', '#0a7262', '#08564b',
];

// Navy greys for the dark scheme: [0] text … [7] page … [9] deepest.
const dark: MantineColorsTuple = [
  '#d6dde6', '#aeb8c5', '#8693a3', '#5f6b7b', '#2c3644', '#222b37', '#19212c', '#121821', '#0e131b', '#0a0e14',
];

export const theme = createTheme({
  primaryColor: 'forge',
  primaryShade: { light: 7, dark: 5 },
  colors: { forge, dark },
  // The fonts are the user's pick (fonts.ts sets the variables).
  fontFamily: '"Twemoji Country Flags", var(--uf-font, system-ui), system-ui, -apple-system, "Segoe UI", sans-serif',
  fontFamilyMonospace: '"Twemoji Country Flags", var(--uf-mono, ui-monospace), ui-monospace, SFMono-Regular, Menlo, monospace',
  headings: { fontFamily: '"Twemoji Country Flags", var(--uf-font, system-ui), system-ui, sans-serif', fontWeight: '650' },
  defaultRadius: 'md',
  radius: { md: rem(10), lg: rem(14) },
  cursorType: 'pointer',
  components: {
    Card: { defaultProps: { withBorder: true, radius: 'lg', padding: 'lg' } },
    Paper: { defaultProps: { radius: 'lg' } },
    Tooltip: { defaultProps: { withArrow: true, openDelay: 120, transitionProps: { duration: 120 } } },
    Badge: { defaultProps: { radius: 'sm' } },
    Modal: { defaultProps: { radius: 'lg', centered: true, overlayProps: { blur: 3, backgroundOpacity: 0.55 } } },
    Drawer: { defaultProps: { overlayProps: { blur: 3, backgroundOpacity: 0.55 } } },
  },
});

// Chart roles. Series hues are the validated categorical order; a probe
// keeps its colour for life (assigned by its position in the probe list).
export const palette = {
  dark: {
    series: ['#3987e5', '#d95926', '#199e70', '#c98500', '#d55181', '#008300', '#9085e9', '#e66767'],
    surface: '#19212c',
    grid: '#273141',
    axis: '#3a4555',
    muted: '#8693a3',
    text: '#d6dde6',
    textSecondary: '#aeb8c5',
  },
  light: {
    series: ['#2a78d6', '#eb6834', '#1baf7a', '#eda100', '#e87ba4', '#008300', '#6250d6', '#e34948'],
    surface: '#ffffff',
    grid: '#e6e8eb',
    axis: '#c3c8cf',
    muted: '#7a828d',
    text: '#14181f',
    textSecondary: '#4b5361',
  },
};

// Status colours are reserved for state and never reused for series.
export const status = {
  ok: '#0ca30c',
  partial: '#fab219',
  blocked: '#ec835a',
  down: '#d03b3b',
  unknown: '#6b7685',
  paused: '#6b7685',
} as const;
