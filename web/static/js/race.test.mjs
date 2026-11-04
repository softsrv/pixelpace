import test from 'node:test';
import assert from 'node:assert/strict';
import { access } from 'node:fs/promises';

// race.mjs guards its top-level DOM access, so it imports under node with no
// document; each test then builds the page it needs and calls startRaceView.
import { startRaceView } from './race.mjs';
import { COLORS, PACER_HULL, hullColor } from './race-pixels.mjs';

var REMOVED = 'You were removed from this race because your rowing machine was not connected in time.';
var raceResultsAvailable = await access(new URL('./race-results.mjs', import.meta.url)).then(function () { return true; }, function () { return false; });

function fakeElement(tag) {
  var target = new EventTarget();
  var classes = new Set();
  var el = {
    tagName: tag,
    hidden: false,
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
    appendChild: function (child) { el.children.push(child); child.parent = el; return child; },
    remove: function () {
      if (el.parent) el.parent.children = el.parent.children.filter(function (c) { return c !== el; });
      el.parent = null;
    },
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
    return { table: table, tbody: tbody, '.text-error': error, 'button[data-finish-done]': done }[selector] || null;
  };
  return { modal: modal, table: table, tbody: tbody, error: error, done: done };
}

function makePixi() {
  var release;
  var initGate = new Promise(function (resolve) { release = resolve; });
  var texts = [];
  var ticks = [];
  var graphics = [];
  var stageChildren = [];
  function chain() {
    var g = {};
    g.fills = [];
    ['moveTo', 'lineTo', 'stroke', 'rect', 'roundRect', 'clear'].forEach(function (m) { g[m] = function () { return g; }; });
    g.fill = function (color) { g.fills.push(color); return g; };
    g.destroy = function () {};
    return g;
  }
  var PIXI = {
    Application: function () {
      this.init = function () { return initGate; };
      this.canvas = fakeElement('canvas');
      this.stage = {
        addChild: function () { stageChildren.push.apply(stageChildren, arguments); },
        removeChild: function (child) { var i = stageChildren.indexOf(child); if (i >= 0) stageChildren.splice(i, 1); }
      };
      this.ticker = { add: function (fn) { ticks.push(fn); } };
      this.screen = { width: 300, height: 600 };
    },
    Graphics: function () { var g = chain(); graphics.push(g); return g; },
    Container: function () {
      var c = { children: [], destroy: function () {}, alpha: 1, x: null, y: null };
      c.position = { set: function (x, y) { c.x = x; c.y = y; } };
      c.addChild = function () { c.children.push.apply(c.children, arguments); };
      return c;
    },
    Text: function (options) {
      var t = { text: options.text, style: Object.assign({}, options.style), anchor: { set: function () {} }, position: { set: function () {} }, alpha: 1, y: 0 };
      texts.push(t);
      return t;
    }
  };
  // The countdown is drawn in the pixel font: find its label on the stage.
  function countdownText() {
    var view = stageChildren.find(function (c) { return c && c.pixelLabel; });
    return view ? view.pixelLabel.text : undefined;
  }
  // The rower on the stage whose name label reads `label`.
  function rower(label) {
    return stageChildren.find(function (c) {
      return c && Array.isArray(c.children) && c.children.some(function (child) { return child && child.text === label; });
    });
  }
  return { PIXI: PIXI, releaseInit: release, texts: texts, ticks: ticks, graphics: graphics, countdownText: countdownText, rower: rower };
}

