import test from 'node:test';
import assert from 'node:assert/strict';

import { formatDuration, formatPace, formatDistance, resultRows } from './race-results.mjs';

test('formatDuration formats finish time as m:ss.t', function () {
  assert.equal(formatDuration(95000), '1:35.0');
  assert.equal(formatDuration(95200), '1:35.2');
  assert.equal(formatDuration(15000), '0:15.0');
  assert.equal(formatDuration(0), '0:00.0');
  assert.equal(formatDuration(null), '');
  assert.equal(formatDuration(undefined), '');
  assert.equal(formatDuration(NaN), '');
  assert.equal(formatDuration(-100), '');
});

test('formatDistance formats distance in metres', function () {
  assert.equal(formatDistance(500), '500 m');
  assert.equal(formatDistance(2000), '2000 m');
  assert.equal(formatDistance(0), '0 m');
  assert.equal(formatDistance(null), '0 m');
  assert.equal(formatDistance(undefined), '0 m');
  assert.equal(formatDistance(NaN), '0 m');
});

test('formatPace shows pace per 500m as m:ss.t', function () {
  assert.equal(formatPace(95000), '1:35.0');
  assert.equal(formatPace(120000), '2:00.0');
  assert.equal(formatPace(132000), '2:12.0');
});

test('formatPace: pace never NaN or divide-by-zero at 0 distance (S8)', function () {
  assert.equal(formatPace(null), '—', 'null input returns em-dash');
  assert.equal(formatPace(0), '—', 'zero pace returns em-dash');
  assert.equal(formatPace(-5), '—', 'negative pace returns em-dash');
  assert.equal(formatPace('abc'), '—', 'non-numeric pace returns em-dash');
  assert.equal(formatPace(NaN), '—', 'NaN input returns em-dash');
  assert.equal(formatPace(undefined), '—', 'undefined returns em-dash');
  assert.equal(formatPace(95000, 0), '—', 'distance 0 returns em-dash, no divide-by-zero');
  assert.equal(formatPace(95000, null), '—', 'null distance returns em-dash');
  assert.equal(formatPace(95000, NaN), '—', 'NaN distance returns em-dash');
});

test('resultRows: a single racer ranked 1st (S9)', function () {
  var data = {
    results: [
      { user_id: 'u-1', name: 'Solo', rank: 1, status: 'finished', elapsed_milliseconds: 95000, distance_millimeters: 500000, pace_milliseconds_per_500m: 95000 }
    ]
  };
  var res = resultRows(data, 'u-other');
  assert.equal(res.rows.length, 1);
  assert.equal(res.rows[0].rank, 1);
  assert.equal(res.rows[0].name, 'Solo');
  assert.equal(res.rows[0].time, '1:35.0');
  assert.equal(res.rows[0].distance, '500 m');
  assert.equal(res.rows[0].pace, '1:35.0');
  assert.equal(res.rows[0].status, 'finished');
  assert.equal(res.rows[0].isSelf, false);
  assert.equal(res.message, null);
});

test('resultRows: numbers finishers 1..n with time as m:ss.t and distance in metres', function () {
  var data = {
    results: [
      { user_id: 'u-1', name: 'Winner', rank: 1, status: 'finished', elapsed_milliseconds: 95000, distance_millimeters: 500000, pace_milliseconds_per_500m: 95000 },
      { user_id: 'u-2', name: 'Second', rank: 2, status: 'finished', elapsed_milliseconds: 102400, distance_millimeters: 500000, pace_milliseconds_per_500m: 102400 }
    ]
  };
  var res = resultRows(data);
  assert.equal(res.rows.length, 2);
  assert.equal(res.rows[0].rank, 1);
  assert.equal(res.rows[0].time, '1:35.0');
  assert.equal(res.rows[0].distance, '500 m');
  assert.equal(res.rows[1].rank, 2);
  assert.equal(res.rows[1].time, '1:42.4');
  assert.equal(res.rows[1].distance, '500 m');
  assert.equal(res.message, null);
});

