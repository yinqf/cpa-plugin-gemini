const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const html = fs.readFileSync(path.join(__dirname, '..', 'ui.html'), 'utf8');
const source = html.match(/<script data-monitor>([\s\S]*?)<\/script>/)[1];
const fixture = (extra = {}) => ({
  scope: 'antigravity', version: '0.0.3', processed: 7, relocated: 3, skipped: 4,
  observed_at: '2026-11-19T04:00:00Z', started_at: '2026-11-19T03:00:00Z', uptime_seconds: 3600,
  quiescing: false,
  config: { enabled: true, mode: 'auto', models: '*', keep_max_chars: 2000, trigger_word_count: 2, debug: false, priority: 0 },
  move_reasons: { length: 1, trigger: 1, always: 1 },
  skip_reasons: { no_change: 1, disabled: 1, quiescing: 1, model_filtered: 1 },
  ...extra,
});

function target() {
  return {
    listeners: {}, textContent: '', hidden: false, disabled: false, dataset: {}, value: '',
    addEventListener(name, listener) { this.listeners[name] = listener; },
    emit(name) { return this.listeners[name]?.({ target: this }); },
    set innerHTML(value) { throw new Error('Unsafe HTML rendering'); },
  };
}

function harness(fetcher) {
  const elements = Object.fromEntries([...html.matchAll(/id="([^"]+)"/g)].map(match => [match[1], target()]));
  elements.interval.value = '5000';
  elements.error.hidden = true;
  const document = { ...target(), hidden: false, getElementById: id => {
    assert.ok(elements[id], `Missing DOM element ${id}`);
    return elements[id];
  } };
  const window = { ...target(), location: { href: 'http://localhost/v0/resource/plugins/cpa-plugin-gemini/ui/?preview=1#top' } };
  const timers = new Map();
  const calls = [];
  let nextTimer = 0;
  const theme = { embedded: false, value: 'auto', get() { return this.value; }, set(value) { this.value = value; } };
  const context = {
    document, window, URL, Intl, Date, AbortController, monitorTheme: theme,
    setTimeout(callback, delay) { const id = ++nextTimer; timers.set(id, { callback, delay }); return id; },
    clearTimeout(id) { timers.delete(id); },
    fetch(url, options) { calls.push({ url, options }); return fetcher(url, options); },
  };
  vm.runInNewContext(source, context);
  return { elements, document, window, timers, calls, theme };
}

const response = data => ({ ok: true, json: async () => data });
const settle = () => new Promise(resolve => setImmediate(resolve));

test('renders counters and safe effective configuration from the resource endpoint', async () => {
  const data = fixture();
  data.config.models = '<img src=x onerror=alert(1)>';
  data.config.trigger_words = 'private-trigger-unique';
  const h = harness(async () => response(data));
  await settle();
  assert.equal(h.calls[0].url, 'http://localhost/v0/resource/plugins/cpa-plugin-gemini/status');
  assert.equal(h.calls[0].options.cache, 'no-store');
  assert.equal(h.elements.processed.textContent, '7');
  assert.equal(h.elements.relocated.textContent, '3');
  assert.equal(h.elements.skipped.textContent, '4');
  assert.equal(h.elements['config-models'].textContent, data.config.models);
  assert.equal(h.elements['config-triggers'].textContent, '2 条 · 不展示原文');
  assert.equal(h.elements['skip-inactive'].textContent, '2');
  assert.equal(h.elements.state.textContent, '运行中');
  assert.equal(h.elements.refresh.disabled, false);
  assert.equal([...h.timers.values()][0].delay, 5000);
  assert.ok(!Object.values(h.elements).some(element => element.textContent.includes('private-trigger-unique')));
});

test('retains the last successful snapshot on network and schema errors and recovers', async () => {
  let next = fixture();
  let fail = false;
  const h = harness(async () => {
    if (fail) throw new Error('private-server-body');
    return response(next);
  });
  await settle();
  fail = true;
  h.elements.refresh.emit('click');
  await settle();
  assert.equal(h.elements.processed.textContent, '7');
  assert.equal(h.elements.error.hidden, false);
  assert.ok(!h.elements.updated.textContent.includes('private-server-body'));
  fail = false;
  next = { scope: 'wrong' };
  h.elements.refresh.emit('click');
  await settle();
  assert.equal(h.elements.processed.textContent, '7');
  assert.equal(h.elements.error.hidden, false);
  next = fixture({ processed: 9, skipped: 6 });
  h.elements.refresh.emit('click');
  await settle();
  assert.equal(h.elements.processed.textContent, '9');
  assert.equal(h.elements.error.hidden, true);
});

test('manual and automatic refreshes never overlap; automatic refresh can be disabled', async () => {
  let finish;
  const h = harness(() => new Promise(resolve => { finish = resolve; }));
  assert.equal(h.elements.refresh.disabled, true);
  h.elements.refresh.emit('click');
  h.elements.refresh.emit('click');
  assert.equal(h.calls.length, 1);
  h.elements.interval.value = '0';
  h.elements.interval.emit('change');
  finish(response(fixture()));
  await settle();
  assert.equal(h.elements.refresh.disabled, false);
  assert.equal(h.timers.size, 0);
  h.elements.interval.value = '30000';
  h.elements.interval.emit('change');
  assert.equal([...h.timers.values()][0].delay, 30000);
});

test('hidden pages abort pending reads and resume polling after becoming visible', async () => {
  let first = true;
  const h = harness((url, options) => {
    if (!first) return Promise.resolve(response(fixture()));
    first = false;
    return new Promise((resolve, reject) => options.signal.addEventListener('abort', () => reject(new Error('aborted'))));
  });
  h.document.hidden = true;
  h.document.emit('visibilitychange');
  await settle();
  assert.equal(h.calls[0].options.signal.aborted, true);
  assert.equal(h.timers.size, 0);
  assert.equal(h.elements.error.hidden, true);
  h.document.hidden = false;
  h.document.emit('visibilitychange');
  await settle();
  assert.equal(h.calls.length, 2);
  assert.equal(h.elements.processed.textContent, '7');
  h.window.emit('pagehide');
  assert.equal(h.timers.size, 0);
  h.elements.refresh.emit('click');
  assert.equal(h.calls.length, 2);
  h.window.emit('pageshow');
  await settle();
  assert.equal(h.calls.length, 3);
});

test('empty and paused states remain clear and theme selection is forwarded', async () => {
  let data = fixture({ processed: 0, relocated: 0, skipped: 0 });
  data.config.mode = 'off';
  const h = harness(async () => response(data));
  await settle();
  assert.equal(h.elements.state.textContent, '已暂停');
  assert.equal(h.elements['relocation-rate'].textContent, '尚无处理记录');
  assert.equal(h.elements.processed.textContent, '0');
  h.elements.appearance.value = 'dark';
  h.elements.appearance.emit('change');
  assert.equal(h.theme.value, 'dark');
  data = fixture({ quiescing: true });
  h.elements.refresh.emit('click');
  await settle();
  assert.equal(h.elements.state.textContent, '已停止');
});
