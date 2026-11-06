import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';

var appJs = readFileSync(new URL('./app.js', import.meta.url), 'utf8');
var dashboardHtml = readFileSync(new URL('../../templates/dashboard.html', import.meta.url), 'utf8');

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

// The race card's mode options, read from the real template, so the test
// follows the markup players get.
function modeOptions() {
  var select = dashboardHtml.match(/<select name="mode"[^>]*>([\s\S]*?)<\/select>/);
  assert.ok(select, 'dashboard.html has the mode select');
  return Array.from(select[1].matchAll(/<option value="([^"]+)" data-submit-label="([^"]+)"/g), function (m) {
    return { value: m[1], label: m[2], getAttribute: function (name) { return name === 'data-submit-label' ? m[2] : null; } };
  });
}

// Loads app.js and returns its document change listeners and the label element.
function load() {
  var change = [];
  var label = { textContent: 'Create race' };
  runInNewContext(appJs, {
    document: {
      addEventListener: function (type, fn) { if (type === 'change') change.push(fn); },
      body: { addEventListener: function () {} },
      getElementById: function () { return inert(); },
      querySelector: function (sel) { return sel === '#create-room-submit-label' ? label : null; },
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
  assert.ok(change.length > 0, 'app.js registers a document change listener');
  return { label: label, fire: function (target) { change.forEach(function (fn) { fn({ target: target }); }); } };
}

function modeSelect(options, value) {
  return {
    options: options,
    selectedIndex: options.findIndex(function (o) { return o.value === value; }),
    matches: function (sel) { return sel === 'select[data-submit-label-target]'; },
    getAttribute: function (name) { return name === 'data-submit-label-target' ? '#create-room-submit-label' : null; }
  };
}

test('changing the mode swaps the race card button between Create race and Start time trial', function () {
  var options = modeOptions();
  var page = load();
  page.fire(modeSelect(options, 'time_trial'));
  assert.equal(page.label.textContent, 'Start time trial');
  page.fire(modeSelect(options, 'race'));
  assert.equal(page.label.textContent, 'Create race');
});

test('a change on another select leaves the button label alone', function () {
  var page = load();
  page.fire({ matches: function () { return false; } });
  assert.equal(page.label.textContent, 'Create race');
});