test('resultRows: DNF shown without time, after finishers, marked DNF with distance and pace (S4)', function () {
  var data = {
    results: [
      { user_id: 'u-1', name: 'Finisher', status: 'finished', elapsed_ms: 95000, distance_m: 500, pace_ms_per_500m: 95000 },
      { user_id: 'u-2', name: 'Quitter', status: 'dnf', elapsed_ms: null, distance_m: 350, pace_ms_per_500m: 120000 },
      { user_id: 'u-3', name: 'NoSamples', status: 'dnf', elapsed_ms: null, distance_m: 0, pace_ms_per_500m: null }
    ]
  };
  var res = resultRows(data);
  assert.equal(res.rows.length, 3);

  // Finisher
  assert.equal(res.rows[0].rank, 1);
  assert.equal(res.rows[0].time, '1:35.0');
  assert.equal(res.rows[0].distance, '500 m');
  assert.equal(res.rows[0].pace, '1:35.0');
  assert.equal(res.rows[0].status, 'finished');

  // DNF with distance
  assert.equal(res.rows[1].rank, null);
  assert.equal(res.rows[1].time, '');
  assert.equal(res.rows[1].distance, '350 m');
  assert.equal(res.rows[1].pace, '2:00.0');
  assert.equal(res.rows[1].status, 'dnf');

  // DNF with 0 distance and null pace
  assert.equal(res.rows[2].rank, null);
  assert.equal(res.rows[2].time, '');
  assert.equal(res.rows[2].distance, '0 m');
  assert.equal(res.rows[2].pace, '—', 'pace is — when null/zero');
  assert.equal(res.rows[2].status, 'dnf');

  assert.equal(res.message, null);
});

test('resultRows: all-DNF rows and "No one finished" message (S7)', function () {
  var data = {
    results: [
      { user_id: 'u-1', name: 'DNF1', status: 'dnf', distance_m: 200, pace_ms_per_500m: 110000 },
      { user_id: 'u-2', name: 'DNF2', status: 'dnf', distance_m: 100, pace_ms_per_500m: null }
    ]
  };
  var res = resultRows(data);
  assert.equal(res.rows.length, 2);
  assert.equal(res.rows[0].status, 'dnf');
  assert.equal(res.rows[0].rank, null);
  assert.equal(res.rows[0].time, '');
  assert.equal(res.rows[1].status, 'dnf');
  assert.equal(res.rows[1].rank, null);
  assert.equal(res.rows[1].time, '');
  assert.equal(res.message, 'No one finished');
});

test('resultRows: the self row highlighted (S10)', function () {
  var data = [
    { user_id: 'u-rival', name: 'Rival', status: 'finished', elapsed_ms: 90000, distance_m: 500 },
    { user_id: 'u-me', name: 'Myself', status: 'finished', elapsed_ms: 95000, distance_m: 500 }
  ];
  var res = resultRows(data, 'u-me');
  assert.equal(res.rows.length, 2);
  assert.equal(res.rows[0].isSelf, false);
  assert.equal(res.rows[1].isSelf, true);
});

test('formatDuration floors to tenths, pads seconds and carries minutes', function () {
  assert.equal(formatDuration(95999), '1:35.9');
  assert.equal(formatDuration(59999), '0:59.9');
  assert.equal(formatDuration(60000), '1:00.0');
  assert.equal(formatDuration(5100), '0:05.1');
  assert.equal(formatDuration(3600000), '60:00.0');
  assert.equal(formatDuration('abc'), '');
});

test('formatPace derives pace from elapsed and distance and rounds to the tenth', function () {
  assert.equal(formatPace(95000, 500), '1:35.0');
  assert.equal(formatPace(190000, 1000), '1:35.0');
  assert.equal(formatPace(95000, 500000), '1:35.0', 'millimetre distance is converted to metres');
  assert.equal(formatPace(0, 500), '—');
  assert.equal(formatPace(null, 500), '—');
  assert.equal(formatPace(NaN, 500), '—');
  assert.equal(formatPace(95000, -5), '—');
  assert.equal(formatPace(-95000), '—');
  assert.equal(formatPace(95400), '1:35.4');
  assert.equal(formatPace(95600), '1:35.6');
  assert.equal(formatPace(95600, 500), '1:35.6');
  assert.equal(formatPace(59600), '0:59.6');
  assert.equal(formatPace(5000), '0:05.0');
  assert.equal(formatPace(59960), '1:00.0');
  assert.equal(formatPace(95550), '1:35.6');
  assert.equal(formatPace(59.96), '1:00.0');
});

test('formatDistance rounds and rejects negative or non-numeric input', function () {
  assert.equal(formatDistance(499.6), '500 m');
  assert.equal(formatDistance(499.4), '499 m');
  assert.equal(formatDistance(-1), '0 m');
  assert.equal(formatDistance('abc'), '0 m');
});