// Sets up the race page globals. responses maps a URL to a function returning
// a promise of a fetch response; calls records every fetched URL.
function setupPage(options) {
  var doc = new EventTarget();
  var track = fakeElement('div');
  Object.assign(track.dataset, {
    selfId: 'u-self', targetMm: options.targetMm || '500000',
    statusUrl: '/rooms/r1/race/status', readyUrl: '/rooms/r1/race/ready', lobbyUrl: '/rooms/r1/lobby',
    resultsUrl: '/rooms/r1/race/results', dashboardUrl: '/dashboard',
    finished: options.finished ? 'true' : 'false', raceId: options.finished ? 'race-1' : '',
    finishPollMs: options.finishPollMs || '1'
  });
  var status = fakeElement('p');
  var parts = fakeModal();
  var laneList = fakeElement('ol');
  var pacerNote = options.pacerNote === true ? fakeElement('p') : (options.pacerNote || null);
  if (pacerNote && !pacerNote.textContent) pacerNote.textContent = 'No personal best yet: set your first time.';
  (options.lanes || [
    { dataset: { userId: 'u-self', self: 'true' }, textContent: 'Me' },
    { dataset: { userId: 'u-2', self: 'false' }, textContent: 'Rival' }
  ]).forEach(function (item) { laneList.appendChild(item); });
  doc.getElementById = function (id) {
    return { 'race-track': track, 'race-status': status, 'race-finish-modal': parts.modal, 'race-lanes': laneList, 'race-pacer-note': pacerNote }[id] || null;
  };
  doc.querySelectorAll = function () { return laneList.children.slice(); };
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
    if (!respond) {
      if (url.endsWith('/race/results')) return jsonResponse(200, RESULTS);
      return new Promise(function () {}); // never settles: no further polling
    }
    return respond();
  };
  return {
    doc: doc, track: track, status: status, parts: parts, calls: calls, assigned: assigned, laneList: laneList, pacerNote: pacerNote,
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
  assert.equal(pixi.countdownText(), '', 'the countdown text is cleared once it exists');
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
  page.doc.dispatchEvent(new CustomEvent('pixelpace:race-finished'));
});

// Starts a race whose countdown has already passed, so the view is racing and
// streaming. after(n) answers the n-th status call made once racing began.
async function startRacing(after, extra) {
  var statusCalls = 0;
  var page = setupPage({
    finished: false,
    responses: Object.assign({
      '/rooms/r1/race/status': function () {
        statusCalls++;
        if (statusCalls === 1) {
          return jsonResponse(200, { race_id: 'race-1', race_starts_at: new Date(Date.now() - 1000).toISOString(), server_time: new Date().toISOString(), racers: [] });
        }
        return after(statusCalls - 1);
      }
    }, extra || {})
  });
  page.track.dataset.finishPollMs = '1';
  globalThis.window.startRaceStream = function () {};
  var pixi = makePixi();
  pixi.releaseInit();
  await startRaceView(page.track, pixi.PIXI);
  await settle();
  pixi.ticks[0]({ deltaTime: 1 }); // GO!: streaming starts, and so does the finish check
  return page;
}

function statusCount(page) {
  return page.calls.filter(function (u) { return u === '/rooms/r1/race/status'; }).length;
}

async function waitFor(check) {
  for (var i = 0; i < 50 && !check(); i++) await new Promise(function (resolve) { setTimeout(resolve, 2); });
}

test('while racing, a status check reporting finished opens the modal once, even with no finished frame', async function () {
  var page = await startRacing(function (n) {
    if (n === 1) return Promise.reject(new Error('offline')); // ignored; the check goes on
    return jsonResponse(200, { finished: n >= 3, racers: [] });
  }, { '/rooms/r1/race/results': function () { return jsonResponse(200, RESULTS); } });
  await waitFor(function () { return page.parts.modal.open; });
  assert.equal(page.parts.modal.open, true);
  var checks = statusCount(page);
  await new Promise(function (resolve) { setTimeout(resolve, 20); });
  assert.equal(statusCount(page), checks, 'the check stops once the modal opens');
  assert.equal(page.calls.filter(function (u) { return u === '/rooms/r1/race/results'; }).length, 1, 'results load once');
});

test('a finished frame and then a finished status check open the modal only once', async function () {
  var page = await startRacing(function () { return jsonResponse(200, { finished: true, racers: [] }); },
    { '/rooms/r1/race/results': function () { return jsonResponse(200, RESULTS); } });
  page.doc.dispatchEvent(new CustomEvent('pixelpace:race-finished'));
  await new Promise(function (resolve) { setTimeout(resolve, 20); });
  assert.equal(page.parts.modal.open, true);
  assert.equal(page.calls.filter(function (u) { return u === '/rooms/r1/race/results'; }).length, 1, 'results load once');
  assert.equal(statusCount(page), 1, 'no finish check runs after the frame opened the modal');
});

test('while racing, a 404 status check shows the removal notice and stops checking', async function () {
  var page = await startRacing(function () { return jsonResponse(404, null); });
  await waitFor(function () { return page.status.textContent === REMOVED; });
  assert.equal(page.status.textContent, REMOVED);
  var checks = statusCount(page);
  await new Promise(function (resolve) { setTimeout(resolve, 20); });
  assert.equal(statusCount(page), checks, 'no more checks after removal');
  assert.equal(page.parts.modal.open, false);
});

