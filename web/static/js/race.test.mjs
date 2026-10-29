import test from 'node:test';
import assert from 'node:assert/strict';
import { access } from 'node:fs/promises';

// race.mjs guards its top-level DOM access, so it imports under node with no
// document; each test then builds the page it needs and calls startRaceView.
import { startRaceView } from './race.mjs';

var REMOVED = 'You were removed from this race because your rowing machine was not connected in time.';
var raceResultsAvailable = await access(new URL('./race-results.mjs', import.meta.url)).then(function () { return true; }, function () { return false; });

function fakeElement(tag) {
  var target = new EventTarget();
  var classes = new Set();
  var el = {
    tagName: tag,
    textContent: '',
    dataset: {},
    attributes: {},
    children: [],
    colSpan: 1,
    classList: {
      add: function (c) { classes.add(c); },
      remove: function (c) { classes.delete(c); },
      contains: function (c) { return classes.has(c); }
    },
    addEventListener: target.addEventListener.bind(target),
    removeEventListener: target.removeEventListener.bind(target),
    dispatchEvent: target.dispatchEvent.bind(target),
    appendChild: function (child) { el.children.push(child); return child; },
    setAttribute: function (name, value) { el.attributes[name] = value; },
    querySelector: function () { return null; }
  };
  Object.defineProperty(el, 'textContent', {
    get: function () { return el._text || ''; },
    set: function (value) { el._text = value; if (value === '') el.children = []; }
  });
  return el;
}

// The FINISH! modal as race.html renders it: a table, a hidden error and Done.
function fakeModal() {
  var modal = fakeElement('dialog');
  modal.open = false;
  modal.showModal = function () { modal.open = true; };
  var table = fakeElement('table');
  var tbody = fakeElement('tbody');
  var error = fakeElement('p');
  error.classList.add('text-error');
  error.classList.add('hidden');
  var done = fakeElement('button');
  modal.querySelector = function (selector) {
    return { table: table, tbody: tbody, '.text-error': error, 'button.btn-primary': done }[selector] || null;
  };
  return { modal: modal, table: table, tbody: tbody, error: error, done: done };
}

function makePixi() {
  var release;
  var initGate = new Promise(function (resolve) { release = resolve; });
  var texts = [];
  var ticks = [];
  function chain() {
    var g = {};
    ['moveTo', 'lineTo', 'stroke', 'rect', 'fill', 'roundRect', 'clear'].forEach(function (m) { g[m] = function () { return g; }; });
    g.destroy = function () {};
    return g;
  }
  var PIXI = {
    Application: function () {
      this.init = function () { return initGate; };
      this.canvas = fakeElement('canvas');
      this.stage = { addChild: function () {}, removeChild: function () {} };
      this.ticker = { add: function (fn) { ticks.push(fn); } };
      this.screen = { width: 300, height: 600 };
    },
    Graphics: function () { return chain(); },
    Container: function () { return { addChild: function () {}, position: { set: function () {} }, destroy: function () {}, alpha: 1 }; },
    Text: function (options) {
      var t = { text: options.text, style: Object.assign({}, options.style), anchor: { set: function () {} }, position: { set: function () {} }, alpha: 1, y: 0 };
      texts.push(t);
      return t;
    }
  };
  return { PIXI: PIXI, releaseInit: release, texts: texts, ticks: ticks };
}

// Sets up the race page globals. responses maps a URL to a function returning
// a promise of a fetch response; calls records every fetched URL.
function setupPage(options) {
  var doc = new EventTarget();
  var track = fakeElement('div');
  Object.assign(track.dataset, {
    selfId: 'u-self', targetMm: '500000',
    statusUrl: '/rooms/r1/race/status', readyUrl: '/rooms/r1/race/ready', lobbyUrl: '/rooms/r1/lobby',
    resultsUrl: '/rooms/r1/race/results', dashboardUrl: '/dashboard',
    finished: options.finished ? 'true' : 'false', raceId: options.finished ? 'race-1' : ''
  });
  var status = fakeElement('p');
  var parts = fakeModal();
  doc.getElementById = function (id) {
    return { 'race-track': track, 'race-status': status, 'race-finish-modal': parts.modal }[id] || null;
  };
  doc.querySelectorAll = function () {
    return [{ dataset: { userId: 'u-self', self: 'true' }, textContent: 'Me' }, { dataset: { userId: 'u-2', self: 'false' }, textContent: 'Rival' }];
  };
  doc.createElement = fakeElement;
  var calls = [];
  var assigned = [];
  var streamStops = 0;
  globalThis.document = doc;
  globalThis.window = {
    devicePixelRatio: 1,
    location: { assign: function (url) { assigned.push(url); } },
    isPm5Connected: function () { return !!options.pm5; },
    stopRaceStream: function () { streamStops++; }
  };
  globalThis.fetch = function (url) {
    calls.push(url);
    var respond = options.responses[url];
    if (!respond) return new Promise(function () {}); // never settles: no further polling
    return respond();
  };
  return {
    doc: doc, track: track, status: status, parts: parts, calls: calls, assigned: assigned,
    streamStops: function () { return streamStops; }
  };
}

