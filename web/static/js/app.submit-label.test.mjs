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
  var select = dashboardHtml.match(/<select name="activity_type"[^>]*>([\s\S]*?)<\/select>/) || dashboardHtml.match(/<select name="mode"[^>]*>([\s\S]*?)<\/select>/);
  assert.ok(select, 'dashboard.html has the activity_type select');
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

function modeSelect(options, value, form) {
  return {
    name: 'activity_type',
    value: value,
    form: form,
    closest: function (sel) { return sel === 'form' ? form : null; },
    options: options,
    selectedIndex: options.findIndex(function (o) { return o.value === value; }),
    matches: function (sel) { return sel === 'select[data-submit-label-target]' || sel === 'select[name="activity_type"]' || sel === 'select[name="mode"]'; },
    getAttribute: function (name) { return name === 'data-submit-label-target' ? '#create-room-submit-label' : null; }
  };
}

test('changing the mode swaps the race card button between Create race, Start time trial and Start warmup', function () {
  var options = modeOptions();
  var page = load();
  page.fire(modeSelect(options, 'time_trial'));
  assert.equal(page.label.textContent, 'Start time trial');
  page.fire(modeSelect(options, 'warmup'));
  assert.equal(page.label.textContent, 'Start warmup');
  page.fire(modeSelect(options, 'race'));
  assert.equal(page.label.textContent, 'Create race');
});

test('changing mode to warmup hides distance select and enables minutes input', function () {
  function makeElem(initialClasses, disabled) {
    var classes = new Set(initialClasses || []);
    return {
      disabled: !!disabled,
      classList: {
        add: function (c) { classes.add(c); },
        remove: function (c) { classes.delete(c); },
        contains: function (c) { return classes.has(c); }
      }
    };
  }
  var distanceField = makeElem([], false);
  var warmupField = makeElem(['hidden'], false);
  var distanceSelect = makeElem([], false);
  var minutesInput = makeElem([], true);

  var fakeForm = {
    querySelector: function (sel) {
      if (sel === '#preset-field' || sel === '#race-type-field' || sel === '[data-activity-type-field="distance"]' || sel === '[data-mode-field="distance"]') return distanceField;
      if (sel === '#warmup-minutes-field' || sel === '[data-activity-type-field="warmup"]' || sel === '[data-mode-field="warmup"]') return warmupField;
      if (sel === 'select[name="race_type_id"]') return distanceSelect;
      if (sel === 'input[name="minutes"]') return minutesInput;
      return null;
    }
  };

  var options = modeOptions();
  var page = load();

  // Switch to warmup
  page.fire(modeSelect(options, 'warmup', fakeForm));
  assert.ok(distanceField.classList.contains('hidden'), 'distance field hidden');
  assert.equal(distanceSelect.disabled, true, 'distance select disabled');
  assert.ok(!warmupField.classList.contains('hidden'), 'warmup field visible');
  assert.equal(minutesInput.disabled, false, 'minutes input enabled');

  // Switch back to race
  page.fire(modeSelect(options, 'race', fakeForm));
  assert.ok(!distanceField.classList.contains('hidden'), 'distance field visible');
  assert.equal(distanceSelect.disabled, false, 'distance select enabled');
  assert.ok(warmupField.classList.contains('hidden'), 'warmup field hidden');
  assert.equal(minutesInput.disabled, true, 'minutes input disabled');

  // Switch back to time trial
  page.fire(modeSelect(options, 'time_trial', fakeForm));
  assert.ok(!distanceField.classList.contains('hidden'), 'distance field visible on time trial');
  assert.equal(distanceSelect.disabled, false, 'distance select enabled on time trial');
  assert.ok(warmupField.classList.contains('hidden'), 'warmup field hidden on time trial');
  assert.equal(minutesInput.disabled, true, 'minutes input disabled on time trial');
});

test('a change on another select leaves the button label alone', function () {
  var page = load();
  page.fire({ matches: function () { return false; } });
  assert.equal(page.label.textContent, 'Create race');
});
