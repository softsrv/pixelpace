import test from 'node:test';
import assert from 'node:assert/strict';

import { GO_DISPLAY_MS, countdownLabel, laneCenterX, progressFraction, trackY } from './race-track.mjs';

test('progress is distance rowed over the race target distance', function () {
  assert.equal(progressFraction(0, 2000000), 0);
  assert.equal(progressFraction(500000, 2000000), 0.25);
  assert.equal(progressFraction(1000000, 2000000), 0.5);
  assert.equal(progressFraction(2000000, 2000000), 1);
});

test('progress is clamped to the course and tolerates missing data', function () {
  assert.equal(progressFraction(2500000, 2000000), 1, 'rowing past the finish stays on the line');
  assert.equal(progressFraction(-10, 2000000), 0);
  assert.equal(progressFraction(undefined, 2000000), 0);
  assert.equal(progressFraction(1000, 0), 0, 'an unknown target never divides by zero');
  assert.equal(progressFraction(1000, NaN), 0);
});

test('a rower moves the same share of the way from start line to finish line', function () {
  var startY = 600;
  var finishY = 40;
  assert.equal(trackY(0, startY, finishY), 600, 'at the start line before rowing');
  assert.equal(trackY(0.25, startY, finishY), 460);
  assert.equal(trackY(0.5, startY, finishY), 320);
  assert.equal(trackY(1, startY, finishY), 40, 'on the finish line at the target distance');
  assert.equal(trackY(progressFraction(500000, 2000000), startY, finishY), 460);
});

test('lanes split the track width evenly', function () {
  assert.deepEqual([0, 1, 2, 3].map(function (i) { return laneCenterX(i, 4, 400); }), [50, 150, 250, 350]);
  assert.equal(laneCenterX(0, 1, 300), 150);
});

test('the countdown shows whole seconds down to 1, then GO! instead of 0', function () {
  assert.equal(countdownLabel(10000), '10');
  assert.equal(countdownLabel(9001), '10', 'partial seconds round up');
  assert.equal(countdownLabel(9000), '9');
  assert.equal(countdownLabel(1), '1');
  assert.equal(countdownLabel(0), 'GO!', 'zero is shown as GO!, never 0');
  assert.equal(countdownLabel(-GO_DISPLAY_MS + 1), 'GO!');
  assert.equal(countdownLabel(-GO_DISPLAY_MS), '', 'GO! clears once the race is underway');
  assert.equal(countdownLabel(NaN), '');
});
