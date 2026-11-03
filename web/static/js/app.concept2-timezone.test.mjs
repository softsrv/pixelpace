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

// A form whose matches() answers for the given selector, with a hidden tz input.
function form(isConnect) {
  var tz = { value: 'untouched' };
  return {
    tz: tz,
    matches: function (sel) { return isConnect && sel === 'form[data-concept2-connect]'; },
    querySelector: function (sel) { return sel === 'input[name="tz"]' ? tz : null; }
  };
}

// Loads app.js with the given Intl and returns the document submit listeners.
function load(intl) {
  var submit = [];
  runInNewContext(appJs, {
    document: {
      addEventListener: function (type, fn) { if (type === 'submit') submit.push(fn); },
      body: { addEventListener: function () {} },
      getElementById: function () { return inert(); },
      querySelector: function () { return null; },
      querySelectorAll: function () { return []; },
      dispatchEvent: function () { return true; }
    },
    Intl: intl,
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
  assert.ok(submit.length > 0, 'app.js registers a document submit listener');
  return function (target) { submit.forEach(function (fn) { fn({ target: target }); }); };
}

var berlinIntl = { DateTimeFormat: function () { return { resolvedOptions: function () { return { timeZone: 'Europe/Berlin' }; } }; } };

test('submitting the Concept2 connect form fills tz from Intl', function () {
  var submit = load(berlinIntl);
  var f = form(true);
  submit(f);
  assert.equal(f.tz.value, 'Europe/Berlin');
});

test('a throwing Intl leaves tz empty', function () {
  var submit = load({ DateTimeFormat: function () { throw new Error('no Intl'); } });
  var f = form(true);
  submit(f);
  assert.equal(f.tz.value, '');
});

test('other forms are untouched', function () {
  var submit = load(berlinIntl);
  var f = form(false);
  submit(f);
  assert.equal(f.tz.value, 'untouched');
});