// ── Pacer ────────────────────────────────────────────────────────────────────

function laneItem(userId, label, isSelf) {
  return { dataset: { userId: userId, self: isSelf ? 'true' : 'false' }, textContent: label };
}

// The server renders the pace label as the li's text; the view recomputes it
// from data-pacer-ms and data-target-mm, so a placeholder here proves it does.
function pacerItem(pacerMs) {
  return { dataset: { userId: 'pacer', pacer: 'true', pacerMs: String(pacerMs) }, textContent: 'server label' };
}

// Answers each status call with the next body in turn; once they run out the
// call never settles, so polling stops.
function statusQueue(bodies) {
  return function () {
    var body = bodies.shift();
    return body ? jsonResponse(200, body) : new Promise(function () {});
  };
}

async function until(check) {
  for (var i = 0; i < 300 && !check(); i++) await new Promise(function (resolve) { setTimeout(resolve, 10); });
  assert.ok(check(), 'timed out waiting');
}

// Every fill a rower's art used, in order: the pose sprites first, whose first
// cell is the bow, painted in the hull colour.
function hullsOf(rower) {
  var colors = [];
  rower.children.forEach(function (child) { if (child && child.fills) colors.push.apply(colors, child.fills); });
  return colors;
}

function hullOf(rower) {
  return rower.children[0].fills[0];
}

function pacerLanes(page) {
  return page.laneList.children.filter(function (el) { return el.dataset.pacer === 'true'; });
}

// The course runs from the start line (600 - 112) up to the finish line (40),
// and positions are snapped to the art grid (at most 4px).
var START_Y = 488;
var FINISH_Y = 40;
function assertNear(actual, expected, message) {
  assert.ok(Math.abs(actual - expected) <= 4, message + ': y = ' + actual + ', want about ' + expected);
}

test('the pacer boat is labelled with its pace per 500m in the pacer hull, takes no rival colour, and ignores race-telemetry', async function () {
  async function draw(lanes) {
    var page = setupPage({
      finished: false, lanes: lanes,
      responses: { '/rooms/r1/race/status': statusQueue([{ server_time: new Date().toISOString(), race_starts_at: new Date(Date.now() + 60000).toISOString(), racers: [{ user_id: 'u-self', equipment_ready: true }, { user_id: 'u-2', equipment_ready: true }] }]) }
    });
    var pixi = makePixi();
    pixi.releaseInit();
    await startRaceView(page.track, pixi.PIXI);
    await settle();
    return { page: page, pixi: pixi };
  }
  var without = await draw([laneItem('u-self', 'Me', true), laneItem('u-2', 'Rival', false)]);
  var rivalHullAlone = hullsOf(without.pixi.rower('Rival'));
  assert.equal(hullOf(without.pixi.rower('Rival')), hullColor(false, 0));
  var withPacer = await draw([laneItem('u-self', 'Me', true), laneItem('u-2', 'Rival', false), pacerItem(100000)]);

  // 100000ms over the default 500000mm target: 1:40.0 per 500m.
  var pacer = withPacer.pixi.rower('1:40.0');
  assert.ok(pacer, 'a boat is drawn for the pacer lane, labelled with its pace 1:40.0');
  assert.equal(withPacer.pixi.rower('Your best'), undefined, 'no boat is labelled Your best');
  assert.equal(hullOf(pacer), PACER_HULL, 'the pacer hull is PACER_HULL');
  assert.equal(pacer.children.find(function (c) { return c.text === '1:40.0'; }).style.fill, PACER_HULL, 'its label is in the pacer colour');
  assert.equal(pacerLanes(withPacer.page)[0].textContent, '1:40.0', 'the lane list names the pacer as its boat does');
  var rivalHull = hullsOf(withPacer.pixi.rower('Rival'));
  assert.equal(hullOf(withPacer.pixi.rower('Rival')), hullColor(false, 0), 'the rival keeps rival colour 0');
  assert.deepEqual(rivalHull, rivalHullAlone, 'the rival is drawn exactly as it is without a pacer');
  // Even listed ahead of a rival, the pacer takes no rival slot.
  var pacerFirst = await draw([laneItem('u-self', 'Me', true), pacerItem(100000), laneItem('u-2', 'Rival', false)]);
  assert.equal(hullOf(pacerFirst.pixi.rower('Rival')), hullColor(false, 0), 'a rival after the pacer still gets rival colour 0');

  var tick = withPacer.pixi.ticks[0];
  tick({ deltaTime: 1 });
  var before = pacer.y;
  withPacer.page.doc.dispatchEvent(new CustomEvent('pixelpace:race-telemetry', { detail: { user_id: 'pacer', distance_millimeters: 400000, stroke_rate: 30 } }));
  withPacer.page.doc.dispatchEvent(new CustomEvent('pixelpace:race-telemetry', { detail: { user_id: 'u-2', distance_millimeters: 400000, stroke_rate: 30 } }));
  for (var i = 0; i < 60; i++) tick({ deltaTime: 1 });
  assert.equal(pacer.y, before, 'race-telemetry for user_id pacer does not move the pacer');
  assert.ok(withPacer.pixi.rower('Rival').y < before - 100, 'a rival still follows its race-telemetry');
  without.page.doc.dispatchEvent(new CustomEvent('pixelpace:race-finished'));
  withPacer.page.doc.dispatchEvent(new CustomEvent('pixelpace:race-finished'));
  pacerFirst.page.doc.dispatchEvent(new CustomEvent('pixelpace:race-finished'));
});