function jsonResponse(status, body) {
  return Promise.resolve({ status: status, ok: status >= 200 && status < 300, json: function () { return Promise.resolve(body); } });
}

function badJsonResponse() {
  return Promise.resolve({ status: 200, ok: true, json: function () { return Promise.reject(new SyntaxError('bad json')); } });
}

function settle() {
  return new Promise(function (resolve) { setTimeout(resolve, 0); });
}

var RESULTS = {
  race_id: 'race-1', finished: true, target_millimeters: 500000,
  results: [
    { user_id: 'u-2', name: 'Rival', rank: 1, status: 'finished', elapsed_ms: 95000, distance_m: 500, pace_ms_per_500m: 95000 },
    { user_id: 'u-self', name: 'Me', rank: null, status: 'dnf', elapsed_ms: null, distance_m: 300, pace_ms_per_500m: 120000 }
  ]
};

test('finished reload: a 404 before the canvas initialises shows the removal notice, no throw, modal closed', async function () {
  var page = setupPage({ finished: true, responses: { '/rooms/r1/race/status': function () { return jsonResponse(404, null); } } });
  var pixi = makePixi();
  var errors = [];
  var onError = function (err) { errors.push(err); };
  process.on('unhandledRejection', onError);
  try {
    var view = startRaceView(page.track, pixi.PIXI); // held at app.init
    await settle();
    assert.equal(page.status.textContent, REMOVED, 'the removal notice shows while the canvas is still loading');
    assert.equal(page.parts.modal.open, false, 'the FINISH! modal stays closed');
    assert.equal(pixi.texts.length, 0, 'the countdown text did not exist yet');

    pixi.releaseInit();
    await view; // must not reject
    page.doc.dispatchEvent(new CustomEvent('pixelpace:race-finished'));
    await settle();
    assert.equal(page.parts.modal.open, false, 'a removed racer never opens the modal');
    assert.equal(page.status.textContent, REMOVED);
    assert.deepEqual(page.calls, ['/rooms/r1/race/status'], 'no ready report, no results fetch, no polling');
    assert.deepEqual(errors, []);
  } finally {
    process.off('unhandledRejection', onError);
  }
});

test('finished reload: a dropped racer whose 404 lands after the canvas initialised keeps the notice and no modal', async function () {
  var answer;
  var statusGate = new Promise(function (resolve) { answer = resolve; });
  var page = setupPage({ finished: true, responses: { '/rooms/r1/race/status': function () { return statusGate; } } });
  var pixi = makePixi();
  var view = startRaceView(page.track, pixi.PIXI);
  pixi.releaseInit();
  await view;
  // A finished frame while the status check is in flight must wait for it.
  page.doc.dispatchEvent(new CustomEvent('pixelpace:race-finished'));
  assert.equal(page.parts.modal.open, false, 'no modal before the dropped() check has answered');
  answer(await jsonResponse(404, null));
  await settle();
  assert.equal(page.status.textContent, REMOVED);
  assert.equal(page.parts.modal.open, false);
  var countdown = pixi.texts.find(function (t) { return t.style.fontSize === 120; });
  assert.equal(countdown.text, '', 'the countdown text is cleared once it exists');
  assert.ok(!page.calls.includes('/rooms/r1/race/results'));
});

test('finished reload: a racer still in the race gets the FINISH! modal with results', async function () {
  var page = setupPage({
    finished: true,
    responses: {
      '/rooms/r1/race/status': function () { return jsonResponse(200, { race_id: 'race-1', server_time: new Date().toISOString(), racers: [] }); },
      '/rooms/r1/race/results': function () { return jsonResponse(200, RESULTS); }
    }
  });
  var pixi = makePixi();
  var view = startRaceView(page.track, pixi.PIXI);
  await settle();
  assert.equal(page.parts.modal.open, true, 'the modal opens once the check passes');
  assert.notEqual(page.status.textContent, REMOVED);
  assert.deepEqual(page.calls, ['/rooms/r1/race/status', '/rooms/r1/race/results']);
  pixi.releaseInit();
  await view;
  await settle();
  assert.equal(page.calls.filter(function (u) { return u === '/rooms/r1/race/status'; }).length, 1, 'no polling after the modal opens');
  assert.ok(!page.calls.includes('/rooms/r1/race/ready'), 'a finished race is not joined');
});

