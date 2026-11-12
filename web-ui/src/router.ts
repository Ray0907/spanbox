import type {MouseEvent} from 'react';

export function matchRoute(path: string) {
  if (path === '/' || path === '/ui' || path === '/traces') return {page: 'traces', trace: undefined, span: undefined};
  const trace = /^\/traces\/([^/]+)$/.exec(path);
  if (trace) return {page: 'trace', trace: trace[1], span: undefined};
  const span = /^\/spans\/([^/]+)\/([^/]+)$/.exec(path);
  if (span) return {page: 'span', trace: span[1], span: span[2]};
  if (/^\/(sessions|search|dashboard|sql|login)$/.test(path)) return {page: path.slice(1), trace: undefined, span: undefined};
  return null;
}

export function navigate(path: string) {
  history.pushState(null, '', path);
  window.dispatchEvent(new Event('popstate'));
}

export function onLinkClick(event: MouseEvent<HTMLElement>) {
  const link = event.currentTarget as HTMLAnchorElement;
  if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey ||
      link.hasAttribute('target') || link.hasAttribute('download')) return;
  const url = new URL(link.href, location.href);
  if (url.origin !== location.origin || url.hash || url.searchParams.get('format') === 'json') return;
  const route = matchRoute(url.pathname);
  if (!route) return;
  event.preventDefault();
  navigate(url.pathname + url.search);
}

// /ui#/t/<id> was the hash route of the first UI build; keep those links working.
export function legacyHashPath(path: string, hash: string) {
  const m = path === '/ui' ? /^#\/t\/([^/]+)$/.exec(hash) : null;
  return m ? `/traces/${m[1]}` : null;
}