test("the player's own boat is labelled 'you' in its hull colour, while rivals and pacer keep their names", async function () {
  async function draw(lanes) {
    var racers = lanes.filter(function (l) { return l.dataset.pacer !== 'true'; }).map(function (l) {
      return { user_id: l.dataset.userId, equipment_ready: true };
    });
    var page = setupPage({
      finished: false, lanes: lanes,
      responses: { '/rooms/r1/race/status': statusQueue([{ server_time: new Date().toISOString(), race_starts_at: new Date(Date.now() + 60000).toISOString(), racers: racers }]) }
    });
    var pixi = makePixi();
    pixi.releaseInit();
    await startRaceView(page.track, pixi.PIXI);
    await settle();
    return { page: page, pixi: pixi };
  }

  // Render lanes Me (self), Rival, a second rival also named 'Me', and the pacer.
  var lanes = [
    laneItem('u-self', 'Me', true),
    laneItem('u-2', 'Rival', false),
    laneItem('u-3', 'Me', false),
    pacerItem(100000)
  ];
  var mainView = await draw(lanes);

  // CLM-1: Exactly one PIXI.Text label has text === 'you', fill equals self hull, fontWeight 'bold'
  var youLabels = mainView.pixi.texts.filter(function (t) { return t.text === 'you'; });
  assert.equal(youLabels.length, 1, "exactly one label reads 'you'");
  var selfRower = mainView.pixi.rower('you');
  assert.ok(selfRower, "player boat exists with label 'you'");
  assert.equal(hullOf(selfRower), hullColor(true, 0), "self hull colour matches hullColor(true, 0)");
  var selfLabel = selfRower.children.find(function (c) { return c && c.text === 'you'; });
  assert.ok(selfLabel, "self boat child label found");
  assert.equal(selfLabel.style.fill, hullColor(true, 0), "label fill equals self lane hull colour");
  assert.equal(selfLabel.style.fontWeight, 'bold', "label fontWeight is bold");
  assert.equal(selfLabel.style.fontSize, 12, "label fontSize is 12");
  assert.deepEqual(selfLabel.style.stroke, { color: COLORS.ink, width: 3 }, "label stroke is ink width 3");
  assert.equal(mainView.pixi.texts.some(function (t) { return t.text === 'Me (you)'; }), false, "no label reads 'Me (you)'");
  assert.equal(mainView.pixi.texts.some(function (t) { return typeof t.text === 'string' && t.text.includes('(you)'); }), false, "no label contains '(you)'");

  // CLM-2: Rival and pacer labels
  var rivalRower = mainView.pixi.rower('Rival');
  assert.ok(rivalRower, "rival boat exists with label 'Rival'");
  var rivalLabel = rivalRower.children.find(function (c) { return c && c.text === 'Rival'; });
  assert.ok(rivalLabel, "rival boat child label found");
  assert.equal(rivalLabel.text, 'Rival');
  assert.equal(rivalLabel.style.fill, COLORS.white, "rival label fill is COLORS.white");

  // The pacer is labelled with its pace: 100000ms over the default 500000mm target is 1:40.0 per 500m.
  var pacerRower = mainView.pixi.rower('1:40.0');
  assert.ok(pacerRower, "pacer boat exists with label '1:40.0'");
  var pacerLabel = pacerRower.children.find(function (c) { return c && c.text === '1:40.0'; });
  assert.ok(pacerLabel, "pacer boat child label found");
  assert.equal(pacerLabel.text, '1:40.0');
  assert.equal(pacerLabel.style.fill, PACER_HULL, "pacer label fill is PACER_HULL");

  // CLM-3: Same-named rival boat carries different label
  var sameNameRower = mainView.pixi.rower('Me');
  assert.ok(sameNameRower, "same-named rival boat exists with label 'Me'");
  var sameNameLabel = sameNameRower.children.find(function (c) { return c && c.text === 'Me'; });
  assert.ok(sameNameLabel, "same-name rival child label found");
  assert.equal(sameNameLabel.text, 'Me');
  assert.equal(sameNameLabel.style.fill, COLORS.white, "same-name rival label fill is COLORS.white");
  var meLabels = mainView.pixi.texts.filter(function (t) { return t.text === 'Me'; });
  assert.equal(meLabels.length, 1, "exactly one label has text 'Me'");

  // CLM-5: Screen-reader list (#race-lanes) still gives the player's real display name
  var selfLaneEl = mainView.page.laneList.children.find(function (el) { return el.dataset.self === 'true'; });
  assert.ok(selfLaneEl, "self lane element exists in #race-lanes");
  assert.equal(selfLaneEl.textContent, 'Me', "#race-lanes still lists the self display name 'Me'");
  mainView.page.doc.dispatchEvent(new CustomEvent('pixelpace:race-finished'));

  // CLM-4: (a) Blank/whitespace self name; (b) lanes with no self
  var emptyView = await draw([laneItem('u-self', '', true), laneItem('u-2', 'Rival', false)]);
  assert.ok(emptyView.pixi.rower('you'), "empty self name draws boat labelled 'you'");
  assert.equal(emptyView.pixi.texts.filter(function (t) { return t.text === 'you'; }).length, 1);
  assert.equal(emptyView.pixi.texts.some(function (t) { return typeof t.text === 'string' && t.text.includes('(you)'); }), false);
  emptyView.page.doc.dispatchEvent(new CustomEvent('pixelpace:race-finished'));

  var whitespaceView = await draw([laneItem('u-self', '   ', true), laneItem('u-2', 'Rival', false)]);
  assert.ok(whitespaceView.pixi.rower('you'), "whitespace self name draws boat labelled 'you'");
  assert.equal(whitespaceView.pixi.texts.filter(function (t) { return t.text === 'you'; }).length, 1);
  assert.equal(whitespaceView.pixi.texts.some(function (t) { return typeof t.text === 'string' && t.text.includes('(you)'); }), false);
  whitespaceView.page.doc.dispatchEvent(new CustomEvent('pixelpace:race-finished'));

  var noSelfView = await draw([laneItem('u-1', 'Rival 1', false), laneItem('u-2', 'Rival 2', false), pacerItem(100000)]);
  assert.equal(noSelfView.pixi.texts.some(function (t) { return t.text === 'you'; }), false, "no label reads 'you' when no self lane");
  assert.ok(noSelfView.pixi.rower('Rival 1'), "rival 1 boat labelled with its display name");
  assert.ok(noSelfView.pixi.rower('Rival 2'), "rival 2 boat labelled with its display name");
  assert.ok(noSelfView.pixi.rower('1:40.0'), "pacer boat labelled with its pace '1:40.0'");
  noSelfView.page.doc.dispatchEvent(new CustomEvent('pixelpace:race-finished'));
});