test('resultRows: accepts a bare array and returns nothing for empty or missing data', function () {
  var res = resultRows([{ user_id: 'u-1', name: 'A', status: 'finished', elapsed_milliseconds: 60000, distance_m: 500, pace_milliseconds_per_500m: 60000 }]);
  assert.equal(res.rows.length, 1);
  assert.equal(res.length, 1);
  assert.equal(res.message, null);

  [null, undefined, {}, { results: 'x' }, { results: [] }, []].forEach(function (data) {
    var empty = resultRows(data);
    assert.equal(empty.rows.length, 0);
    assert.equal(empty.message, 'No one finished');
  });
});

test('resultRows: finishers keep server order numbered 1..n, and a server rank wins', function () {
  var res = resultRows({
    results: [
      { name: 'A', status: 'finished', elapsed_milliseconds: 60000, distance_m: 500 },
      { name: 'B', status: 'finished', elapsed_milliseconds: 70000, distance_m: 500 },
      { name: 'C', status: 'finished', rank: 7, elapsed_milliseconds: 80000, distance_m: 500 }
    ]
  });
  assert.deepEqual(res.rows.map(function (r) { return r.rank; }), [1, 2, 7]);
  assert.deepEqual(res.rows.map(function (r) { return r.name; }), ['A', 'B', 'C']);
});

test('resultRows: DNF rows are moved after finishers whatever the input order', function () {
  var res = resultRows({
    results: [
      { name: 'Out', status: 'dnf', distance_m: 100 },
      { name: 'Home', status: 'finished', elapsed_milliseconds: 60000, distance_m: 500 },
      { name: 'Out2', status: 'DNF', distance_m: 50 }
    ]
  });
  assert.deepEqual(res.rows.map(function (r) { return r.name; }), ['Home', 'Out', 'Out2']);
  assert.deepEqual(res.rows.map(function (r) { return r.status; }), ['finished', 'dnf', 'dnf']);
  assert.deepEqual(res.rows.map(function (r) { return r.rank; }), [1, null, null]);
  assert.equal(res.message, null);
});

test('resultRows: pace is computed from elapsed and distance when the server sent none', function () {
  var res = resultRows({
    results: [
      { name: 'A', status: 'finished', elapsed_milliseconds: 100000, distance_millimeters: 500000 },
      { name: 'B', status: 'dnf', elapsed_milliseconds: 50000, distance_m: 250 }
    ]
  });
  assert.equal(res.rows[0].pace, '1:40.0');
  assert.equal(res.rows[0].distance, '500 m');
  assert.equal(res.rows[1].pace, '1:40.0');
  assert.equal(res.rows[1].distance, '250 m');
});

test('resultRows: pace is — for null, zero or missing pace and no way to compute it', function () {
  var res = resultRows({
    results: [
      { name: 'A', status: 'finished', elapsed_milliseconds: 100000, distance_m: 500, pace_milliseconds_per_500m: 0 },
      { name: 'B', status: 'finished', elapsed_milliseconds: 100000, distance_m: 0, pace_milliseconds_per_500m: null },
      { name: 'C', status: 'dnf', distance_m: 0, pace_milliseconds_per_500m: 0 },
      { name: 'D', status: 'dnf', distance_m: 100, pace_milliseconds_per_500m: null }
    ]
  });
  res.rows.forEach(function (r) { assert.equal(r.pace, '—', r.name); });
});

test('resultRows: distance in millimetres, metres or the legacy field becomes metres', function () {
  var res = resultRows({
    results: [
      { name: 'mm', status: 'dnf', distance_millimeters: 350000 },
      { name: 'mm2', status: 'dnf', distance_mm: 120000 },
      { name: 'm', status: 'dnf', distance_m: 80 },
      { name: 'big', status: 'dnf', distance: 60000 },
      { name: 'small', status: 'dnf', distance: 400 },
      { name: 'none', status: 'dnf' },
      { name: 'fmm', status: 'finished', distance_millimeters: 500000, elapsed_ms: 1000 },
      { name: 'fm2', status: 'finished', distance_mm: 250000, elapsed_ms: 1000 },
      { name: 'fbig', status: 'finished', distance: 60000, elapsed_ms: 1000 },
      { name: 'fsmall', status: 'finished', distance: 400, elapsed_ms: 1000 },
      { name: 'fnone', status: 'finished', elapsed_ms: 1000 }
    ]
  });
  var byName = {};
  res.rows.forEach(function (r) { byName[r.name] = r.distance; });
  assert.deepEqual(byName, {
    fmm: '500 m', fm2: '250 m', fbig: '60 m', fsmall: '400 m', fnone: '0 m',
    mm: '350 m', mm2: '120 m', m: '80 m', big: '60 m', small: '400 m', none: '0 m'
  });
});

