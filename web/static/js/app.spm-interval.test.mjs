import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';

import { spmToIntervalMs } from './spm-interval.mjs';

var appJs = readFileSync(new URL('./app.js', import.meta.url), 'utf8');
var dashboardHtml = readFileSync(new URL('../../templates/dashboard.html', import.meta.url), 'utf8');
var raceHtml = readFileSync(new URL('../../templates/race.html', import.meta.url), 'utf8');

function metricElement() {
  return {
    textContent: '',
    classList: {
      add: function () {},
      remove: function () {},
      toggle: function () {}
    },
    querySelector: function () { return { classList: this.classList }; },
    getAttribute: function () { return ''; },
    closest: function () { return null; },
    addEventListener: function () {}
  };
}

// A panel control that records its listeners so a test can fire real events.
function simElement(value, text) {
  var el = metricElement();
  el.value = value;
  el.textContent = text || '';
  el.listeners = {};
  el.addEventListener = function (type, fn) {
    if (!el.listeners[type]) el.listeners[type] = [];
    el.listeners[type].push(fn);
  };
  el.fire = function (type) {
    (el.listeners[type] || []).forEach(function (fn) { fn({ type: type, target: el }); });
  };
  return el;
}

var PM5_UUIDS = {
  '31': 'ce060031-43e5-11e4-916c-0800200c9a66',
  '32': 'ce060032-43e5-11e4-916c-0800200c9a66',
  '33': 'ce060033-43e5-11e4-916c-0800200c9a66',
  '35': 'ce060035-43e5-11e4-916c-0800200c9a66',
  '36': 'ce060036-43e5-11e4-916c-0800200c9a66'
};
var UUID_KEYS = {};
Object.keys(PM5_UUIDS).forEach(function (key) { UUID_KEYS[PM5_UUIDS[key]] = key; });

// The app runs unmodified except for a probe around the real onNotify: it
// records each notification (uuid, payload and what the app's own
// decodePm5Notification made of it) and whether a tile render happens inside
// it. The simulator's mockPm5State is exposed so a test can jump the clock.
var ON_NOTIFY = 'function onNotify(event) {';
var DEV_MOCK_END = '  // DEV_MOCK_PM5_END';
assert.equal(appJs.split(ON_NOTIFY).length, 2, 'app.js declares onNotify once');
assert.equal(appJs.split(DEV_MOCK_END).length, 2, 'app.js closes the simulator region once');
var probedAppJs = appJs
  .replace(ON_NOTIFY, [
    'function onNotify(event) {',
    '    __probe.notifications.push({ uuid: event.target.uuid, value: event.target.value, decoded: decodePm5Notification(event.target.uuid, event.target.value) });',
    '    __probe.depth++;',
    '    try { return probedOnNotify(event); } finally { __probe.depth--; }',
    '  }',
    '  __probe.onNotify = onNotify;',
    '  function probedOnNotify(event) {'
  ].join('\n'))
  .replace(DEV_MOCK_END, '  if (devMode) __probe.mockPm5State = mockPm5State;\n' + DEV_MOCK_END);

function u24(dv, offset) {
  return dv.getUint8(offset) | (dv.getUint8(offset + 1) << 8) | (dv.getUint8(offset + 2) << 16);
}

