import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

import { spmToIntervalMs } from './spm-interval.mjs';

var appJs = readFileSync(new URL('./app.js', import.meta.url), 'utf8');
var dashboardHtml = readFileSync(new URL('../../templates/dashboard.html', import.meta.url), 'utf8');

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
