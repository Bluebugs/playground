// SPMD Playground frontend.
// Loads examples from the manifest, drives the editor, and renders the three
// compiler-output tabs (Run / WASM / AVX2). The "WASM" tab shows WAT text
// (wasm2wat output); only the user-facing label says WASM, internals stay "wat".

import { Editor } from './resources/editor.bundle.min.js';
import { highlightWAT } from './highlight-wat.js';
import { highlightX86 } from './highlight-x86.js';

const API = '/api';
const EXAMPLE_BASE = '/examples/spmd';

let editor = null;
let manifest = [];
let currentExample = null;
let activeTab = 'run';
let simdEnabled = true;

// cache key = `${sha1(src)}|${simdEnabled}|${tab}|${symbols}`
const outputCache = new Map();

// One-shot worker per Run invocation (worker is terminated after it exits).
let runWorker = null;

// AbortController for in-flight fetches. Cancelled on tab switch / SIMD toggle
// / example change so stale responses don't stomp the active pane.
let pendingController = null;

async function init() {
  // Load manifest.
  try {
    manifest = await fetch(`${API}/examples`).then(r => r.json());
  } catch (e) {
    setPaneError(`failed to load /api/examples: ${e}`);
    return;
  }
  buildDropdown(manifest);

  // Editor. Must call setText() once before .text() works (initializes view).
  editor = new Editor(document.getElementById('editor'));
  editor.setText('');

  // Wire controls.
  document.getElementById('simd-toggle').addEventListener('change', onSimdToggle);
  document.getElementById('run-button').addEventListener('click', () => {
    activateTab('run');
    refreshActiveTab(/*force=*/true);
  });
  for (const t of document.querySelectorAll('#tabbar .tab')) {
    t.addEventListener('click', () => {
      activateTab(t.dataset.tab);
      refreshActiveTab();
    });
  }

  // Load first example.
  if (manifest.length > 0) await loadExample(manifest[0].key);
}

function buildDropdown(items) {
  const menu = document.getElementById('example-menu');
  menu.innerHTML = '';
  for (const ex of items) {
    const a = document.createElement('a');
    a.classList.add('dropdown-item');
    a.href = '#';
    a.textContent = ex.label;
    a.dataset.key = ex.key;
    a.addEventListener('click', (e) => {
      e.preventDefault();
      loadExample(ex.key);
    });
    menu.appendChild(a);
  }
}

async function loadExample(key) {
  const ex = manifest.find(e => e.key === key);
  if (!ex) return;
  currentExample = ex;
  document.getElementById('example-label').textContent = ex.label;
  document.getElementById('hint').textContent = ex.hint || '';
  // Mark the active item in the dropdown.
  for (const a of document.querySelectorAll('#example-menu .dropdown-item')) {
    a.classList.toggle('active', a.dataset.key === key);
  }
  // Fetch source.
  let src;
  try {
    src = await fetch(`${EXAMPLE_BASE}/${key}/main.go`).then(r => {
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      return r.text();
    });
  } catch (e) {
    setPaneError(`failed to load example ${key}: ${e}`);
    return;
  }
  editor.setText(src);
  refreshActiveTab();
}

function activateTab(name) {
  activeTab = name;
  for (const t of document.querySelectorAll('#tabbar .tab')) {
    const isActive = t.dataset.tab === name;
    t.classList.toggle('active', isActive);
    t.setAttribute('aria-selected', isActive ? 'true' : 'false');
  }
  const panel = document.getElementById('output-pane');
  if (panel) panel.setAttribute('aria-labelledby', `tab-${name}`);
}

function onSimdToggle(e) {
  simdEnabled = e.target.checked;
  refreshActiveTab();
}

function srcText() {
  return editor.text();
}

// Lightweight non-cryptographic hash; only used as a cache key.
function fastHash(s) {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return (h >>> 0).toString(16);
}

function cacheKey() {
  const sym = (currentExample?.symbols || []).join(',');
  return `${fastHash(srcText())}|${simdEnabled}|${activeTab}|${sym}`;
}

async function refreshActiveTab(force = false) {
  // Cancel any in-flight fetches and kill any running worker. Rapid tab
  // switches / SIMD toggles / example changes otherwise leave stale work
  // resolving later and stomping the active pane.
  if (pendingController) pendingController.abort();
  pendingController = new AbortController();
  const signal = pendingController.signal;
  if (runWorker) { try { runWorker.terminate(); } catch {} runWorker = null; }

  const key = cacheKey();
  if (!force && outputCache.has(key)) {
    return renderResult(outputCache.get(key));
  }
  setPaneText('Compiling…');
  try {
    const result = await fetchForActiveTab(signal);
    if (signal.aborted) return;
    outputCache.set(key, result);
    renderResult(result);
  } catch (e) {
    if (e && e.name === 'AbortError') return;
    renderResult({ kind: 'error', text: String(e && e.message || e) });
  }
}