function runDashboardApp(devMode, options) {
  var withPanel = !(options && options.panel === false);
  var probe = { notifications: [], renders: [], depth: 0 };
  var metricEls = {};
  ['elapsedTime', 'distance', 'pace', 'strokeRate', 'power', 'strokeCount', 'calories', 'heartRate'].forEach(function (key) {
    var el = metricElement();
    var text = '';
    Object.defineProperty(el, 'textContent', {
      get: function () { return text; },
      set: function (value) {
        text = value;
        probe.renders.push({ key: key, text: value, inNotify: probe.depth > 0, notification: probe.notifications.length });
      }
    });
    metricEls[key] = el;
  });

  var metricsRegion = metricElement();
  metricsRegion.querySelector = function (selector) {
    var match = selector.match(/\[data-metric="(.+)"\]/);
    return match ? metricEls[match[1]] : null;
  };

  var connectBtn = metricElement();
  var connectionInfo = metricElement();
  var rowingMachineCard = metricElement();
  rowingMachineCard.getAttribute = function (name) {
    return name === 'data-dev-mode' && devMode ? 'true' : 'false';
  };
  var rowerCanvas = metricElement();
  rowerCanvas.width = 320;
  rowerCanvas.height = 180;
  rowerCanvas.getContext = function () {
    return {
      fillStyle: '',
      strokeStyle: '',
      lineWidth: 0,
      lineCap: '',
      lineJoin: '',
      fillRect: function () {},
      beginPath: function () {},
      moveTo: function () {},
      lineTo: function () {},
      closePath: function () {},
      fill: function () {},
      stroke: function () {},
      arc: function () {}
    };
  };

  // The same markup dashboard.html and race.html render in dev mode.
  var sim = {
    panel: simElement(),
    spm: simElement('26'),
    pace: simElement('2:12'),
    start: simElement(undefined, 'Stop rowing')
  };
  var byID = {
    'bt-connect-btn': connectBtn,
    'rowing-machine-card': rowingMachineCard,
    'bt-connection-info': connectionInfo,
    'bt-metrics': metricsRegion,
    'bt-rower-canvas': rowerCanvas
  };
  if (withPanel) {
    byID['sim-panel'] = sim.panel;
    byID['sim-spm'] = sim.spm;
    byID['sim-pace'] = sim.pace;
    byID['sim-start'] = sim.start;
  }
  var documentListeners = {};
  var dispatched = [];
  var timers = [];
  var window = { isSecureContext: false, location: { href: '', protocol: 'http:', host: 'pixelpace.test' } };
  runInNewContext(probedAppJs, {
    __probe: probe,
    document: {
      addEventListener: function (type, fn) {
        if (!documentListeners[type]) documentListeners[type] = [];
        documentListeners[type].push(fn);
      },
      body: { addEventListener: function () {} },
      getElementById: function (id) { return byID[id] || null; },
      querySelector: function () { return null; },
      dispatchEvent: function (evt) { dispatched.push(evt); return true; }
    },
    CustomEvent: function (type, init) { this.type = type; this.detail = init && init.detail; },
    window: window,
    navigator: {},
    htmx: { trigger: function () {} },
    // The race socket is never reachable here: samples stay in the replay buffer.
    WebSocket: function () { throw new Error('offline'); },
    setTimeout: function () { return 0; },
    clearTimeout: function () {},
    setInterval: function (fn, delay) {
      timers.push({ fn: fn, delay: delay, active: true });
      return timers.length;
    },
    clearInterval: function (id) { if (timers[id - 1]) timers[id - 1].active = false; }
  });

  function activeTimer(name) {
    return timers.find(function (timer) { return timer.active && timer.fn.name === name; });
  }

  return {
    metricEls: metricEls,
    probe: probe,
    // Characteristic keys ('31'..'36') of the notifications since index `from`.
    notified: function (from) {
      return probe.notifications.slice(from || 0).map(function (n) { return UUID_KEYS[n.uuid]; });
    },
    // The latest notification on a characteristic: its payload and decoded metrics.
    last: function (key) {
      for (var i = probe.notifications.length - 1; i >= 0; i--) {
        if (probe.notifications[i].uuid === PM5_UUIDS[key]) return probe.notifications[i];
      }
      return null;
    },
    connectionInfo: connectionInfo,
    timers: timers,
    documentListeners: documentListeners,
    sim: sim,
    window: window,
    events: function (type) {
      return dispatched.filter(function (evt) { return evt.type === type; }).map(function (evt) { return evt.detail; });
    },
    statusTimer: function () { return activeTimer('mockPm5StatusTick'); },
    strokeTimer: function () { return activeTimer('mockPm5StrokeTick'); },
    rowerTimer: function () {
      return timers.find(function (timer) { return timer.active && !/^mockPm5/.test(timer.fn.name); });
    },
    statusTicks: function (n) {
      for (var i = 0; i < n; i++) activeTimer('mockPm5StatusTick').fn();
    },
    strokeTick: function () { activeTimer('mockPm5StrokeTick').fn(); },
    set: function (control, value, type) {
      sim[control].value = value;
      sim[control].fire(type || 'change');
    }
  };
}

function renderedTiles(result) {
  return Object.keys(result.metricEls).map(function (key) { return String(result.metricEls[key].textContent); });
}

test('spmToIntervalMs maps endpoints and clamps out-of-range SPM', function () {
  assert.equal(spmToIntervalMs(16), 1000, '16 SPM maps to 1000ms');
  assert.equal(spmToIntervalMs(40), 200, '40 SPM maps to 200ms');
  assert.equal(spmToIntervalMs(0), 1000, 'below-range SPM clamps to 1000ms');
  assert.equal(spmToIntervalMs(60), 200, 'above-range SPM clamps to 200ms');
});

test('spmToIntervalMs stays within [200,1000] and is non-increasing', function () {
  var previous = spmToIntervalMs(0);

  for (var spm = 0; spm <= 60; spm++) {
    var interval = spmToIntervalMs(spm);
    assert.ok(interval >= 200 && interval <= 1000, spm + ' SPM maps within [200,1000]');
    assert.ok(interval <= previous, spm + ' SPM does not increase the interval');
    previous = interval;
  }
});

test('dashboard starts the rower canvas hidden in the Rowing Machine card', function () {
  assert.match(dashboardHtml, /<h2 class="card-title">Rowing Machine<\/h2>[\s\S]*<canvas id="bt-rower-canvas" class="hidden [^"]*" width="320" height="180"/);
});

