import {useEffect, useState} from 'react';
import * as stylex from '@stylexjs/stylex';
import {Text} from '@astryxdesign/core/Text';
import type {TextWindow} from './traces';
import {getJSON} from './http';
export {getJSON, HTTPError} from './http';

export type TraceItem = {
  trace_id: string; name: string; service: string; models: string; start_ns: number;
  span_count: number; input_tokens: number; output_tokens: number; cost_usd: number | null;
  duration_ms: number; has_error: boolean;
};
export type Span = {
  span_id: string; parent_span_id: string; kind: string; name: string; model: string;
  duration_ms: number; start_offset_ms: number; input_tokens: number | null; output_tokens: number | null;
  cost_usd: number | null; status_code: number;
};
export type TraceDetail = TraceItem & {session_id: string; spans: Span[]};
export const spanFields = ['input', 'output', 'attributes', 'events', 'links', 'resource', 'scope'] as const;
export type SpanField = typeof spanFields[number];
export type SpanDetail = Span & Record<SpanField, TextWindow> & {provider: string; status_message: string};

export function useJSON<T>(url: string | null, refresh = 0) {
  const [state, set] = useState<{data?: T; error?: Error; url?: string}>({});
  useEffect(() => {
    if (!url) return;
    let live = true;
    getJSON<T>(url).then(
      data => live && set({data, url}),
      (error: Error) => live && set({error, url}),
    );
    return () => { live = false; };
  }, [url, refresh]);
  const ready = state.url === url;
  return {data: ready ? state.data : undefined, error: ready ? state.error : undefined, loading: !!url && !ready};
}

export const fmtTime = (ns: number) => new Date(ns / 1e6).toISOString().replace('T', ' ').slice(0, 19);
export const fmtCost = (cost: number | null | undefined) => cost == null ? '—' : `$${cost.toFixed(6)}`;
export const fmtMs = (ms: number) => `${ms.toFixed(1)} ms`;
export const pageStyles = stylex.create({
  page: {padding: 24, display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', alignContent: 'start', gap: 20, backgroundColor: 'var(--color-background-body)'},
});
export function ErrorLine({message}: {message: string | Error}) {
  return <Text color="accent" role="alert">Unable to load data: {message instanceof Error ? message.message : message}</Text>;
}
