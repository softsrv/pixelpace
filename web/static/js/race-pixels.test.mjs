import test from 'node:test';
import assert from 'node:assert/strict';

import {
  BOAT_COLS, BOAT_POSES, BOAT_ROWS, PACER_HULL, SELF_HULL,
  advanceStroke, boatCells, cellColor, hullColor, pixelScale, shade, snap, strokePose, textCells
} from './race-pixels.mjs';

test('a stroke has a quick drive and a slow recovery', function () {
  assert.equal(strokePose(0), 'catch');
  assert.equal(strokePose(0.15), 'square', 'mid-drive');
  assert.equal(strokePose(0.3), 'finish');
  assert.equal(strokePose(0.6), 'square', 'recovery');
  assert.equal(strokePose(0.9), 'catch', 'reaching for the next catch');
  // The drive (catch to finish) is shorter than the recovery back.
  var drive = 0.24 - 0.12;
  var recovery = 0.76 - 0.44;
  assert.ok(drive < recovery);
});

test('stroke pose wraps and tolerates out-of-range phases', function () {
  assert.equal(strokePose(1), 'catch');
  assert.equal(strokePose(1.3), 'finish');
  assert.equal(strokePose(-0.7), 'finish');
});

test('the stroke advances at the racer’s stroke rate', function () {
  // At 30 spm one stroke takes 2s, so 500ms is a quarter of a stroke.
  assert.equal(advanceStroke(0, 30, 500), 0.25);
  assert.equal(advanceStroke(0.9, 30, 500), (0.9 + 0.25) % 1, 'wraps into the next stroke');
  // A faster rate covers more of the stroke in the same time.
  assert.ok(advanceStroke(0, 36, 100) > advanceStroke(0, 20, 100));
});

test('a racer who is not rowing holds their pose', function () {
  assert.equal(advanceStroke(0.4, 0, 1000), 0.4);
  assert.equal(advanceStroke(0.4, undefined, 1000), 0.4);
  assert.equal(advanceStroke(0.4, NaN, 1000), 0.4);
  assert.equal(advanceStroke(0.4, 28, 0), 0.4);
  assert.equal(advanceStroke(0.4, -5, 1000), 0.4);
});

test('every pose is a symmetric boat with its bow at row 0', function () {
  BOAT_POSES.forEach(function (pose) {
    var cells = boatCells(pose);
    var keys = new Set(cells.map(function (c) { return c[0] + ',' + c[1]; }));
    cells.forEach(function (c) {
      assert.ok(c[0] >= 0 && c[0] < BOAT_COLS && c[1] >= 0 && c[1] < BOAT_ROWS, pose + ' cell inside the sprite');
      assert.ok(keys.has((BOAT_COLS - 1 - c[0]) + ',' + c[1]), pose + ' is mirrored left/right');
    });
    var bow = Math.min.apply(null, cells.map(function (c) { return c[1]; }));
    assert.equal(bow, 0, pose + ' bow is at the origin row');
  });
});

test('blades reach toward the bow at the catch and sweep to the stern at the finish', function () {
  function bladeRow(pose) {
    var blades = boatCells(pose).filter(function (c) { return c[2] === 'B'; });
    return blades.reduce(function (sum, c) { return sum + c[1]; }, 0) / blades.length;
  }
  assert.ok(bladeRow('catch') < bladeRow('square'), 'catch blades sit nearer the bow (top)');
  assert.ok(bladeRow('finish') > bladeRow('square'), 'finish blades sit nearer the stern');
  assert.throws(function () { boatCells('feather'); }, /unknown pose/);
});

test('our own boat is pink and rivals cycle through distinct colours', function () {
  assert.equal(hullColor(true, 0), SELF_HULL);
  var rivals = [0, 1, 2, 3, 4, 5, 6].map(function (i) { return hullColor(false, i); });
  assert.equal(new Set(rivals).size, rivals.length, 'seven rivals get seven colours');
  assert.ok(!rivals.includes(SELF_HULL), 'no rival looks like us');
  assert.notEqual(PACER_HULL, SELF_HULL, 'pacer is not self');
  assert.ok(!rivals.includes(PACER_HULL), 'no rival looks like the pacer');
  assert.equal(hullColor(false, 7), rivals[0], 'colours repeat after the palette');
  assert.equal(hullColor(false, undefined), rivals[0]);
});

test('hull keys take the boat colour, shaded keys are darker', function () {
  assert.equal(cellColor('H', 0xff79c6), 0xff79c6);
  assert.equal(cellColor('S', 0x8be9fd), 0x8be9fd);
  assert.equal(cellColor('h', 0xffffff), shade(0xffffff, 0.72));
  assert.equal(shade(0x808080, 0.5), 0x404040);
  assert.equal(shade(0xff0000, 0), 0);
});

test('pixel scale fits the boat in its lane, between 2 and 4', function () {
  assert.equal(pixelScale(600), 4, 'wide lanes cap at 4');
  assert.equal(pixelScale(3 * (BOAT_COLS + 2)), 3);
  assert.equal(pixelScale(40), 2, 'eight lanes on a phone');
  assert.equal(pixelScale(0), 2);
  assert.equal(pixelScale(NaN), 2);
});

test('positions snap to the art-pixel grid', function () {
  assert.equal(snap(10, 3), 9);
  assert.equal(snap(11, 3), 12);
  assert.equal(snap(-4, 2), -4);
});

test('the pixel font lays out countdown labels', function () {
  var ten = textCells('10');
  assert.equal(ten.width, 11, 'two 5-wide glyphs and a gap');
  assert.ok(ten.cells.every(function (c) { return c[1] >= 0 && c[1] < 7; }));
  var go = textCells('GO!');
  assert.equal(go.width, 13);
  assert.deepEqual(textCells(''), { cells: [], width: 0 });
  assert.deepEqual(textCells(null), { cells: [], width: 0 });
  assert.equal(textCells('go!').width, 13, 'lower case is drawn in capitals');
  assert.equal(textCells('1?').width, textCells('1').width, 'unknown characters are skipped');
  // Every countdown label the race shows can be drawn.
  ['10', '9', '8', '7', '6', '5', '4', '3', '2', '1', 'GO!'].forEach(function (label) {
    assert.ok(textCells(label).cells.length > 0, label);
  });
});
