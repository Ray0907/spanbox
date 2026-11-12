// Copyright (c) Meta Platforms, Inc. and affiliates.

import {StrictMode} from 'react';
import {createRoot} from 'react-dom/client';
import './layers.css';
import '@astryxdesign/core/reset.css';
import '@astryxdesign/theme-neutral/theme.css';
import './index.css';
import './spanbox.css';
import App from './App';
import {legacyHashPath} from './router';

const legacy = legacyHashPath(location.pathname, location.hash);
if (legacy) history.replaceState(null, '', legacy);

// eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- root element must exist in index.html
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