test('resultRows: elapsed and pace aliases are read', function () {
  var res = resultRows({
    results: [
      { name: 'a', status: 'finished', elapsed: 61000, distance_m: 500, pace_ms: 61000 },
      { name: 'b', status: 'finished', elapsed_ms: 62000, distance_m: 500, pace: 62000 },
      { name: 'c', status: 'finished', elapsed_milliseconds: 63000, distance_m: 500, pace_ms_per_500m: 63000 }
    ]
  });
  assert.deepEqual(res.rows.map(function (r) { return r.time; }), ['1:01.0', '1:02.0', '1:03.0']);
  assert.deepEqual(res.rows.map(function (r) { return r.pace; }), ['1:01.0', '1:02.0', '1:03.0']);
});

test('resultRows: name and user id aliases, and a default finished status', function () {
  var res = resultRows({
    results: [
      { displayName: 'Camel', userId: 'u-1', elapsed_ms: 1000, distance_m: 500 },
      { display_name: 'Snake', id: 'u-2', elapsed_ms: 1000, distance_m: 500 },
      { elapsed_ms: 1000, distance_m: 500 }
    ]
  });
  assert.deepEqual(res.rows.map(function (r) { return r.name; }), ['Camel', 'Snake', '']);
  assert.deepEqual(res.rows.map(function (r) { return r.userId; }), ['u-1', 'u-2', undefined]);
  assert.deepEqual(res.rows.map(function (r) { return r.status; }), ['finished', 'finished', 'finished']);
});

test("resultRows: only the viewer's own row is flagged, finisher or DNF, and nobody without a viewer", function () {
  var data = {
    results: [
      { user_id: 'u-a', name: 'A', status: 'finished', elapsed_ms: 1000, distance_m: 500 },
      { user_id: 'u-b', name: 'B', status: 'dnf', distance_m: 10 },
      { user_id: 'u-c', name: 'C', status: 'dnf', distance_m: 10 }
    ]
  };
  assert.deepEqual(resultRows(data, 'u-b').rows.map(function (r) { return r.isSelf; }), [false, true, false]);
  assert.deepEqual(resultRows(data, 'u-a').rows.map(function (r) { return r.isSelf; }), [true, false, false]);
  assert.deepEqual(resultRows(data, 'u-zzz').rows.map(function (r) { return r.isSelf; }), [false, false, false]);
  assert.deepEqual(resultRows(data).rows.map(function (r) { return r.isSelf; }), [false, false, false]);
  assert.deepEqual(resultRows(data, '').rows.map(function (r) { return r.isSelf; }), [false, false, false]);
});

test('resultRows: a DNF row never shows rank or finish time even if the server sent them', function () {
  var res = resultRows({ results: [{ user_id: 'u', name: 'X', status: 'dnf', rank: 3, elapsed_milliseconds: 90000, distance_m: 200, pace_milliseconds_per_500m: 120000 }] });
  assert.equal(res.rows[0].rank, null);
  assert.equal(res.rows[0].time, '');
  assert.equal(res.rows[0].status, 'dnf');
  assert.equal(res.rows[0].pace, '2:00.0');
});

test('resultRows: finisher with pace 95600 displays pace 1:35.6 (CLM-1)', function () {
  var res = resultRows({
    results: [
      { user_id: 'u-1', name: 'Racer', status: 'finished', elapsed_ms: 95600, distance_m: 500, pace_ms_per_500m: 95600 }
    ]
  });
  assert.equal(res.rows[0].pace, '1:35.6');
});