test('the pacer rows from the start on the server clock: still before GO, mid-course on a mid-race load, at the finish at its time', async function () {
  var realNow = Date.now;
  var startsAt = Date.parse('2026-10-26T12:00:10Z');
  var localNow = realNow(); // the local clock is wildly off the server's
  Date.now = function () { return localNow; };
  try {
    // Countdown: 5s before the start on the server clock.
    var page = setupPage({
      finished: false, targetMm: '1000000',
      lanes: [laneItem('u-self', 'Me', true), pacerItem(100000)],
      responses: { '/rooms/r1/race/status': statusQueue([{ server_time: new Date(startsAt - 5000).toISOString(), race_starts_at: new Date(startsAt).toISOString(), racers: [{ user_id: 'u-self', equipment_ready: true }] }]) }
    });
    var pixi = makePixi();
    pixi.releaseInit();
    await startRaceView(page.track, pixi.PIXI);
    await settle();
    var pacer = pixi.rower('0:50.0'); // 100000ms over 1000m
    assert.ok(pacer, 'the pacer boat over 1000m is labelled 0:50.0');
    assert.equal(pacerLanes(page)[0].textContent, '0:50.0');
    pixi.ticks[0]({ deltaTime: 1 });
    localNow += 4000;
    pixi.ticks[0]({ deltaTime: 1 });
    assertNear(pacer.y, START_Y, 'before GO the pacer waits on the start line');
    assert.equal(pacer.y, pixi.rower('you').y, 'level with our own boat at the start');
    page.doc.dispatchEvent(new CustomEvent('pixelpace:race-finished'));

    // A reload 50s into a 100s personal best over 1000m.
    localNow = realNow();
    page = setupPage({
      finished: false, targetMm: '1000000',
      lanes: [laneItem('u-self', 'Me', true), pacerItem(100000)],
      responses: { '/rooms/r1/race/status': statusQueue([{ server_time: new Date(startsAt + 50000).toISOString(), race_starts_at: new Date(startsAt).toISOString(), race_id: 'race-1', racers: [{ user_id: 'u-self', equipment_ready: true }] }]) }
    });
    pixi = makePixi();
    pixi.releaseInit();
    await startRaceView(page.track, pixi.PIXI);
    await settle();
    pacer = pixi.rower('0:50.0');
    pixi.ticks[0]({ deltaTime: 1 });
    assertNear(pacer.y, START_Y - 0.5 * (START_Y - FINISH_Y), 'a mid-race load shows the pacer at 500m, not the start line');

    localNow += 50000; // server clock now at startsAt + 100000
    pixi.ticks[0]({ deltaTime: 1 });
    assertNear(pacer.y, FINISH_Y, 'at its personal best time the pacer reaches the target');
    localNow += 30000;
    pixi.ticks[0]({ deltaTime: 1 });
    assertNear(pacer.y, FINISH_Y, 'and stays at the finish line');
    // End this racing view so its finish check stops polling later tests' fetch.
    page.doc.dispatchEvent(new CustomEvent('pixelpace:race-finished'));
  } finally {
    Date.now = realNow;
  }
});