test('the modal renders the result rows from race-results.mjs', { skip: raceResultsAvailable ? false : 'race-results.mjs (unit U8) is not in this tree' }, async function () {
  var page = setupPage({ finished: false, responses: { '/rooms/r1/race/results': function () { return jsonResponse(200, RESULTS); } } });
  var pixi = makePixi();
  pixi.releaseInit();
  await startRaceView(page.track, pixi.PIXI);
  page.doc.dispatchEvent(new CustomEvent('pixelpace:race-finished'));
  await settle();
  await settle();
  var rows = page.parts.tbody.children.filter(function (tr) { return tr.children.length === 5; });
  assert.equal(rows.length, 2);
  assert.equal(rows[0].children[1].textContent, 'Rival');
  assert.equal(rows[1].children[0].textContent, 'DNF');
  assert.ok(rows[1].classList.contains('font-bold'), 'the viewer\u2019s own row is flagged');
  assert.equal(page.parts.table.classList.contains('hidden'), false);
  assert.equal(page.parts.error.classList.contains('hidden'), true);
});

for (var failure of [
  ['a network error', function () { return Promise.reject(new Error('offline')); }],
  ['a non-ok response', function () { return jsonResponse(500, null); }],
  ['bad JSON', badJsonResponse],
  ['a null body', function () { return jsonResponse(200, null); }]
]) {
  (function (name, respond) {
    test('race-finished with ' + name + ' shows the error in place of the table, and Done still leaves', async function () {
      var page = setupPage({ finished: false, responses: { '/rooms/r1/race/results': respond } });
      var pixi = makePixi();
      pixi.releaseInit();
      await startRaceView(page.track, pixi.PIXI);
      page.doc.dispatchEvent(new CustomEvent('pixelpace:race-finished'));
      page.doc.dispatchEvent(new CustomEvent('pixelpace:race-finished'));
      await settle();
      await settle();
      assert.equal(page.parts.modal.open, true);
      assert.equal(page.calls.filter(function (u) { return u === '/rooms/r1/race/results'; }).length, 1, 'the modal opens once');
      assert.equal(page.parts.table.classList.contains('hidden'), true, 'the table is hidden');
      assert.equal(page.parts.error.classList.contains('hidden'), false, 'the error shows');
      assert.ok(page.parts.error.textContent.length > 0);
      page.parts.done.dispatchEvent(new Event('click'));
      assert.equal(page.streamStops(), 1, 'Done closes the race connection');
      assert.deepEqual(page.assigned, ['/dashboard']);
    });
  })(failure[0], failure[1]);
}

test('a racer removed before the start (ready 404) never opens the modal', async function () {
  var page = setupPage({
    finished: false, pm5: true,
    responses: { '/rooms/r1/race/ready': function () { return jsonResponse(404, null); } }
  });
  var pixi = makePixi();
  pixi.releaseInit();
  await startRaceView(page.track, pixi.PIXI);
  await settle();
  assert.equal(page.status.textContent, REMOVED);
  page.doc.dispatchEvent(new CustomEvent('pixelpace:race-finished'));
  await settle();
  assert.equal(page.parts.modal.open, false);
  assert.equal(page.status.textContent, REMOVED, 'keeps the removal notice');
});

test('finishing your own row says you are waiting for the others', async function () {
  var served = false;
  var page = setupPage({
    finished: false,
    responses: {
      '/rooms/r1/race/status': function () {
        if (served) return new Promise(function () {});
        served = true;
        return jsonResponse(200, { race_id: 'race-1', race_starts_at: new Date(Date.now() - 1000).toISOString(), server_time: new Date().toISOString(), racers: [] });
      }
    }
  });
  var pixi = makePixi();
  pixi.releaseInit();
  await startRaceView(page.track, pixi.PIXI);
  await settle();
  pixi.ticks[0]({ deltaTime: 1 }); // the countdown has passed zero: GO!
  page.doc.dispatchEvent(new CustomEvent('pixelpace:local-telemetry', { detail: { distance_millimeters: 499999 } }));
  assert.equal(page.status.textContent, 'GO! Row toward the finish line.');
  page.doc.dispatchEvent(new CustomEvent('pixelpace:local-telemetry', { detail: { distance_millimeters: 500000 } }));
  assert.match(page.status.textContent, /waiting for the other rowers/i);
  assert.equal(page.parts.modal.open, false, 'the modal waits for the race to end');
});
