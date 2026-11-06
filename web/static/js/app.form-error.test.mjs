import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';

var appJs = readFileSync(new URL('./app.js', import.meta.url), 'utf8');

function inert() {
  return {
    textContent: '',
    classList: { add: function () {}, remove: function () {}, toggle: function () {}, contains: function () { return false; } },
    getAttribute: function () { return ''; },
    querySelector: function () { return null; },
    closest: function () { return null; },
    addEventListener: function () {}
  };
}

// Loads app.js and returns the htmx:response:error listeners and the error element.
function load() {
  var listeners = [];
  var errorEl = { textContent: '' };
  runInNewContext(appJs, {
    document: {
      addEventListener: function (type, fn) { if (type === 'htmx:response:error') listeners.push(fn); },
      body: { addEventListener: function () {} },
      getElementById: function () { return inert(); },
      querySelector: function (sel) { return sel === '#create-room-error' ? errorEl : null; },
      querySelectorAll: function () { return []; },
      dispatchEvent: function () { return true; }
    },
    CustomEvent: function () {},
    window: { isSecureContext: true, location: { href: '', protocol: 'https:', host: 'pixelpace.test' } },
    navigator: {},
    htmx: { trigger: function () {} },
    WebSocket: function () { throw new Error('offline'); },
    setTimeout: function () { return 0; },
    clearTimeout: function () {},
    setInterval: function () { return 0; },
    clearInterval: function () {},
    console: console
  });
  assert.ok(listeners.length > 0, 'app.js listens for htmx:response:error');
  // The refused action: a form marked with its error target and fallback text.
  var form = {
    getAttribute: function (name) {
      return { 'data-error-target': '#create-room-error', 'data-error-message': "Couldn't create the race. Please try again." }[name] || null;
    }
  };
  var source = { closest: function () { return form; } };
  return {
    errorEl: errorEl,
    fail: function (headers) {
      var evt = { target: source, detail: { ctx: { response: { headers: headers } } } };
      listeners.forEach(function (fn) { fn(evt); });
    }
  };
}

test('a refusal that names its reason shows that reason under the form', function () {
  var page = load();
  page.fail(new Headers({ 'X-Form-Error': 'warmup minutes must be a whole number from 1 to 20' }));
  assert.equal(page.errorEl.textContent, 'warmup minutes must be a whole number from 1 to 20');
});

test('a refusal with no reason shows the form\'s own message', function () {
  var page = load();
  page.fail(new Headers());
  assert.equal(page.errorEl.textContent, "Couldn't create the race. Please try again.");
});