test('a status with a pacer adds its lane, racer syncing keeps it while dropping absent racers, and pacer null removes it', async function () {
  var ahead = new Date(Date.now() + 60000).toISOString();
  var self = [{ user_id: 'u-self', equipment_ready: true }];
  // The test moves the server through these states; every poll reads the
  // current one, so a stray fetch from an earlier test's view changes nothing.
  var phases = [
    { server_time: new Date().toISOString(), racers: self, pacer: { elapsed_milliseconds: 95000 } },
    { server_time: new Date().toISOString(), race_starts_at: ahead, racers: self },
    { server_time: new Date().toISOString(), race_starts_at: ahead, racers: self, pacer: null }
  ];
  var phase = 0;
  var served = [0, 0, 0];
  var page = setupPage({
    finished: false,
    responses: { '/rooms/r1/race/status': function () { served[phase]++; return jsonResponse(200, phases[phase]); } }
  });
  var pixi = makePixi();
  pixi.releaseInit();
  await startRaceView(page.track, pixi.PIXI);
  assert.equal(pacerLanes(page).length, 0, 'no pacer lane to begin with');

  await until(function () { return pacerLanes(page).length === 1; });
  assert.equal(pacerLanes(page)[0].dataset.pacerMs, '95000', 'the added lane carries the pacer time');
  // 95000ms over the default 500000mm target: 1:35.0 per 500m.
  assert.equal(pacerLanes(page)[0].textContent, '1:35.0');
  var pacer = pixi.rower('1:35.0');
  assert.ok(pacer && hullOf(pacer) === PACER_HULL, 'and a pacer boat labelled 1:35.0 is drawn in the pacer hull');
  assert.equal(pixi.rower('Rival'), undefined, 'the racer absent from status.racers is still removed');
  pixi.ticks[0]({ deltaTime: 1 });
  assert.notEqual(pacer.y, null, 'the ticker places the added pacer');

  phase = 1;
  await until(function () { return served[1] >= 1; });
  await settle();
  assert.equal(pacerLanes(page).length, 1, 'syncing against racers that omit the pacer keeps its lane');
  assert.equal(pixi.rower('1:35.0'), pacer, 'and its boat');

  phase = 2;
  await until(function () { return pacerLanes(page).length === 0; });
  assert.equal(pixi.rower('1:35.0'), undefined, 'pacer null removes the boat');
  assert.ok(pixi.rower('you'), 'our own boat stays');
  pixi.ticks[0]({ deltaTime: 1 }); // the ticker runs on without the removed lane
  page.doc.dispatchEvent(new CustomEvent('pixelpace:race-finished')); // stop this view's polling
});

