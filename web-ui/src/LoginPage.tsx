import {useState} from 'react';
import * as stylex from '@stylexjs/stylex';
import {Card} from '@astryxdesign/core/Card';
import {VStack} from '@astryxdesign/core/Layout';
import {Heading, Text} from '@astryxdesign/core/Text';
import {TextInput} from '@astryxdesign/core/TextInput';
import {Button} from '@astryxdesign/core/Button';
import {Banner} from '@astryxdesign/core/Banner';

const styles = stylex.create({page: {padding: 24, minHeight: '100svh', display: 'grid', placeItems: 'center'}, card: {width: '100%', maxWidth: 420}});
export default function LoginPage() {
  const [token, setToken] = useState(''), [error, setError] = useState(''), [busy, setBusy] = useState(false);
  return <main id="main" {...stylex.props(styles.page)}><Card xstyle={styles.card}>
    <form method="post" action="/login" onSubmit={async event => {
      event.preventDefault();
      if (busy) return;
      setBusy(true); setError('');
      try {
        const response = await fetch('/login', {method: 'POST', body: new URLSearchParams({token})});
        setToken('');
        if (response.redirected && response.ok) {location.assign('/traces'); return;}
        setError(response.status === 401 ? 'Invalid token.' : response.status === 413 ? 'Request too large.' : 'Could not sign in. Please try again.');
      } catch {setError('Could not sign in. Please try again.');}
      finally {setBusy(false);}
    }}><VStack gap={4}>
      <Heading level={1}>Sign in to spanbox</Heading>
      <Text color="secondary">Enter the operator token to continue.</Text>
      <TextInput ref={input => {if (input) input.required = true;}} label="Token" htmlName="token" type="password" autoComplete="off" value={token} onChange={setToken} isRequired hasAutoFocus width="100%" />
      {error && <Banner status="error" title={error} role="alert" />}
      <Button label="Sign in" type="submit" variant="primary" isDisabled={busy} />
    </VStack></form>
  </Card></main>;
}
