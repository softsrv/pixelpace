import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';

var appJs = readFileSync(new URL('./app.js', import.meta.url), 'utf8');

function element() {
  var listeners = {};
  var classes = new Set(['hidden']);
  return {
    textContent: '',
    listeners: listeners,
    classList: {
      add: function (c) { classes.add(c); },
      remove: function (c) { classes.delete(c); },
      toggle: function () {},
      contains: function (c) { return classes.has(c); }
    },
    getAttribute: function () { return ''; },
    querySelector: function () { return null; },
    closest: function () { return null; },
    addEventListener: function (type, fn) { listeners[type] = fn; }
  };
}

// Loads app.js on the dashboard and clicks "Connect Rowing Machine".
async function clickConnect(devMode, secure, navigator) {
  var connectBtn = element();
  var info = element();
  var card = element();
  card.getAttribute = function (name) { return name === 'data-dev-mode' ? String(devMode) : ''; };
  var byID = { 'bt-connect-btn': connectBtn, 'rowing-machine-card': card, 'bt-connection-info': info };
  runInNewContext(appJs, {
    document: {
      addEventListener: function () {},
      body: { addEventListener: function () {} },
      getElementById: function (id) { return byID[id] || null; },
      querySelector: function () { return null; },
      dispatchEvent: function () { return true; }
    },
    CustomEvent: function () {},
    window: { isSecureContext: secure, location: { href: '', protocol: secure ? 'https:' : 'http:', host: 'pixelpace.test' } },
    navigator: navigator,
    htmx: { trigger: function () {} },
    WebSocket: function () { throw new Error('offline'); },
    setTimeout: function () { return 0; },
    clearTimeout: function () {},
    setInterval: function () { return 0; },
    clearInterval: function () {}
  });
  await connectBtn.listeners.click();
  return { text: info.textContent, shown: !info.classList.contains('hidden') };
}

test('in development, an insecure page is reported as such, even in a browser with Bluetooth', async function () {
  for (var nav of [{}, { bluetooth: {} }]) {
    var info = await clickConnect(true, false, nav);
    assert.ok(info.shown);
    assert.match(info.text, /secure page/);
    assert.match(info.text, /HTTPS/);
    assert.doesNotMatch(info.text, /Chrome/);
  }
});

test('a secure page in a browser without Web Bluetooth asks for Chrome', async function () {
  for (var devMode of [true, false]) {
    var info = await clickConnect(devMode, true, {});
    assert.ok(info.shown);
    assert.match(info.text, /Google Chrome is required/);
    assert.doesNotMatch(info.text, /HTTPS|secure|localhost/);
  }
});

test('in production, customers never see the HTTPS or localhost advice', async function () {
  var info = await clickConnect(false, false, {});
  assert.ok(info.shown);
  assert.match(info.text, /Google Chrome is required/);
  assert.doesNotMatch(info.text, /HTTPS|secure|localhost/);
});

test('a secure page with Web Bluetooth goes on to search for the rower', async function () {
  var requested = false;
  var info = await clickConnect(false, true, {
    bluetooth: { requestDevice: function () { requested = true; return Promise.reject(new Error('cancelled')); } }
  });
  assert.ok(requested);
  assert.doesNotMatch(info.text, /Chrome is required|secure page/);
});