async function fetchForActiveTab(signal) {
  const src = srcText();
  const body = src;
  const headers = { 'Content-Type': 'text/plain' };
  if (activeTab === 'wat' || activeTab === 'asm') {
    const syms = (currentExample?.symbols || []).join(',');
    const qs = `simd=${simdEnabled}` + (syms ? `&symbols=${encodeURIComponent(syms)}` : '');
    try {
      const r = await fetch(`${API}/${activeTab}?${qs}`, { method: 'POST', headers, body, signal });
      return { kind: activeTab, text: await r.text() };
    } catch (e) {
      if (e && e.name === 'AbortError') return { kind: 'aborted' };
      throw e;
    }
  }
  // Run: compile to wasi then execute in a worker.
  return runWASI(src, signal);
}

async function runWASI(src, signal) {
  // Kill any previous worker (refreshActiveTab also does this, but be safe).
  if (runWorker) {
    try { runWorker.terminate(); } catch {}
    runWorker = null;
  }
  // 1. Compile.
  const compileURL = `${API}/compile?format=wasi&compiler=tinygo&simd=${simdEnabled}`;
  let resp;
  try {
    resp = await fetch(compileURL, {
      method: 'POST',
      headers: { 'Content-Type': 'text/plain' },
      body: src,
      signal,
    });
  } catch (e) {
    if (e && e.name === 'AbortError') return { kind: 'aborted' };
    throw e;
  }
  const ct = resp.headers.get('Content-Type') || '';
  if (!ct.includes('application/wasm')) {
    // Compile error: server sent text body with 200.
    const text = await resp.text();
    if (signal.aborted) return { kind: 'aborted' };
    return { kind: 'error', text: `// compile failed:\n${text}` };
  }
  const bytes = new Uint8Array(await resp.arrayBuffer());

  // If user has moved on while we were downloading bytes, bail before
  // spawning a worker.
  if (signal.aborted) return { kind: 'aborted' };

  // 2. Run in worker.
  return new Promise((resolve) => {
    const worker = new Worker('worker/runner.js');
    runWorker = worker;
    let stdout = '';
    // 5s is generous for current examples (32x16 mandelbrot, 64-byte encode, etc.).
    // Revisit if examples with larger inputs are added.
    const timeout = setTimeout(() => {
      try { worker.terminate(); } catch {}
      resolve({ kind: 'run', text: stdout + '\n// (timed out after 5s)' });
    }, 5000);
    worker.onmessage = (e) => {
      const msg = e.data;
      switch (msg.type) {
        case 'stdout':
          stdout += msg.data;
          // Live-stream into the pane while we wait (unless we've been aborted).
          if (!signal.aborted) setPaneText(stdout);
          break;
        case 'error':
          clearTimeout(timeout);
          try { worker.terminate(); } catch {}
          if (signal.aborted) { resolve({ kind: 'aborted' }); break; }
          resolve({ kind: 'error', text: stdout + (stdout ? '\n' : '') + String(msg.message) });
          break;
        case 'exited':
          clearTimeout(timeout);
          try { worker.terminate(); } catch {}
          if (signal.aborted) { resolve({ kind: 'aborted' }); break; }
          resolve({ kind: 'run', text: stdout });
          break;
        // 'compiling' / 'loading' / 'started' are progress signals; ignore.
      }
    };
    worker.onerror = (e) => {
      clearTimeout(timeout);
      try { worker.terminate(); } catch {}
      if (signal.aborted) { resolve({ kind: 'aborted' }); return; }
      resolve({ kind: 'error', text: String(e.message || e) });
    };
    worker.postMessage({ type: 'start', sourceData: bytes });
  });
}

function renderResult(result) {
  const pane = document.getElementById('output-code');
  if (!result) { pane.textContent = ''; return; }
  if (result.kind === 'aborted') return; // a newer refresh has taken over
  switch (result.kind) {
    case 'wat':
      // If the server returned an error blob (no v128.* / proper sexpr), it
      // still renders as plain escaped text since the highlighter only adds
      // spans to recognized mnemonics.
      pane.innerHTML = highlightWAT(result.text || '');
      break;
    case 'asm':
      pane.innerHTML = highlightX86(result.text || '');
      break;
    case 'run':
      pane.textContent = result.text || '(no output)';
      break;
    case 'error':
      pane.textContent = result.text || '(error)';
      break;
    default:
      pane.textContent = '';
  }
}

function setPaneText(text) {
  document.getElementById('output-code').textContent = text;
}

function setPaneError(text) {
  setPaneText(text);
}

init();