test('with a pacer lane, the results modal lists only the racers, never the pacer or its pace label', async function () {
  var page = setupPage({
    finished: false,
    lanes: [laneItem('u-self', 'Me', true), laneItem('u-2', 'Rival', false), pacerItem(100000)],
    responses: { '/rooms/r1/race/results': function () { return jsonResponse(200, RESULTS); } }
  });
  var pixi = makePixi();
  pixi.releaseInit();
  await startRaceView(page.track, pixi.PIXI);
  page.doc.dispatchEvent(new CustomEvent('pixelpace:race-finished'));
  await until(function () { return page.parts.tbody.children.length > 0; });
  var names = page.parts.tbody.children.map(function (tr) { return tr.children[1] && tr.children[1].textContent; });
  assert.deepEqual(names, ['Rival', 'Me']);
  assert.ok(pixi.rower('1:40.0'), 'the pacer boat was on the track, labelled 1:40.0');
  assert.ok(!names.includes('1:40.0'), 'the pacer pace label is not in the results');
  assert.ok(!names.includes('Your best'), 'the pacer is not in the results');
});

test('a status changing the pacer time relabels its boat and lane with the new pace', async function () {
  var self = [{ user_id: 'u-self', equipment_ready: true }];
  var phases = [
    { server_time: new Date().toISOString(), racers: self, pacer: { elapsed_milliseconds: 95000 } },
    { server_time: new Date().toISOString(), racers: self, pacer: { elapsed_milliseconds: 120000 } }
  ];
  var phase = 0;
  var page = setupPage({
    finished: false,
    lanes: [laneItem('u-self', 'Me', true)],
    responses: { '/rooms/r1/race/status': function () { return jsonResponse(200, phases[phase]); } }
  });
  var pixi = makePixi();
  pixi.releaseInit();
  await startRaceView(page.track, pixi.PIXI);
  await until(function () { return pacerLanes(page).length === 1; });
  var pacer = pixi.rower('1:35.0');
  assert.ok(pacer, 'the first status labels the pacer 1:35.0');

  phase = 1; // 120000ms over 500m: 2:00.0 per 500m
  await until(function () { return pacerLanes(page)[0].textContent === '2:00.0'; });
  assert.equal(pacerLanes(page).length, 1, 'still one pacer lane');
  assert.equal(pacerLanes(page)[0].dataset.pacerMs, '120000');
  assert.equal(pixi.rower('2:00.0'), pacer, 'the same boat now reads the new pace');
  assert.equal(pixi.rower('1:35.0'), undefined, 'the old pace no longer shows');
  assert.equal(pacer.children.find(function (c) { return c.text === '2:00.0'; }).style.fill, PACER_HULL, 'the relabelled boat keeps the pacer colour');
  page.doc.dispatchEvent(new CustomEvent('pixelpace:race-finished'));
});

test('a pacer with an unusable time or target is still drawn, labelled — rather than NaN or nothing', async function () {
  async function draw(options) {
    var page = setupPage(Object.assign({
      finished: false,
      responses: { '/rooms/r1/race/status': statusQueue([]) }
    }, options));
    var pixi = makePixi();
    pixi.releaseInit();
    await startRaceView(page.track, pixi.PIXI);
    await settle();
    return { page: page, pixi: pixi };
  }
  var cases = [
    { lanes: [laneItem('u-self', 'Me', true), pacerItem(0)] },
    { lanes: [laneItem('u-self', 'Me', true), pacerItem('abc')] },
    { lanes: [laneItem('u-self', 'Me', true), pacerItem(100000)], targetMm: 'abc' },
    { lanes: [laneItem('u-self', 'Me', true), pacerItem(100000)], targetMm: '-5' }
  ];
  for (var i = 0; i < cases.length; i++) {
    var drawn = await draw(cases[i]);
    var boat = drawn.pixi.rower('—');
    assert.ok(boat, 'case ' + i + ': the pacer boat is drawn, labelled —');
    assert.equal(hullOf(boat), PACER_HULL, 'case ' + i + ': in the pacer hull');
    assert.equal(pacerLanes(drawn.page)[0].textContent, '—', 'case ' + i + ': the lane list names it — too');
    drawn.pixi.texts.forEach(function (t) { assert.ok(!/NaN/.test(String(t.text)), 'case ' + i + ': no label reads NaN'); });
    drawn.page.doc.dispatchEvent(new CustomEvent('pixelpace:race-finished'));
  }
});