test('app reveals the canvas after connection success and draws the resting scene', function () {
  assert.match(appJs, /var server = await device\.gatt\.connect\(\);[\s\S]*showConnectionInfo\([\s\S]*\);\n      revealRowerCanvas\(\);\n      showZeroMetrics\(\);/);
  assert.match(appJs, /function revealRowerCanvas\(\) \{[\s\S]*rowerCanvas\.classList\.remove\('hidden'\);[\s\S]*drawRestingRower\(\);[\s\S]*\}/);
  assert.match(appJs, /rowerCtx\.fillStyle = '#1e63c9';\n    rowerCtx\.fillRect\(0, 0, rowerCanvas\.width, rowerCanvas\.height\);/);
});

test('rower sprite uses exactly two in-code frames and no image asset loader', function () {
  assert.match(appJs, /function drawRowerFrame\(frame\)/);
  assert.match(appJs, /if \(frame === 1\)/);
  assert.match(appJs, /currentRowerFrame = currentRowerFrame === 0 \? 1 : 0;/);
  assert.doesNotMatch(appJs, /new Image|Image\(|\.png|\.jpg|\.jpeg|\.svg|\.webp|\.gif/);
});

test('SPM notifications start one rescheduled timer and SPM zero stops at rest', function () {
  assert.match(appJs, /if \(typeof metrics\.strokeRate === 'number'\) syncRowerAnimation\(metrics\.strokeRate\);/);
  assert.match(appJs, /function startRowerAnimation\(intervalMs\) \{\n    stopRowerAnimation\(\);[\s\S]*rowerTimer = setInterval/);
  assert.match(appJs, /if \(currentStrokeRate <= 0\) \{\n      stopRowerAnimation\(\);\n      drawRestingRower\(\);\n      return;\n    \}/);
});

test('disconnect hides the canvas and clears the timer', function () {
  assert.match(appJs, /device\.addEventListener\('gattserverdisconnected', function \(\) \{\n        stopRowerAnimation\(\);\n        hideRowerCanvas\(\);\n        showConnectionInfo/);
  assert.match(appJs, /function hideRowerCanvas\(\) \{[\s\S]*rowerCanvas\.classList\.add\('hidden'\);[\s\S]*\}/);
  assert.match(appJs, /function stopRowerAnimation\(\) \{\n    if \(rowerTimer\) clearInterval\(rowerTimer\);\n    rowerTimer = null;\n    rowerTimerIntervalMs = null;\n  \}/);
});

test('dev-mode simulator auto-starts at 26 spm and 2:12 with no Bluetooth present', function () {
  var result = runDashboardApp(true);

  assert.equal(result.metricEls.strokeRate.textContent, '26 spm');
  assert.equal(result.metricEls.pace.textContent, '2:12 /500m');
  assert.equal(result.metricEls.distance.textContent, '0 m');
  assert.equal(result.metricEls.heartRate.textContent, '0 bpm');
  assert.equal(result.connectionInfo.textContent, 'Development mock PM5 — streaming synthetic rowing metrics.');
  assert.equal(result.statusTimer().delay, 500, 'status characteristics every 500 ms');
  assert.equal(result.strokeTimer().delay, Math.round(60000 / 26), 'one stroke every 60/SPM seconds');
  assert.equal(result.sim.start.textContent, 'Stop rowing');
});

test('dashboard and race pages wrap the simulator panel in the development-mode flag', function () {
  [['dashboard', dashboardHtml], ['race', raceHtml]].forEach(function (page) {
    var html = page[1];
    var panel = html.match(/{{if \.DevMode}}\s*<div id="sim-panel"[^>]*data-sim-control="panel"[\s\S]*?{{end}}/);
    assert.ok(panel, page[0] + ' wraps #sim-panel in {{if .DevMode}}');
    assert.match(panel[0], /<input id="sim-spm" type="number" min="0" max="60" step="1" value="26"[^>]*data-sim-control="spm"/);
    assert.match(panel[0], /<input id="sim-pace" type="text" value="2:12"[^>]*data-sim-control="pace"/);
    assert.match(panel[0], /<button id="sim-start" type="button"[^>]*data-sim-control="start">Stop rowing<\/button>/);
    assert.ok(html.indexOf('id="bt-connection-info"') < html.indexOf('id="sim-panel"'), page[0] + ' places the panel after #bt-connection-info');
  });
  assert.ok(raceHtml.indexOf('id="sim-panel"') < raceHtml.indexOf('id="bt-metrics"'), 'race panel sits before #bt-metrics');
});

test('dev mode wires the panel controls and registers no document keydown listener', function () {
  var result = runDashboardApp(true);

  assert.equal(result.sim.spm.listeners.input.length, 1);
  assert.equal(result.sim.spm.listeners.change.length, 1);
  assert.equal(result.sim.pace.listeners.input.length, 1);
  assert.equal(result.sim.pace.listeners.change.length, 1);
  assert.equal(result.sim.start.listeners.click.length, 1);
  assert.equal(result.documentListeners.keydown, undefined, 'the arrow-key controls are gone');
});

test('the simulator streams and renders without the panel on the page', function () {
  var result;
  assert.doesNotThrow(function () { result = runDashboardApp(true, { panel: false }); });

  result.statusTicks(4);

  assert.equal(result.metricEls.elapsedTime.textContent, '0:02');
  assert.equal(result.metricEls.distance.textContent, '8 m', '2 s at 2:12 is 7.58 m');
  assert.equal(result.metricEls.strokeRate.textContent, '26 spm');
});

test('distance grows at 500/pace m/s and distance and elapsed never go backwards', function () {
  var result = runDashboardApp(true);
  var stream = result.window.startRaceStream('race-one');
  result.set('pace', '2:00');
  var n = 12;

  result.statusTicks(n);

  var expectedMm = n * 0.5 * 500 / 120 * 1000;
  var last = stream.buffer[stream.buffer.length - 1];
  assert.ok(Math.abs(last.distance_millimeters - expectedMm) <= 100, last.distance_millimeters + ' mm is within 0.1 m of ' + expectedMm);
  assert.equal(last.elapsed_milliseconds, n * 500);
  assert.equal(result.metricEls.distance.textContent, '25 m');
  assert.equal(stream.buffer.length, 3 * n, 'one sample per status notification');
  for (var i = 1; i < stream.buffer.length; i++) {
    assert.ok(stream.buffer[i].distance_millimeters >= stream.buffer[i - 1].distance_millimeters, 'distance never decreases');
    assert.ok(stream.buffer[i].elapsed_milliseconds >= stream.buffer[i - 1].elapsed_milliseconds, 'elapsed never decreases');
  }
});

test('a stroke rate or pace change shows within one status tick and persists', function () {
  var result = runDashboardApp(true);
  var stream = result.window.startRaceStream('race-one');
  result.set('spm', '30', 'input');
  result.set('pace', '1:50', 'input');

  result.statusTicks(1);

  assert.equal(result.metricEls.strokeRate.textContent, '30 spm');
  assert.equal(result.metricEls.pace.textContent, '1:50 /500m');
  assert.equal(stream.buffer[stream.buffer.length - 1].stroke_rate, 30, 'the next race sample carries the new stroke rate');
  assert.equal(result.strokeTimer().delay, 2000, 'strokes follow the new rate');
  result.strokeTick();
  result.statusTicks(3);
  assert.equal(result.metricEls.strokeRate.textContent, '30 spm');
  assert.equal(result.metricEls.pace.textContent, '1:50 /500m');
});

test('simulated readings fill the dashboard tiles and drive the rower animation through onNotify', function () {
  var result = runDashboardApp(true);

  result.statusTicks(4);
  result.strokeTick();

  assert.equal(result.metricEls.elapsedTime.textContent, '0:02');
  assert.equal(result.metricEls.distance.textContent, '8 m');
  assert.equal(result.metricEls.strokeCount.textContent, 1);
  assert.match(result.metricEls.power.textContent, /^[1-9]\d* W$/);
  assert.match(result.metricEls.calories.textContent, /^\d+ kcal$/);
  assert.equal(result.rowerTimer().delay, spmToIntervalMs(26), 'the rower animates at the simulated stroke rate');
});

test('stopped or at SPM 0 the simulator rests: no distance, no strokes, zero rate and power, elapsed keeps counting', function () {
  function assertResting(result, label) {
    var distance = result.metricEls.distance.textContent;
    var strokes = result.metricEls.strokeCount.textContent;
    assert.equal(result.strokeTimer(), undefined, label + ': no stroke payloads are scheduled');
    result.statusTicks(4);
    assert.equal(result.metricEls.distance.textContent, distance, label + ': no distance gained');
    assert.equal(result.metricEls.strokeCount.textContent, strokes, label + ': no strokes');
    assert.equal(result.metricEls.strokeRate.textContent, '0 spm');
    assert.equal(result.metricEls.power.textContent, '0 W');
    assert.equal(result.metricEls.heartRate.textContent, '0 bpm');
    assert.equal(result.metricEls.elapsedTime.textContent, '0:04', label + ': elapsed keeps counting');
  }

  var stopped = runDashboardApp(true);
  stopped.statusTicks(4);
  stopped.strokeTick();
  assert.notEqual(stopped.metricEls.power.textContent, '0 W');
  stopped.sim.start.fire('click');
  assert.equal(stopped.sim.start.textContent, 'Start rowing');
  assertResting(stopped, 'stopped');
  stopped.sim.start.fire('click');
  assert.equal(stopped.sim.start.textContent, 'Stop rowing');
  stopped.statusTicks(2);
  assert.equal(stopped.metricEls.distance.textContent, '11 m', 'rowing resumes gaining distance');
  assert.ok(stopped.strokeTimer(), 'strokes resume');

  var spmZero = runDashboardApp(true);
  spmZero.statusTicks(4);
  spmZero.strokeTick();
  spmZero.set('spm', '0');
  assertResting(spmZero, 'SPM 0');
});

test('panel settings are clamped or refused and never render NaN or a negative', function () {
  var result = runDashboardApp(true);
  var stream = result.window.startRaceStream('race-one');
  [['0', '0'], ['-5', '0'], ['99', '60'], ['abc', '60'], ['NaN', '60']].forEach(function (pair) {
    result.set('spm', pair[0]);
    assert.equal(result.sim.spm.value, pair[1], 'SPM ' + pair[0]);
    result.statusTicks(1);
  });
  result.set('spm', '20');
  [['0:30', '1:00'], ['9:99', '5:00'], ['abc', '5:00'], ['', '5:00']].forEach(function (pair) {
    result.set('pace', pair[0]);
    assert.equal(result.sim.pace.value, pair[1], 'pace "' + pair[0] + '"');
    result.statusTicks(1);
    result.strokeTick();
  });
  result.set('pace', '1:00', 'input');
  result.statusTicks(2);

  renderedTiles(result).forEach(function (text) {
    assert.doesNotMatch(text, /NaN|-/, 'tile "' + text + '"');
  });
  assert.equal(result.metricEls.pace.textContent, '1:00 /500m');
  for (var i = 0; i < stream.buffer.length; i++) {
    var sample = stream.buffer[i];
    assert.ok(Number.isFinite(sample.distance_millimeters) && sample.distance_millimeters >= 0);
    assert.ok(Number.isFinite(sample.elapsed_milliseconds) && sample.elapsed_milliseconds >= 0);
    assert.ok(Number.isFinite(sample.power) && sample.power >= 0);
    if (i > 0) assert.ok(sample.elapsed_milliseconds >= stream.buffer[i - 1].elapsed_milliseconds, 'elapsed never goes backwards');
  }
});

test('production dashboard does not start the simulator or wire the panel', function () {
  var result = runDashboardApp(false);

  assert.equal(result.metricEls.strokeRate.textContent, '');
  assert.equal(result.metricEls.pace.textContent, '');
  assert.equal(result.connectionInfo.textContent, '');
  assert.equal(result.timers.length, 0, 'no simulator timers');
  assert.equal(result.events('pixelpace:pm5-status').length, 0);
  ['panel', 'spm', 'pace', 'start'].forEach(function (control) {
    assert.deepEqual(result.sim[control].listeners, {}, 'production registers no sim-' + control + ' listeners');
  });
  assert.equal(result.documentListeners.keydown, undefined);
});

test('dashboard exposes server-derived development mode to app.js', function () {
  assert.match(dashboardHtml, /id="rowing-machine-card"[^>]*data-dev-mode="{{if \.DevMode}}true{{else}}false{{end}}"/);
  assert.match(appJs, /var rowingMachineCard = document\.getElementById\('rowing-machine-card'\);\n  var devMode = rowingMachineCard && rowingMachineCard\.getAttribute\('data-dev-mode'\) === 'true';/);
});

test('mock PM5 activation is gated on the development-mode flag', function () {
  assert.match(appJs, /function startMockPm5\(\) \{\n    if \(!devMode\) return;/);
  assert.match(appJs, /if \(devMode\) startMockPm5\(\);/);
  assert.doesNotMatch(appJs, /(^|[^\w])startMockPm5\(\);(?![\s\S]*DEV_MOCK_PM5_END)/);
});

test('the simulator region feeds onNotify only and is confined away from Web Bluetooth', function () {
  var mockRegion = appJs.match(/\/\/ DEV_MOCK_PM5_START([\s\S]*?)\/\/ DEV_MOCK_PM5_END/)[1];
  assert.match(mockRegion, /onNotify\(\{ target: \{ uuid: pm5Characteristics\[key\]\.uuid, value: value \} \}\)/);
  assert.doesNotMatch(mockRegion, /renderMetrics\(|streamRaceMetrics\(|syncRowerAnimation\(|emitMockPm5Metrics|mockPm5Metrics\b/, 'the simulator feeds onNotify only');
  assert.doesNotMatch(mockRegion, /navigator\.bluetooth|requestDevice|gatt\.connect|getPrimaryService|startNotifications|subscribeToPm5Metrics/);
});

function isDataView(value) {
  return Object.prototype.toString.call(value) === '[object DataView]';
}

// The tile text renderMetric writes for a decoded value.
function tileText(key, value) {
  function duration(total) {
    var s = Math.max(0, Math.floor(total || 0));
    return Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0');
  }
  if (key === 'elapsedTime') return duration(value);
  if (key === 'pace') return duration(value) + ' /500m';
  if (key === 'strokeCount') return Math.round(value);
  var unit = { distance: ' m', strokeRate: ' spm', power: ' W', calories: ' kcal', heartRate: ' bpm' }[key];
  return Math.round(value) + unit;
}

test('every simulator reading reaches the tiles only from a ce06003x DataView notification through onNotify', function () {
  var result = runDashboardApp(true);
  result.statusTicks(6);
  result.strokeTick();
  result.set('spm', '31', 'input');
  result.set('pace', '1:48', 'input');
  result.statusTicks(2);
  result.strokeTick();

  var probe = result.probe;
  var firstNotify = probe.renders.findIndex(function (render) { return render.inNotify; });
  assert.equal(firstNotify, 8, 'only the eight zero tiles of showZeroMetrics render before the first notification');
  var later = probe.renders.slice(firstNotify);
  assert.ok(later.length > 30);
  later.forEach(function (render) {
    assert.ok(render.inNotify, render.key + ' "' + render.text + '" renders inside onNotify');
    var notification = probe.notifications[render.notification - 1];
    assert.ok(Object.values(PM5_UUIDS).includes(notification.uuid), 'from a ce06003x notification');
    assert.ok(isDataView(notification.value), 'carrying a DataView');
    assert.ok(render.key in notification.decoded, render.key + ' is a field decodePm5Notification produced');
    assert.equal(render.text, tileText(render.key, notification.decoded[render.key]), 'the tile shows the decoded value');
  });
  assert.equal(result.metricEls.strokeRate.textContent, '31 spm');
  assert.equal(result.metricEls.pace.textContent, '1:48 /500m');
  assert.equal(result.metricEls.distance.textContent, tileText('distance', result.last('31').decoded.distance));
});

test('each simulator payload decodes to the simulator state at PM5 resolution and renders it', function () {
  var result = runDashboardApp(true);
  var n = 1200; // 10 minutes at 26 spm and 2:12
  var strokes = 5;
  result.statusTicks(n);
  for (var i = 0; i < strokes; i++) result.strokeTick();

  var seconds = n * 0.5;
  var metres = seconds * 500 / 132;
  var watts = 2.8 / Math.pow(132 / 500, 3);
  var kcal = seconds * (4 * watts + 350) / 3600;
  var close = function (actual, expected, label) { assert.ok(Math.abs(actual - expected) < 1e-6, label + ': ' + actual + ' vs ' + expected); };

  var general = result.last('31').decoded;
  close(general.elapsedTime, Math.round(seconds * 100) / 100, 'elapsed time 0.01 s');
  close(general.distance, Math.round(metres * 10) / 10, 'distance 0.1 m');
  var status1 = result.last('32').decoded;
  assert.equal(status1.strokeRate, 26);
  close(status1.pace, 132, 'pace 0.01 s');
  assert.equal(result.last('32').value.getUint8(6), 255, 'heart rate byte is the invalid marker');
  assert.equal(status1.heartRate, 0, 'heart rate 255 decodes to 0');
  assert.equal(result.last('33').decoded.calories, Math.round(kcal), 'whole kcal');
  assert.equal(result.last('36').decoded.power, Math.round(watts), 'stroke power');
  assert.equal(result.last('35').decoded.strokeCount, strokes, '0x35 stroke count');
  assert.equal(result.last('36').decoded.strokeCount, strokes, '0x36 stroke count');

  assert.equal(result.metricEls.elapsedTime.textContent, '10:00');
  assert.equal(result.metricEls.distance.textContent, Math.round(Math.round(metres * 10) / 10) + ' m');
  assert.equal(result.metricEls.strokeRate.textContent, '26 spm');
  assert.equal(result.metricEls.pace.textContent, '2:12 /500m');
  assert.equal(result.metricEls.heartRate.textContent, '0 bpm');
  assert.equal(result.metricEls.calories.textContent, Math.round(kcal) + ' kcal');
  assert.equal(result.metricEls.power.textContent, Math.round(watts) + ' W');
  assert.equal(result.metricEls.strokeCount.textContent, strokes);
});

test('simulator payloads are full Concept2 layouts with the just-row defaults at their spec offsets', function () {
  var result = runDashboardApp(true);

  // At start-up nothing has been rowed: average pace is 0, not a division by zero.
  assert.equal(result.last('32').value.getUint16(9, true), 0, '0x32 average pace at distance 0');
  assert.equal(result.last('33').value.getUint16(8, true), 0, '0x33 average pace at distance 0');

  var n = 40;
  result.statusTicks(n);
  result.strokeTick();
  var seconds = n * 0.5;
  var metres = seconds * 500 / 132;
  var watts = 2.8 / Math.pow(132 / 500, 3);
  var lengths = { '31': 19, '32': 17, '33': 20, '35': 20, '36': 15 };
  Object.keys(lengths).forEach(function (key) {
    assert.equal(result.last(key).value.byteLength, lengths[key], '0x' + key + ' byteLength');
  });

  var g = result.last('31').value;
  assert.equal(u24(g, 0), Math.round(seconds * 100));
  assert.equal(u24(g, 3), Math.round(metres * 10));
  assert.equal(g.getUint8(6), 1, 'workout type: just row');
  assert.equal(g.getUint8(7), 255, 'interval type: none');
  assert.equal(g.getUint8(8), 1, 'workout state: workout row');
  assert.equal(g.getUint8(9), 1, 'rowing state while rowing');
  assert.equal(g.getUint8(10), 0, 'stroke state');
  assert.equal(u24(g, 11), Math.floor(metres), 'total work distance');
  assert.equal(u24(g, 14), 0, 'workout duration');
  assert.equal(g.getUint8(17), 0, 'workout duration type');
  assert.equal(g.getUint8(18), 120, 'drag factor');

  var a1 = result.last('32').value;
  var averagePace = Math.round(seconds / metres * 500 * 100);
  assert.equal(u24(a1, 0), Math.round(seconds * 100));
  assert.equal(a1.getUint16(3, true), Math.round(500 / 132 * 1000), 'speed 0.001 m/s = 500/pace');
  assert.equal(a1.getUint8(5), 26);
  assert.equal(a1.getUint8(6), 255);
  assert.equal(a1.getUint16(7, true), 13200);
  assert.equal(a1.getUint16(9, true), averagePace, '0x32 average pace');
  assert.equal(a1.getUint16(11, true), 0, 'rest distance');
  assert.equal(u24(a1, 13), 0, 'rest time');
  assert.equal(a1.getUint8(16), 0, 'erg machine type');

  var a2 = result.last('33').value;
  assert.equal(u24(a2, 0), Math.round(seconds * 100));
  assert.equal(a2.getUint8(3), 0, 'interval count');
  assert.equal(a2.getUint16(4, true), Math.round(watts), 'average power');
  assert.equal(a2.getUint16(8, true), averagePace, '0x33 average pace');
  assert.equal(u24(a2, 14), 0, 'last split time');
  assert.equal(u24(a2, 17), 0, 'last split distance');

  var s = result.last('35').value;
  assert.equal(u24(s, 0), Math.round(seconds * 100));
  assert.equal(u24(s, 3), Math.round(metres * 10));
  assert.equal(s.getUint16(18, true), 1, 'stroke count at 18');
  assert.ok(s.getUint16(16, true) > 0, 'work per stroke at 16');

  var as = result.last('36').value;
  assert.equal(u24(as, 0), Math.round(seconds * 100));
  assert.equal(as.getUint16(3, true), Math.round(watts));
  assert.equal(as.getUint16(7, true), 1);
  assert.equal(u24(as, 9), 0, 'projected work time');
  assert.equal(u24(as, 12), 0, 'projected work distance');

  result.set('spm', '0');
  result.statusTicks(1);
  assert.equal(result.last('31').value.getUint8(9), 0, 'rowing state 0 at SPM 0');
  assert.equal(result.last('32').value.getUint16(3, true), 0, 'no speed at rest');
  result.set('spm', '20');
  result.statusTicks(1);
  assert.equal(result.last('31').value.getUint8(9), 1, 'rowing state back to 1');
});

test('a real PM5 Stroke Data notification takes stroke count from bytes 18-19, not work per stroke at 16-17', function () {
  var result = runDashboardApp(false);
  var dv = new DataView(new ArrayBuffer(20));
  dv.setUint8(0, 0x10); // elapsed 0.16 s
  dv.setUint16(16, 4321, true); // work per stroke, 0.1 J
  dv.setUint16(18, 57, true); // stroke count

  result.probe.onNotify({ target: { uuid: PM5_UUIDS['35'], value: dv } });

  assert.equal(result.probe.notifications[0].decoded.strokeCount, 57);
  assert.equal(result.metricEls.strokeCount.textContent, 57);
});

test('simulated distance, power and calories follow the PM5 model and rest at SPM 0', function () {
  var result = runDashboardApp(true);
  result.set('pace', '1:45');
  result.statusTicks(10);
  var before = result.last('31').decoded;
  var calsBefore = result.probe.mockPm5State.calories;
  var dt = 30; // seconds, 60 status ticks
  result.statusTicks(dt * 2);
  result.strokeTick();
  var after = result.last('31').decoded;
  var watts = 2.8 / Math.pow(105 / 500, 3);

  assert.ok(after.elapsedTime > before.elapsedTime);
  assert.ok(Math.abs((after.distance - before.distance) - 500 / 105 * dt) <= 0.1, 'distance delta ' + (after.distance - before.distance));
  assert.equal(result.last('36').decoded.power, Math.round(watts), 'power 2.80/(pace/500)^3');
  assert.ok(Math.abs((result.probe.mockPm5State.calories - calsBefore) - dt * (4 * watts + 350) / 3600) < 1e-9, 'calories at (4W+350) per hour');
  assert.equal(result.metricEls.power.textContent, Math.round(watts) + ' W');

  result.set('spm', '0');
  var rest = result.last('31').decoded;
  result.statusTicks(6);
  var later = result.last('31').decoded;
  assert.equal(later.distance, rest.distance, 'no distance at SPM 0');
  assert.ok(later.elapsedTime > rest.elapsedTime, 'elapsed keeps counting');
  assert.equal(result.last('32').decoded.strokeRate, 0);
  assert.equal(result.last('32').decoded.pace, 0);
  assert.equal(result.last('36').decoded.power, 0);
  assert.equal(result.metricEls.pace.textContent, '0:00 /500m');
  assert.equal(result.metricEls.power.textContent, '0 W');
});

test('encoded fields are clamped to their width and never decode to NaN or a negative', function () {
  var result = runDashboardApp(true);
  function assertSane(from) {
    result.probe.notifications.slice(from).forEach(function (n) {
      Object.keys(n.decoded).forEach(function (key) {
        var value = n.decoded[key];
        assert.ok(Number.isFinite(value) && value >= 0, key + ' = ' + value);
      });
    });
    renderedTiles(result).forEach(function (text) { assert.doesNotMatch(text, /NaN|-/, 'tile "' + text + '"'); });
  }
  assertSane(0); // distance 0 at start-up

  [['60', '1:00'], ['0', '5:00'], ['60', '5:00'], ['1', '1:00']].forEach(function (setting) {
    var from = result.probe.notifications.length;
    result.set('spm', setting[0]);
    result.set('pace', setting[1]);
    result.statusTicks(3);
    if (result.strokeTimer()) result.strokeTick();
    assertSane(from);
  });

  // Far past every field's range, and below zero: clamped, not wrapped.
  var state = result.probe.mockPm5State;
  result.set('spm', '60');
  result.set('pace', '1:00');
  state.elapsedTime = 1e7;
  state.distance = 1e8;
  state.calories = 1e7;
  state.work = 1e12;
  state.strokeCount = 1e6;
  var from = result.probe.notifications.length;
  result.statusTicks(1);
  result.strokeTick();
  assertSane(from);
  var g = result.last('31').value;
  assert.equal(u24(g, 0), 0xffffff, 'elapsed time clamps to u24');
  assert.equal(u24(g, 3), 0xffffff, 'distance clamps to u24');
  assert.equal(u24(g, 11), 0xffffff, 'work distance clamps to u24');
  assert.equal(result.last('33').decoded.calories, 0xffff, 'calories clamp to u16');
  assert.equal(result.last('35').decoded.strokeCount, 0xffff, 'stroke count clamps to u16');

  state.distance = -50;
  state.calories = -3;
  from = result.probe.notifications.length;
  result.statusTicks(1);
  assertSane(from);
  assert.equal(result.last('31').decoded.distance, 0, 'a negative distance encodes as 0');
  assert.equal(u24(result.last('31').value, 11), 0, 'a negative work distance encodes as 0');
  assert.equal(result.last('33').decoded.calories, 0, 'negative calories encode as 0');
});

test('status characteristics notify every 500 ms and stroke characteristics once per stroke at 60000/SPM', function () {
  var result = runDashboardApp(true);
  var mockTimers = function () {
    return result.timers.filter(function (t) { return t.active && /^mockPm5/.test(t.fn.name); });
  };
  assert.deepEqual(result.notified(0), ['31', '32', '33'], 'start-up sends one status set');
  assert.equal(mockTimers().length, 2, 'one status timer and one stroke timer');
  assert.ok(!mockTimers().some(function (t) { return t.delay === 1000; }), 'no combined once-per-second update');
  assert.equal(result.statusTimer().delay, 500);
  assert.equal(result.strokeTimer().delay, 2308);

  var from = result.probe.notifications.length;
  result.statusTicks(1);
  assert.deepEqual(result.notified(from), ['31', '32', '33']);
  from = result.probe.notifications.length;
  result.strokeTick();
  assert.deepEqual(result.notified(from), ['35', '36']);

  var old = result.strokeTimer();
  result.set('spm', '40', 'input');
  assert.equal(old.active, false, 'the old stroke timer is cleared');
  assert.equal(result.strokeTimer().delay, 1500, 're-timed to the new SPM');
  assert.equal(mockTimers().length, 2);

  result.set('spm', '0');
  assert.equal(result.strokeTimer(), undefined, 'no stroke timer at SPM 0');
  from = result.probe.notifications.length;
  result.statusTicks(5);
  assert.deepEqual(result.notified(from), ['31', '32', '33', '31', '32', '33', '31', '32', '33', '31', '32', '33', '31', '32', '33'], 'no 0x35/0x36 at SPM 0');
});
