import {useState} from 'react';
import * as stylex from '@stylexjs/stylex';
import {HStack, VStack} from '@astryxdesign/core/Layout';
import {Heading, Text} from '@astryxdesign/core/Text';
import {TextInput} from '@astryxdesign/core/TextInput';
import {Button} from '@astryxdesign/core/Button';
import {Link} from '@astryxdesign/core/Link';
import {Badge} from '@astryxdesign/core/Badge';
import {List, ListItem} from '@astryxdesign/core/List';
import {EmptyState} from '@astryxdesign/core/EmptyState';
import {searchQuery, searchPageQuery, snippetParts} from './search';
import type {SearchPage as SearchData} from './search';
import {sessionTime} from './sessions';
import {navigate, onLinkClick} from './router';
import {ErrorLine, pageStyles, useJSON, fmtTime} from './telemetry';

const styles = stylex.create({
  field: {flexGrow: 1, minWidth: 0},
  time: {whiteSpace: 'nowrap'},
  snippet: {whiteSpace: 'pre-wrap', overflowWrap: 'anywhere'},
  mark: {backgroundColor: 'var(--color-accent-muted)', color: 'var(--color-text-primary)', borderRadius: 2, paddingInline: 2},
});
export default function SearchPage({search}: {search: string}) {
  const q = searchQuery(search);
  const [draft, setDraft] = useState(q);
  const first = useJSON<SearchData>(searchPageQuery(q, null, 0));
  const second = useJSON<SearchData>(first.data?.next_cursor ? searchPageQuery(q, first.data.next_cursor, first.data.items.length) : null);
  const loading = first.loading || second.loading;
  const error = first.error || second.error;
  const hits = [...(first.data?.items ?? []), ...(second.data?.items ?? [])];
  return <main id="main" tabIndex={-1} {...stylex.props(pageStyles.page)}>
    <VStack gap={1}><Heading level={1}>Search</Heading><Text color="secondary">Find a literal phrase in span names, prompts, and completions.</Text></VStack>
    <form onSubmit={event => {event.preventDefault(); navigate('/search?' + new URLSearchParams({q: draft}));}}>
      <HStack gap={2} align="end">
        <TextInput label="Phrase" htmlName="q" value={draft} onChange={setDraft} placeholder="What is the weather?" hasAutoFocus containerXstyle={styles.field} />
        <Button label="Search" variant="primary" type="submit" />
      </HStack>
    </form>
    {error && <ErrorLine message={error} />}
    {q && <section className="search-results" aria-label={`Results for “${q}”`}>
      <VStack gap={4} width="100%">
        <Heading level={2}>Results for “{q}”</Heading>
        {loading ? <Text role="status">Loading…</Text> : !error && (hits.length ? <List hasDividers edge="flush">{hits.map(hit => <ListItem key={hit.trace_id + ':' + hit.span_id}
          label={<article><VStack gap={1} width="100%">
            <HStack gap={2} wrap="wrap" align="center" width="100%">
              <Badge label={hit.kind} />
              <Link href={`/spans/${hit.trace_id}/${hit.span_id}`} onClick={onLinkClick}>{hit.span_name}</Link>
              <Link href={`/traces/${hit.trace_id}`} onClick={onLinkClick}>{hit.trace_name}</Link>
              <Text color="secondary" xstyle={styles.field}>{hit.model}</Text>
              <Text color="secondary" xstyle={styles.time}><time dateTime={sessionTime(hit.start_ns)}>{fmtTime(hit.start_ns)}</time> UTC</Text>
            </HStack>
            <Text className="search-snippet" xstyle={styles.snippet}>{snippetParts(hit.snippet).map((part, i) => part.match ? <mark key={i} {...stylex.props(styles.mark)}>{part.text}</mark> : part.text)}</Text>
          </VStack></article>} />)}</List> : <EmptyState title="No matching spans" description="Try a shorter literal phrase." />)}
      </VStack>
    </section>}
  </main>;
}
