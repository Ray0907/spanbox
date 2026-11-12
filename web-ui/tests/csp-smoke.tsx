import {useState} from 'react';
import {createRoot} from 'react-dom/client';
import {Theme} from '@astryxdesign/core/theme';
import {neutralTheme} from '@astryxdesign/theme-neutral/built';
import {VStack} from '@astryxdesign/core/Layout';
import {Button} from '@astryxdesign/core/Button';
import {Tooltip} from '@astryxdesign/core/Tooltip';
import {Selector} from '@astryxdesign/core/Selector';
import '../src/layers.css';
import '@astryxdesign/core/reset.css';
import '@astryxdesign/theme-neutral/theme.css';
import '../src/spanbox.css';

function Smoke() {
  const [value, setValue] = useState('one');
  return <Theme theme={neutralTheme}><VStack gap={4}>
    <Tooltip content="CSP tooltip smoke" focusTrigger="always" delay={0}><Button label="Open tooltip" /></Tooltip>
    <Selector label="Smoke selector" value={value} onChange={setValue} options={[{value: 'one', label: 'One'}, {value: 'two', label: 'Two'}]} />
  </VStack></Theme>;
}
createRoot(document.getElementById('root')!).render(<Smoke />);