test('resultRows: every row pace has tenths or em-dash across multi-racer and solo time trial (CLM-3)', function () {
  var multi = resultRows({
    results: [
      { name: 'A', status: 'finished', elapsed_ms: 95600, distance_m: 500, pace_ms_per_500m: 95600 },
      { name: 'B', status: 'finished', elapsed_ms: 102400, distance_m: 500, pace_ms_per_500m: 102400 },
      { name: 'C', status: 'dnf', elapsed_ms: null, distance_m: 350, pace_ms_per_500m: 120000 },
      { name: 'D', status: 'dnf', elapsed_ms: null, distance_m: 0, pace_ms_per_500m: null }
    ]
  });
  assert.equal(multi.rows.length, 4);
  multi.rows.forEach(function (row) {
    assert.match(row.pace, /^\d+:\d{2}\.\d$|^—$/);
  });

  // Solo Time Trial single-row case asserting tenths
  var solo = resultRows({
    results: [
      { user_id: 'u-solo', name: 'Solo TT', status: 'finished', elapsed_ms: 95400, distance_m: 500, pace_ms_per_500m: 95400 }
    ]
  });
  assert.equal(solo.rows.length, 1);
  assert.equal(solo.rows[0].pace, '1:35.4');
  assert.match(solo.rows[0].pace, /^\d+:\d{2}\.\d$/);
});

test('resultRows: warmup rows show distance, time and pace, with no rank column and never the "No one finished" text (CLM-7, CLM-9)', function () {
  var data = {
    activityType: 'warmup',
    results: [
      { user_id: 'u-warmup', name: 'Warmup Rower', status: 'finished', elapsed_milliseconds: 300000, distance_millimeters: 1250000, pace_milliseconds_per_500m: 120000 }
    ]
  };
  var res = resultRows(data, 'u-warmup');
  assert.equal(res.rows.length, 1);
  assert.equal(res.hasRank, false, 'hasRank must be false for warmup');
  assert.equal(res.isWarmup, true, 'isWarmup must be true for warmup');
  assert.equal(res.activityType, 'warmup');
  assert.equal(res.rows[0].rank, null, 'rank must be null for warmup row');
  assert.equal(res.rows[0].name, 'Warmup Rower');
  assert.equal(res.rows[0].time, '5:00.0');
  assert.equal(res.rows[0].distance, '1250 m');
  assert.equal(res.rows[0].pace, '2:00.0');
  assert.equal(res.rows[0].isSelf, true);
  assert.equal(res.message, null, 'warmup must not have message');

  // Also when activityType is passed as 3rd parameter
  var resExplicitMode = resultRows({
    results: [
      { user_id: 'u-1', name: 'Rower', status: 'finished', elapsed_ms: 60000, distance_m: 250, pace_ms_per_500m: 120000 }
    ]
  }, 'u-other', 'warmup');
  assert.equal(resExplicitMode.hasRank, false);
  assert.equal(resExplicitMode.activityType, 'warmup');
  assert.equal(resExplicitMode.rows[0].rank, null);
  assert.equal(resExplicitMode.message, null);

  // Warmup with no finishers never shows "No one finished"
  var emptyWarmup = resultRows({ activityType: 'warmup', results: [] });
  assert.equal(emptyWarmup.message, null, 'empty warmup never shows "No one finished"');
  assert.notEqual(emptyWarmup.message, 'No one finished');
  assert.equal(emptyWarmup.activityType, 'warmup');

  // Race and time_trial resultRows have ranks 1..n and hasRank true
  var raceResults = resultRows({
    activityType: 'race',
    results: [
      { user_id: 'u-1', name: 'Racer 1', status: 'finished', elapsed_ms: 60000, distance_m: 500, pace_ms_per_500m: 60000 },
      { user_id: 'u-2', name: 'Racer 2', status: 'finished', elapsed_ms: 62000, distance_m: 500, pace_ms_per_500m: 62000 }
    ]
  });
  assert.equal(raceResults.hasRank, true, 'hasRank must be true for race');
  assert.equal(raceResults.isWarmup, false);
  assert.equal(raceResults.activityType, 'race');
  assert.equal(raceResults.rows[0].rank, 1);
  assert.equal(raceResults.rows[1].rank, 2);

  var ttResults = resultRows({
    activityType: 'time_trial',
    results: [
      { user_id: 'u-1', name: 'Solo TT', status: 'finished', elapsed_ms: 95400, distance_m: 500, pace_ms_per_500m: 95400 }
    ]
  });
  assert.equal(ttResults.hasRank, true, 'hasRank must be true for time_trial');
  assert.equal(ttResults.isWarmup, false);
  assert.equal(ttResults.activityType, 'time_trial');
  assert.equal(ttResults.rows[0].rank, 1);
});