test('note hidden once status brings a pacer', async function () {
  var self = [{ user_id: 'u-self', equipment_ready: true }];
  var page = setupPage({
    finished: false,
    pacerNote: true,
    lanes: [laneItem('u-self', 'Me', true)],
    responses: {
      '/rooms/r1/race/status': function () {
        return jsonResponse(200, {
          server_time: new Date().toISOString(),
          racers: self,
          pacer: { elapsed_milliseconds: 110000 }
        });
      }
    }
  });
  assert.equal(page.pacerNote.hidden, false, 'note starts visible');
  assert.equal(pacerLanes(page).length, 0, 'no pacer lane to begin with');

  var pixi = makePixi();
  pixi.releaseInit();
  await startRaceView(page.track, pixi.PIXI);

  await until(function () { return pacerLanes(page).length === 1 && page.pacerNote.hidden; });
  assert.equal(page.pacerNote.hidden, true, 'note is hidden once status brings a pacer');
  assert.equal(page.pacerNote.classList.contains('hidden'), true);
  assert.equal(pacerLanes(page).length, 1, 'pacer lane exists');
  assert.equal(pacerLanes(page)[0].dataset.pacerMs, '110000');

  page.doc.dispatchEvent(new CustomEvent('pixelpace:race-finished'));
});

test('note shown again on pacer null in a solo race', async function () {
  var self = [{ user_id: 'u-self', equipment_ready: true }];
  var phases = [
    { server_time: new Date().toISOString(), racers: self, pacer: { elapsed_milliseconds: 110000 } },
    { server_time: new Date().toISOString(), racers: self, pacer: null }
  ];
  var phase = 0;
  var served = [0, 0];
  var page = setupPage({
    finished: false,
    pacerNote: true,
    lanes: [laneItem('u-self', 'Me', true)],
    responses: {
      '/rooms/r1/race/status': function () {
        served[phase]++;
        return jsonResponse(200, phases[phase]);
      }
    }
  });

  var pixi = makePixi();
  pixi.releaseInit();
  await startRaceView(page.track, pixi.PIXI);

  // Status with pacer: note hides, pacer lane appears
  await until(function () { return pacerLanes(page).length === 1 && page.pacerNote.hidden; });
  assert.equal(page.pacerNote.hidden, true);

  // Status with pacer null and 1 racer: note shows again
  phase = 1;
  await until(function () { return pacerLanes(page).length === 0 && !page.pacerNote.hidden; });
  assert.equal(page.pacerNote.hidden, false, 'note is shown again on pacer null in a solo race');
  assert.equal(page.pacerNote.classList.contains('hidden'), false);

  page.doc.dispatchEvent(new CustomEvent('pixelpace:race-finished'));
});

test('note hidden in a multi-racer race', async function () {
  var twoRacers = [{ user_id: 'u-self', equipment_ready: true }, { user_id: 'u-2', equipment_ready: true }];
  var page = setupPage({
    finished: false,
    pacerNote: true,
    lanes: [laneItem('u-self', 'Me', true), laneItem('u-2', 'Rival', false)],
    responses: {
      '/rooms/r1/race/status': function () {
        return jsonResponse(200, {
          server_time: new Date().toISOString(),
          racers: twoRacers,
          pacer: null
        });
      }
    }
  });
  assert.equal(page.pacerNote.hidden, false, 'note starts visible');

  var pixi = makePixi();
  pixi.releaseInit();
  await startRaceView(page.track, pixi.PIXI);

  await until(function () { return page.pacerNote.hidden; });
  assert.equal(page.pacerNote.hidden, true, 'note is hidden with two racers and pacer null');
  assert.equal(page.pacerNote.classList.contains('hidden'), true);

  page.doc.dispatchEvent(new CustomEvent('pixelpace:race-finished'));
});
