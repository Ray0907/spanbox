import {useEffect, useRef, useState} from 'react';
import * as stylex from '@stylexjs/stylex';
import {Theme} from '@astryxdesign/core/theme';
import {neutralTheme} from '@astryxdesign/theme-neutral/built';
import {Text} from '@astryxdesign/core/Text';
import {Button} from '@astryxdesign/core/Button';
import {Link} from '@astryxdesign/core/Link';
import {matchRoute, onLinkClick} from './router';
import {ErrorLine} from './telemetry';
import Traces from './TracesPage';
import TracePage from './TracePage';
import SessionsPage from './SessionsPage';
import SessionPage from './SessionPage';
import SearchPage from './SearchPage';
import DashboardPage from './DashboardPage';
import SQLPage from './SQLPage';
import LoginPage from './LoginPage';

const styles = stylex.create({
  nav: {display: 'flex', flexWrap: 'wrap', gap: '8px 24px', alignItems: 'center', padding: '12px 24px',
    borderBottomWidth: 1, borderBottomStyle: 'solid', borderBottomColor: 'var(--color-border)', backgroundColor: 'var(--color-background-body)'},
});

function Nav({route}: {route: string}) {
  const [showSQL, setShowSQL] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    fetch('/sql?format=json', {signal: controller.signal})
      .then(response => setShowSQL(response.status !== 404))
      .catch(error => { if (error?.name !== 'AbortError') setShowSQL(true); });
    return () => controller.abort();
  }, []);
  const items = [['Traces', '/traces', ['traces', 'trace', 'span'].includes(route)],
    ['Sessions', '/sessions', route === 'sessions'], ['Dashboard', '/dashboard', route === 'dashboard'],
    ['Search', '/search', route === 'search'], ...(showSQL ? [['SQL', '/sql', route === 'sql']] : [])] as [string, string, boolean][];
  return <nav aria-label="Primary" {...stylex.props(styles.nav)}>
    <Text weight="semibold">spanbox</Text>
    {items.map(([label, href, current]) => <Button key={label} label={label} href={href} onClick={onLinkClick}
      aria-current={current ? 'page' : undefined} variant={current ? 'secondary' : 'ghost'} />)}
  </nav>;
}

function useRoute() {
  const [path, setPath] = useState(location.pathname + location.search);
  const [visit, setVisit] = useState(0); // bumps on every navigation so a repeated submit of the same URL refetches
  useEffect(() => {
    if (location.pathname === '/') {
      history.replaceState(null, '', '/traces' + location.search);
      setPath(location.pathname + location.search);
    }
    const update = () => {
      if (location.pathname === '/') history.replaceState(null, '', '/traces' + location.search);
      setPath(location.pathname + location.search);
      setVisit(n => n + 1);
    };
    window.addEventListener('popstate', update);
    return () => window.removeEventListener('popstate', update);
  }, []);
  const url = new URL(path, location.origin);
  return {route: matchRoute(url.pathname), search: url.search, visit};
}

export default function App() {
  const {route, search, visit} = useRoute();
  // In-page span and dashboard range changes keep focus on the control that changed them.
  const focusSearch = new URLSearchParams(search);
  if (route?.page === 'dashboard') focusSearch.delete('range');
  const path = route?.trace ? `/traces/${route.trace}` : route?.page === 'dashboard' ? '/dashboard?' + focusSearch : location.pathname + search;
  const previousPath = useRef(path);
  useEffect(() => {
    if (previousPath.current !== path) {
      const main = document.querySelector('main');
      if (main) { main.tabIndex = -1; main.focus({preventScroll: true}); }
    }
    previousPath.current = path;
  }, [path]);
  const sessionID = new URLSearchParams(search).get('id');
  const hasSkipLink = route?.page !== 'login';
  useEffect(() => {
    document.title = route?.page === 'sessions' ? `${sessionID ? 'Session ' + sessionID : 'Sessions'} · spanbox` :
      route?.page === 'search' ? 'Search · spanbox' : route?.page === 'dashboard' ? 'Dashboard · spanbox' : route?.page === 'sql' ? 'SQL · spanbox' : route?.page === 'login' ? 'Sign in · spanbox' : 'spanbox';
  }, [route?.page, sessionID]);
  return <Theme theme={neutralTheme}>
    {hasSkipLink && <Link href="#main" className="skip-link">Skip to main content</Link>}
    {route?.page !== 'login' && <Nav route={route?.page ?? ''} />}
    {route?.trace ? <TracePage key={route.trace} id={route.trace} spanId={route.span} /> :
      route?.page === 'traces' ? <Traces key={search + visit} search={search} /> :
      route?.page === 'sessions' ? (sessionID ? <SessionPage key={sessionID} id={sessionID} /> : <SessionsPage key={search + visit} search={search} />) :
      route?.page === 'search' ? <SearchPage key={search + visit} search={search} /> :
      route?.page === 'dashboard' ? <DashboardPage search={search} refresh={visit} /> :
      route?.page === 'sql' ? <SQLPage /> : route?.page === 'login' ? <LoginPage /> : <ErrorLine message="Page not found" />}
  </Theme>;
}
