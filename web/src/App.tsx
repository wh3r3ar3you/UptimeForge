import { Center, Loader } from '@mantine/core';
import { lazy, Suspense, type ReactNode } from 'react';
import { Navigate, Route, Routes, useLocation, useParams } from 'react-router';
import { useAuth } from './api';
import { ErrorBoundary } from './components/ErrorBoundary';
import { Layout } from './components/Layout';
import { Login } from './pages/Login';

const Overview = lazy(() => import('./pages/Overview'));
const Checks = lazy(() => import('./pages/Checks'));
const CheckDetail = lazy(() => import('./pages/CheckDetail'));
const ProbeDetail = lazy(() => import('./pages/ProbeDetail'));
const TelegramProxy = lazy(() => import('./pages/TelegramProxy'));
const Incidents = lazy(() => import('./pages/Incidents'));
const Tools = lazy(() => import('./pages/Tools'));
const Remnawave = lazy(() => import('./pages/Remnawave'));
const Notifications = lazy(() => import('./pages/settings/Notifications'));
const Templates = lazy(() => import('./pages/settings/Templates'));
const Telegram = lazy(() => import('./pages/settings/Telegram'));
const RemnawaveSettings = lazy(() => import('./pages/settings/RemnawaveSettings'));
const General = lazy(() => import('./pages/settings/General'));
const Users = lazy(() => import('./pages/settings/Users'));
const StatusPages = lazy(() => import('./pages/settings/StatusPages'));
const PublicStatus = lazy(() => import('./pages/PublicStatus'));

/** A new id is a new page: no state (time window, picked probes, an open form) carries over. */
function CheckDetailRoute() {
  const { id = '' } = useParams();
  return <CheckDetail key={id} />;
}

const spinner = (
  <Center h="60vh">
    <Loader color="forge" type="dots" />
  </Center>
);

function Authed({ children }: { children: ReactNode }) {
  const auth = useAuth();
  const loc = useLocation();
  if (auth.isLoading) return spinner;
  if (!auth.data?.user) return <Login setup={!!auth.data?.setup_required} from={loc.pathname} />;
  // The inner boundary keeps the navigation when a page breaks; keyed by path,
  // it clears when the user goes elsewhere.
  return (
    <ErrorBoundary>
      <Layout>
        <ErrorBoundary key={loc.pathname}>
          <Suspense fallback={spinner}>{children}</Suspense>
        </ErrorBoundary>
      </Layout>
    </ErrorBoundary>
  );
}

export function App() {
  return (
    <Routes>
      <Route path="/status/:slug" element={<ErrorBoundary><Suspense fallback={spinner}><PublicStatus /></Suspense></ErrorBoundary>} />
      <Route
        path="*"
        element={
          <Authed>
            <Routes>
              <Route path="/" element={<Overview />} />
              <Route path="/checks" element={<Checks />} />
              <Route path="/checks/:id" element={<CheckDetailRoute />} />
              <Route path="/probes" element={<Navigate to="/" replace />} />
              <Route path="/probes/:id" element={<ProbeDetail />} />
              <Route path="/telegram-proxy" element={<TelegramProxy />} />
              <Route path="/incidents" element={<Incidents />} />
              <Route path="/tools" element={<Tools />} />
              <Route path="/remnawave" element={<Remnawave />} />
              <Route path="/settings/telegram" element={<Telegram />} />
              <Route path="/settings/remnawave" element={<RemnawaveSettings />} />
              <Route path="/settings/notifications" element={<Notifications />} />
              <Route path="/settings/templates" element={<Templates />} />
              <Route path="/settings/general" element={<General />} />
              <Route path="/settings/users" element={<Users />} />
              <Route path="/settings/status-pages" element={<StatusPages />} />
              <Route path="*" element={<Navigate to="/" replace />} />
            </Routes>
          </Authed>
        }
      />
    </Routes>
  );
}
