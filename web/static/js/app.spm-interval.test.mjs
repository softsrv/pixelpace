import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';

import { spmToIntervalMs } from './spm-interval.mjs';

var appJs = readFileSync(new URL('./app.js', import.meta.url), 'utf8');
var dashboardHtml = readFileSync(new URL('../../templates/dashboard.html', import.meta.url), 'utf8');

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

function runDashboardApp(devMode) {
  var metricEls = {};
  ['elapsedTime', 'distance', 'pace', 'strokeRate', 'power', 'strokeCount', 'calories', 'heartRate'].forEach(function (key) {
    metricEls[key] = metricElement();
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

  var byID = {
    'bt-connect-btn': connectBtn,
    'rowing-machine-card': rowingMachineCard,
    'bt-connection-info': connectionInfo,
    'bt-metrics': metricsRegion,
    'bt-rower-canvas': rowerCanvas
  };
  var documentListeners = {};
  var timers = [];
  runInNewContext(appJs, {
    document: {
      addEventListener: function (type, fn) {
        if (!documentListeners[type]) documentListeners[type] = [];
        documentListeners[type].push(fn);
      },
      body: { addEventListener: function () {} },
      getElementById: function (id) { return byID[id] || null; },
      querySelector: function () { return null; }
    },
    window: { isSecureContext: false, location: { href: '' } },
    navigator: {},
    htmx: { trigger: function () {} },
    Math: Math,
    Number: Number,
    Object: Object,
    String: String,
    setInterval: function (fn) { timers.push(fn); return timers.length; },
    clearInterval: function () {}
  });

  function keydown(key) {
    var prevented = false;
    var listeners = documentListeners.keydown || [];
    listeners.forEach(function (fn) {
      fn({
        key: key,
        preventDefault: function () { prevented = true; }
      });
    });
    return prevented;
  }

  return { metricEls: metricEls, connectionInfo: connectionInfo, timers: timers, documentListeners: documentListeners, keydown: keydown };
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

test('dev-mode mock PM5 renders synthetic metrics with no Bluetooth present', function () {
  var result = runDashboardApp(true);

  assert.equal(result.metricEls.strokeRate.textContent, '26 spm');
  assert.equal(result.metricEls.pace.textContent, '2:12 /500m');
  assert.equal(result.metricEls.distance.textContent, '42 m');
  assert.equal(result.connectionInfo.textContent, 'Development mock PM5 — streaming synthetic rowing metrics.');
});

test('mock PM5 keyboard controls register globally in dev mode only', function () {
  var devResult = runDashboardApp(true);
  var prodResult = runDashboardApp(false);

  assert.equal(devResult.documentListeners.keydown.length, 1);
  assert.equal(prodResult.documentListeners.keydown, undefined);
});

test('mock PM5 arrow keys adjust stroke rate and pace by one and prevent page scroll', function () {
  var result = runDashboardApp(true);

  assert.equal(result.keydown('ArrowUp'), true);
  assert.equal(result.metricEls.strokeRate.textContent, '27 spm');
  assert.equal(result.keydown('ArrowDown'), true);
  assert.equal(result.metricEls.strokeRate.textContent, '26 spm');
  assert.equal(result.keydown('ArrowLeft'), true);
  assert.equal(result.metricEls.pace.textContent, '2:11 /500m');
  assert.equal(result.keydown('ArrowRight'), true);
  assert.equal(result.metricEls.pace.textContent, '2:12 /500m');
});

test('mock PM5 arrow key adjustments persist across timer ticks', function () {
  var result = runDashboardApp(true);

  result.keydown('ArrowUp');
  result.keydown('ArrowLeft');
  result.timers[1]();

  assert.equal(result.metricEls.strokeRate.textContent, '27 spm');
  assert.equal(result.metricEls.pace.textContent, '2:11 /500m');
});

test('mock PM5 keyboard ignores non-arrow keys without preventDefault', function () {
  var result = runDashboardApp(true);

  assert.equal(result.keydown('KeyA'), false);
  assert.equal(result.metricEls.strokeRate.textContent, '26 spm');
  assert.equal(result.metricEls.pace.textContent, '2:12 /500m');
});

test('production dashboard does not auto-start the mock PM5', function () {
  var result = runDashboardApp(false);

  assert.equal(result.metricEls.strokeRate.textContent, '');
  assert.equal(result.metricEls.pace.textContent, '');
  assert.equal(result.connectionInfo.textContent, '');
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

test('mock PM5 emits through the existing metrics and animation render loop', function () {
  var mockRegion = appJs.match(/\/\/ DEV_MOCK_PM5_START([\s\S]*?)\/\/ DEV_MOCK_PM5_END/)[1];
  assert.match(mockRegion, /function emitMockPm5Metrics\(tick\) \{[\s\S]*var metrics = mockPm5Metrics\(tick\);[\s\S]*renderMetrics\(metrics\);[\s\S]*syncRowerAnimation\(metrics\.strokeRate\);/);
  assert.match(appJs, /function onNotify\(event\) \{[\s\S]*renderMetrics\(metrics\);\n    if \(typeof metrics\.strokeRate === 'number'\) syncRowerAnimation\(metrics\.strokeRate\);/);
});

test('mock PM5 synthetic stroke rate and pace come from the static config', function () {
  assert.match(appJs, /var mockPm5Config = \{[\s\S]*pace: 132,[\s\S]*strokeRate: 26,[\s\S]*\};/);
  assert.match(appJs, /function mockPm5Metrics\(tick\) \{[\s\S]*pace: mockPm5Config\.pace,[\s\S]*strokeRate: mockPm5Config\.strokeRate,[\s\S]*\};/);
  assert.doesNotMatch(appJs, /addEventListener\('(?:input|change)'[\s\S]*mockPm5Config|type="range"|data-mock-pm5-control/);
});

test('mock PM5 keyboard controls are confined away from Web Bluetooth', function () {
  var mockRegion = appJs.match(/\/\/ DEV_MOCK_PM5_START([\s\S]*?)\/\/ DEV_MOCK_PM5_END/)[1];
  assert.doesNotMatch(mockRegion, /navigator\.bluetooth|requestDevice|gatt\.connect|getPrimaryService|startNotifications|subscribeToPm5Metrics|onNotify/);
});
