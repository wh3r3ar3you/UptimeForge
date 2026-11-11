import '@mantine/core/styles.css';
import '@mantine/notifications/styles.css';
import './styles.css';

import { MantineProvider } from '@mantine/core';
import { ModalsProvider } from '@mantine/modals';
import { Notifications } from '@mantine/notifications';
import { QueryClientProvider } from '@tanstack/react-query';
import { StrictMode, useEffect, useLayoutEffect, useMemo } from 'react';
import { createRoot } from 'react-dom/client';
import { createBrowserRouter, RouterProvider } from 'react-router';
import { queryClient, useAuth } from './api';
import { App } from './App';
import { reloadForNewVersion, RouteError } from './components/ErrorBoundary';
import { LangProvider } from './i18n';
import { PrefsProvider, usePrefs } from './prefs';
import { applyFonts } from './fonts';
import { cssVars, findTheme, mantineTheme } from './themes';

// A data router, so a page can hold the user back from leaving unsaved changes
// (useBlocker); the app keeps its own descendant <Routes>.
const router = createBrowserRouter([{ path: '*', element: <App />, errorElement: <RouteError /> }]);

// Vite fires this when a lazily loaded chunk or its CSS is gone: the hub was
// updated under this tab. Reload once for the new build.
window.addEventListener('vite:preloadError', (e) => {
  if (reloadForNewVersion()) e.preventDefault();
});

/** Themed applies the user's theme: Mantine palettes, colour scheme and page variables. */
function Themed() {
  const { prefs } = usePrefs();
  const t = findTheme(prefs.theme);
  const mt = useMemo(() => mantineTheme(t), [t]);
  const vars = useMemo(() => cssVars(t), [t]);
  useEffect(() => {
    document.documentElement.dataset.ufTheme = t.id;
  }, [t]);
  useLayoutEffect(() => applyFonts(prefs.font, prefs.numFont), [prefs.font, prefs.numFont]);
  return (
    <MantineProvider theme={mt} forceColorScheme={t.scheme} cssVariablesResolver={vars}>
      <ModalsProvider>
        <Notifications position="top-right" />
        <RouterProvider router={router} />
      </ModalsProvider>
    </MantineProvider>
  );
}

function Root() {
  const auth = useAuth();
  return (
    <PrefsProvider user={auth.data?.user?.id}>
      <Themed />
    </PrefsProvider>
  );
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <LangProvider>
        <Root />
      </LangProvider>
    </QueryClientProvider>
  </StrictMode>,
);
