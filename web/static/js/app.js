// ── Password visibility toggle ────────────────────────────────────────────────

document.addEventListener('click', function(evt) {
  var btn = evt.target.closest('[data-toggle-pwd]');
  if (!btn) return;
  var input = document.getElementById(btn.getAttribute('data-toggle-pwd'));
  if (!input) return;
  var show = input.type === 'password';
  input.type = show ? 'text' : 'password';
  btn.querySelector('.eye').classList.toggle('hidden', show);
  btn.querySelector('.eye-off').classList.toggle('hidden', !show);
});

// ── Copy, dialogs and HTMX errors ─────────────────────────────────────────────

// <button data-copy-target="#input"> copies that input's value.
document.addEventListener('click', function (evt) {
  var btn = evt.target.closest && evt.target.closest('[data-copy-target]');
  if (!btn) return;
  var source = document.querySelector(btn.getAttribute('data-copy-target'));
  if (!source || !navigator.clipboard) return;
  navigator.clipboard.writeText(source.value || source.textContent).then(function () {
    var label = btn.textContent;
    btn.textContent = 'Copied!';
    setTimeout(function () { btn.textContent = label; }, 1500);
  }, function () {
    if (source.select) source.select();
  });
});

// <button data-dialog-open="#dialog"> opens that <dialog> as a modal.
document.addEventListener('click', function (evt) {
  var btn = evt.target.closest && evt.target.closest('[data-dialog-open]');
  if (!btn) return;
  var dialog = document.querySelector(btn.getAttribute('data-dialog-open'));
  if (dialog && dialog.showModal) dialog.showModal();
});

// HTMX actions marked with data-error-target show data-error-message there when
// the server refuses them, and clear it when they are tried again.
function htmxErrorTarget(evt) {
  var source = evt.target && evt.target.closest && evt.target.closest('[data-error-target]');
  if (!source) return null;
  return { source: source, target: document.querySelector(source.getAttribute('data-error-target')) };
}
document.addEventListener('htmx:before:request', function (evt) {
  var found = htmxErrorTarget(evt);
  if (found && found.target) found.target.textContent = '';
});
document.addEventListener('htmx:response:error', function (evt) {
  var found = htmxErrorTarget(evt);
  if (found && found.target) found.target.textContent = found.source.getAttribute('data-error-message') || 'Something went wrong.';
});

// ── Token expiry handling ─────────────────────────────────────────────────────

// When the server fires the token-expired event, attempt a silent refresh.
// If the refresh succeeds, replay the original request.
// If it fails, redirect to login.
document.body.addEventListener('token-expired', async function () {
  try {
    var res = await fetch('/auth/refresh', { method: 'POST' });
    if (res.ok) {
      // Replay by re-triggering HTMX on the active element.
      var active = document.querySelector('[hx-trigger]');
      if (active) htmx.trigger(active, 'retry');
    } else {
      window.location.href = '/login';
    }
  } catch (_) {
    window.location.href = '/login';
  }
});

// ── Concept2 Bluetooth connection ─────────────────────────────────────────────

(function () {
  var connectBtn = document.getElementById('bt-connect-btn');
  if (!connectBtn) return;

  var rowingMachineCard = document.getElementById('rowing-machine-card');
  var devMode = rowingMachineCard && rowingMachineCard.getAttribute('data-dev-mode') === 'true';
  var connectionInfo = document.getElementById('bt-connection-info');
  var metricsRegion = document.getElementById('bt-metrics');
  var rowerCanvas = document.getElementById('bt-rower-canvas');
  var rowerCtx = rowerCanvas && rowerCanvas.getContext ? rowerCanvas.getContext('2d') : null;
  var currentStrokeRate = 0;
  var rowerTimer = null;
  var rowerTimerIntervalMs = null;
  var currentRowerFrame = 0;
  // The PM5 advertises the Discovery service in its BLE advertisement packet, so
  // requestDevice must filter on that. The Rowing service (real-time data) is only
  // reachable via GATT after connecting, so it must be listed in optionalServices.
  var concept2DiscoveryServiceUuid = 'ce060000-43e5-11e4-916c-0800200c9a66';
  var concept2RowingServiceUuid = 'ce060030-43e5-11e4-916c-0800200c9a66';
  var PM5_GENERAL_STATUS_ELAPSED_TIME_OFFSET = 0;
  var PM5_GENERAL_STATUS_ELAPSED_TIME_SCALE_SECONDS = 0.01;
  var PM5_GENERAL_STATUS_DISTANCE_OFFSET = 3;
  var PM5_GENERAL_STATUS_DISTANCE_SCALE_METERS = 0.1;
  var PM5_ADDITIONAL_STATUS_1_ELAPSED_TIME_OFFSET = 0;
  var PM5_ADDITIONAL_STATUS_1_ELAPSED_TIME_SCALE_SECONDS = 0.01;
  var PM5_ADDITIONAL_STATUS_1_STROKE_RATE_OFFSET = 5;
  var PM5_ADDITIONAL_STATUS_1_HEART_RATE_OFFSET = 6;
  var PM5_ADDITIONAL_STATUS_1_HEART_RATE_INVALID = 255;
  var PM5_ADDITIONAL_STATUS_1_CURRENT_PACE_OFFSET = 7;
  var PM5_ADDITIONAL_STATUS_1_CURRENT_PACE_SCALE_SECONDS = 0.01;
  var PM5_ADDITIONAL_STATUS_2_ELAPSED_TIME_OFFSET = 0;
  var PM5_ADDITIONAL_STATUS_2_ELAPSED_TIME_SCALE_SECONDS = 0.01;
  var PM5_ADDITIONAL_STATUS_2_TOTAL_CALORIES_OFFSET = 6;
  var PM5_STROKE_DATA_ELAPSED_TIME_OFFSET = 0;
  var PM5_STROKE_DATA_ELAPSED_TIME_SCALE_SECONDS = 0.01;
  var PM5_STROKE_DATA_STROKE_COUNT_OFFSET = 18;
  var PM5_ADDITIONAL_STROKE_DATA_STROKE_POWER_OFFSET = 3;
  var PM5_ADDITIONAL_STROKE_DATA_STROKE_COUNT_OFFSET = 7;
  var SPM_INTERVAL_LOW = 16;
  var SPM_INTERVAL_HIGH = 40;
  var SPM_INTERVAL_SLOW_MS = 1000;
  var SPM_INTERVAL_FAST_MS = 200;
  var pm5Characteristics = {
    // Concept2 PM5 Bluetooth Smart Interface Definition: Rowing General Status.
    generalStatus: {
      uuid: 'ce060031-43e5-11e4-916c-0800200c9a66',
      elapsedTimeOffset: PM5_GENERAL_STATUS_ELAPSED_TIME_OFFSET,
      elapsedTimeScaleSeconds: PM5_GENERAL_STATUS_ELAPSED_TIME_SCALE_SECONDS,
      distanceOffset: PM5_GENERAL_STATUS_DISTANCE_OFFSET,
      distanceScaleMeters: PM5_GENERAL_STATUS_DISTANCE_SCALE_METERS
    },
    // Concept2 PM5 Bluetooth Smart Interface Definition: Rowing Additional Status 1.
    additionalStatus1: {
      uuid: 'ce060032-43e5-11e4-916c-0800200c9a66',
      elapsedTimeOffset: PM5_ADDITIONAL_STATUS_1_ELAPSED_TIME_OFFSET,
      elapsedTimeScaleSeconds: PM5_ADDITIONAL_STATUS_1_ELAPSED_TIME_SCALE_SECONDS,
      strokeRateOffset: PM5_ADDITIONAL_STATUS_1_STROKE_RATE_OFFSET,
      heartRateOffset: PM5_ADDITIONAL_STATUS_1_HEART_RATE_OFFSET,
      heartRateInvalid: PM5_ADDITIONAL_STATUS_1_HEART_RATE_INVALID,
      currentPaceOffset: PM5_ADDITIONAL_STATUS_1_CURRENT_PACE_OFFSET,
      currentPaceScaleSeconds: PM5_ADDITIONAL_STATUS_1_CURRENT_PACE_SCALE_SECONDS
    },
    // Concept2 PM5 Bluetooth Smart Interface Definition: Rowing Additional Status 2.
    additionalStatus2: {
      uuid: 'ce060033-43e5-11e4-916c-0800200c9a66',
      elapsedTimeOffset: PM5_ADDITIONAL_STATUS_2_ELAPSED_TIME_OFFSET,
      elapsedTimeScaleSeconds: PM5_ADDITIONAL_STATUS_2_ELAPSED_TIME_SCALE_SECONDS,
      totalCaloriesOffset: PM5_ADDITIONAL_STATUS_2_TOTAL_CALORIES_OFFSET
    },
    // Concept2 PM5 Bluetooth Smart Interface Definition: Rowing Stroke Data.
    strokeData: {
      uuid: 'ce060035-43e5-11e4-916c-0800200c9a66',
      elapsedTimeOffset: PM5_STROKE_DATA_ELAPSED_TIME_OFFSET,
      elapsedTimeScaleSeconds: PM5_STROKE_DATA_ELAPSED_TIME_SCALE_SECONDS,
      strokeCountOffset: PM5_STROKE_DATA_STROKE_COUNT_OFFSET
    },
    // Concept2 PM5 Bluetooth Smart Interface Definition: Rowing Additional Stroke Data.
    additionalStrokeData: {
      uuid: 'ce060036-43e5-11e4-916c-0800200c9a66',
      strokePowerOffset: PM5_ADDITIONAL_STROKE_DATA_STROKE_POWER_OFFSET,
      strokeCountOffset: PM5_ADDITIONAL_STROKE_DATA_STROKE_COUNT_OFFSET
    }
  };
  var pm5CharacteristicsByUuid = {};
  Object.keys(pm5Characteristics).forEach(function (key) {
    pm5CharacteristicsByUuid[pm5Characteristics[key].uuid] = pm5Characteristics[key];
  });
  var zeroMetrics = {
    elapsedTime: 0,
    distance: 0,
    pace: 0,
    strokeRate: 0,
    power: 0,
    strokeCount: 0,
    calories: 0,
    heartRate: 0
  };
  // Dev-mode PM5 simulator settings: the defaults the panel starts at and the
  // bounds its inputs are clamped to. Pace is seconds per 500 m.
  var mockPm5Config = {
    strokeRate: 26,
    pace: 132,
    minStrokeRate: 0,
    maxStrokeRate: 60,
    minPace: 60,
    maxPace: 300,
    statusIntervalMs: 500
  };
  // The simulator's two timers: status characteristics every statusIntervalMs,
  // stroke characteristics once per stroke (60000 / SPM ms).
  var mockPm5StatusTimer = null;
  var mockPm5StrokeTimer = null;
  var activeRaceStream = null;
  // PM5 characteristics report partial updates; retain the latest known readings.
  var streamMetrics = { elapsedTime: 0, distance: 0, strokeRate: 0, power: 0 };
  // A race measures from its own start: the readings at that moment are
  // subtracted from every streamed sample, so a machine that was already
  // rowing (or the dev mock) starts the race at zero.
  var raceBaseline = { elapsedTime: 0, distance: 0 };
  var pm5Connected = false;

  document.addEventListener('pixelpace:race-started', function () {
    raceBaseline = { elapsedTime: streamMetrics.elapsedTime, distance: streamMetrics.distance };
  });

  // The race page waits for live rowing data before reporting its racer ready.
  function setPm5Connected(connected) {
    if (pm5Connected === connected) return;
    pm5Connected = connected;
    document.dispatchEvent(new CustomEvent('pixelpace:pm5-status', { detail: { connected: connected } }));
  }
  window.isPm5Connected = function () { return pm5Connected; };

  function startRaceStream(raceId) {
    if (!raceId) throw new Error('A race id is required');
    raceId = String(raceId);
    if (activeRaceStream && activeRaceStream.raceId === raceId) return activeRaceStream;
    if (activeRaceStream) activeRaceStream.stop();

    // roster and racers are prototype-less so ids like '__proto__' are plain keys.
    var stream = {
      raceId: raceId, buffer: [], socket: null, stop: stop,
      roster: Object.create(null), racers: Object.create(null), nameFor: nameFor
    };
    var retryTimer = null;
    var retryDelay = 1000;
    var protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    var url = protocol + '//' + window.location.host + '/races/' + encodeURIComponent(raceId) + '/ws';
    activeRaceStream = stream;

    function stop() {
      if (activeRaceStream !== stream) return;
      activeRaceStream = null;
      if (retryTimer !== null) clearTimeout(retryTimer);
      if (stream.socket) stream.socket.close();
    }

    // Names come only from the server's roster frame, never from an email.
    function nameFor(userId) {
      return typeof userId === 'string' && userId in stream.roster ? stream.roster[userId] : null;
    }

    function renderRoster() {
      var el = document.getElementById('race-roster');
      if (!el) return;
      var names = Object.keys(stream.roster).map(function (id) { return stream.roster[id]; });
      el.textContent = names.length ? 'Racers: ' + names.join(', ') : '';
      if (el.classList) el.classList[names.length ? 'remove' : 'add']('hidden');
    }

    function handleMessage(data) {
      var frame;
      try {
        frame = JSON.parse(data);
      } catch (_) {
        return;
      }
      if (!frame || typeof frame !== 'object' || Array.isArray(frame)) return;
      if (frame.type === 'roster') {
        if (!Array.isArray(frame.players)) return;
        var roster = Object.create(null);
        frame.players.forEach(function (player) {
          if (player && typeof player.user_id === 'string' && typeof player.name === 'string') {
            roster[player.user_id] = player.name;
          }
        });
        stream.roster = roster;
        Object.keys(stream.racers).forEach(function (id) {
          stream.racers[id].name = nameFor(id);
        });
        renderRoster();
        return;
      }
      if (typeof frame.user_id !== 'string') return;
      var racer = Object.create(null);
      Object.keys(frame).forEach(function (key) { racer[key] = frame[key]; });
      racer.name = nameFor(frame.user_id);
      stream.racers[frame.user_id] = racer;
      // Every participant's stored sample, including our own, as broadcast by the server.
      document.dispatchEvent(new CustomEvent('pixelpace:race-telemetry', { detail: racer }));
    }

    function retry() {
      if (activeRaceStream !== stream || retryTimer !== null) return;
      retryTimer = setTimeout(function () {
        retryTimer = null;
        connect();
      }, retryDelay);
      retryDelay = Math.min(retryDelay * 2, 30000);
    }

    function connect() {
      if (activeRaceStream !== stream) return;
      var socket;
      try {
        socket = new WebSocket(url);
      } catch (_) {
        retry();
        return;
      }
      stream.socket = socket;
      socket.onopen = function () {
        if (activeRaceStream !== stream || stream.socket !== socket) return;
        retryDelay = 1000;
        flushRaceStream(stream);
      };
      socket.onmessage = function (event) {
        if (activeRaceStream !== stream || stream.socket !== socket) return;
        handleMessage(event && event.data);
      };
      socket.onclose = function () {
        if (stream.socket === socket) retry();
      };
      socket.onerror = function () {
        // Closing leads to onclose and a client-owned reconnect attempt.
        socket.close();
      };
    }

    connect();
    return stream;
  }

  function flushRaceStream(stream) {
    var socket = stream.socket;
    if (!socket || socket.readyState !== WebSocket.OPEN) return;
    var sent = 0;
    try {
      // One synchronous replay action, one sample per frame as required by the server.
      for (; sent < stream.buffer.length; sent++) {
        if (socket.readyState !== WebSocket.OPEN) break;
        socket.send(JSON.stringify(stream.buffer[sent]));
      }
    } catch (_) {
      // Keep the unsent suffix (and its timestamps) for the next connection.
      socket.close();
    } finally {
      stream.buffer.splice(0, sent);
    }
  }

  function streamRaceMetrics(metrics) {
    Object.keys(streamMetrics).forEach(function (key) {
      if (typeof metrics[key] === 'number') streamMetrics[key] = metrics[key];
    });
    if (!activeRaceStream || Object.keys(metrics).length === 0) return;
    var sample = {
      elapsed_milliseconds: Math.max(0, Math.round((streamMetrics.elapsedTime - raceBaseline.elapsedTime) * 1000)),
      distance_millimeters: Math.max(0, Math.round((streamMetrics.distance - raceBaseline.distance) * 1000)),
      stroke_rate: Math.round(streamMetrics.strokeRate),
      power: Math.round(streamMetrics.power),
      sampled_at: new Date().toISOString()
    };
    activeRaceStream.buffer.push(sample);
    // Lets the race view move our own rower without waiting for the server echo.
    document.dispatchEvent(new CustomEvent('pixelpace:local-telemetry', { detail: sample }));
    flushRaceStream(activeRaceStream);
  }

  // The race owner supplies the id; the returned handle exposes the replay buffer.
  window.startRaceStream = startRaceStream;

  function showConnectionInfo(message) {
    if (!connectionInfo) return;
    connectionInfo.textContent = message;
    connectionInfo.classList.remove('hidden');
  }

  // Keep this logic byte-identical to web/static/js/spm-interval.mjs, where it is unit-tested.
  function spmToIntervalMs(strokeRate) {
    var spm = Number(strokeRate) || 0;
    if (spm <= SPM_INTERVAL_LOW) return SPM_INTERVAL_SLOW_MS;
    if (spm >= SPM_INTERVAL_HIGH) return SPM_INTERVAL_FAST_MS;

    return Math.round(SPM_INTERVAL_SLOW_MS - ((spm - SPM_INTERVAL_LOW) / (SPM_INTERVAL_HIGH - SPM_INTERVAL_LOW)) * (SPM_INTERVAL_SLOW_MS - SPM_INTERVAL_FAST_MS));
  }

  function drawBoat() {
    if (!rowerCtx || !rowerCanvas) return;

    rowerCtx.fillStyle = '#4b2f1f';
    rowerCtx.beginPath();
    rowerCtx.moveTo(45, 124);
    rowerCtx.lineTo(260, 124);
    rowerCtx.lineTo(232, 148);
    rowerCtx.lineTo(78, 148);
    rowerCtx.closePath();
    rowerCtx.fill();

    rowerCtx.strokeStyle = '#f9fafb';
    rowerCtx.lineWidth = 3;
    rowerCtx.beginPath();
    rowerCtx.moveTo(95, 120);
    rowerCtx.lineTo(214, 120);
    rowerCtx.stroke();
  }

  function drawRowerFrame(frame) {
    if (!rowerCtx || !rowerCanvas) return;

    rowerCtx.fillStyle = '#1e63c9';
    rowerCtx.fillRect(0, 0, rowerCanvas.width, rowerCanvas.height);
    drawBoat();

    rowerCtx.strokeStyle = '#facc15';
    rowerCtx.fillStyle = '#facc15';
    rowerCtx.lineWidth = 5;
    rowerCtx.lineCap = 'round';
    rowerCtx.lineJoin = 'round';

    if (frame === 1) {
      rowerCtx.beginPath();
      rowerCtx.arc(150, 76, 10, 0, Math.PI * 2);
      rowerCtx.fill();
      rowerCtx.beginPath();
      rowerCtx.moveTo(150, 88);
      rowerCtx.lineTo(132, 112);
      rowerCtx.lineTo(116, 128);
      rowerCtx.moveTo(132, 112);
      rowerCtx.lineTo(158, 128);
      rowerCtx.moveTo(140, 96);
      rowerCtx.lineTo(102, 92);
      rowerCtx.moveTo(140, 96);
      rowerCtx.lineTo(112, 104);
      rowerCtx.stroke();

      rowerCtx.strokeStyle = '#e5e7eb';
      rowerCtx.lineWidth = 4;
      rowerCtx.beginPath();
      rowerCtx.moveTo(102, 92);
      rowerCtx.lineTo(54, 78);
      rowerCtx.moveTo(112, 104);
      rowerCtx.lineTo(65, 126);
      rowerCtx.stroke();
      return;
    }

    rowerCtx.beginPath();
    rowerCtx.arc(174, 76, 10, 0, Math.PI * 2);
    rowerCtx.fill();
    rowerCtx.beginPath();
    rowerCtx.moveTo(170, 88);
    rowerCtx.lineTo(184, 112);
    rowerCtx.lineTo(208, 128);
    rowerCtx.moveTo(184, 112);
    rowerCtx.lineTo(156, 128);
    rowerCtx.moveTo(176, 96);
    rowerCtx.lineTo(218, 90);
    rowerCtx.moveTo(176, 96);
    rowerCtx.lineTo(208, 106);
    rowerCtx.stroke();

    rowerCtx.strokeStyle = '#e5e7eb';
    rowerCtx.lineWidth = 4;
    rowerCtx.beginPath();
    rowerCtx.moveTo(218, 90);
    rowerCtx.lineTo(270, 72);
    rowerCtx.moveTo(208, 106);
    rowerCtx.lineTo(252, 132);
    rowerCtx.stroke();
  }

  function drawRestingRower() {
    currentRowerFrame = 0;
    drawRowerFrame(currentRowerFrame);
  }

  function revealRowerCanvas() {
    if (!rowerCanvas) return;
    rowerCanvas.classList.remove('hidden');
    drawRestingRower();
  }

  function hideRowerCanvas() {
    if (!rowerCanvas) return;
    rowerCanvas.classList.add('hidden');
  }

  function stopRowerAnimation() {
    if (rowerTimer) clearInterval(rowerTimer);
    rowerTimer = null;
    rowerTimerIntervalMs = null;
  }

  function startRowerAnimation(intervalMs) {
    stopRowerAnimation();
    rowerTimerIntervalMs = intervalMs;
    rowerTimer = setInterval(function () {
      currentRowerFrame = currentRowerFrame === 0 ? 1 : 0;
      drawRowerFrame(currentRowerFrame);
    }, rowerTimerIntervalMs);
  }

  function syncRowerAnimation(strokeRate) {
    currentStrokeRate = Math.max(0, Number(strokeRate) || 0);
    if (!rowerCtx || !rowerCanvas) return;

    if (currentStrokeRate <= 0) {
      stopRowerAnimation();
      drawRestingRower();
      return;
    }

    var intervalMs = spmToIntervalMs(currentStrokeRate);
    if (rowerTimer && rowerTimerIntervalMs === intervalMs) return;
    startRowerAnimation(intervalMs);
  }

  function u24le(dv, offset) {
    return dv.getUint8(offset) | (dv.getUint8(offset + 1) << 8) | (dv.getUint8(offset + 2) << 16);
  }

  function hasBytes(dv, offset, length) {
    return dv && dv.byteLength >= offset + length;
  }

  function formatDuration(totalSeconds) {
    var seconds = Math.max(0, Math.floor(totalSeconds || 0));
    var h = Math.floor(seconds / 3600);
    var m = Math.floor((seconds % 3600) / 60);
    var s = seconds % 60;

    if (h > 0) {
      return h + ':' + String(m).padStart(2, '0') + ':' + String(s).padStart(2, '0');
    }

    return m + ':' + String(s).padStart(2, '0');
  }

  function metricEl(key) {
    if (!metricsRegion) return null;
    return metricsRegion.querySelector('[data-metric="' + key + '"]');
  }

  function renderMetric(key, value) {
    var el = metricEl(key);
    if (!el) return;

    if (key === 'elapsedTime') el.textContent = formatDuration(value);
    if (key === 'distance') el.textContent = Math.round(value || 0) + ' m';
    if (key === 'pace') el.textContent = formatDuration(value) + ' /500m';
    if (key === 'strokeRate') el.textContent = Math.round(value || 0) + ' spm';
    if (key === 'power') el.textContent = Math.round(value || 0) + ' W';
    if (key === 'strokeCount') el.textContent = Math.round(value || 0);
    if (key === 'calories') el.textContent = Math.round(value || 0) + ' kcal';
    if (key === 'heartRate') el.textContent = Math.round(value || 0) + ' bpm';
  }

  function renderMetrics(metrics) {
    Object.keys(metrics).forEach(function (key) {
      renderMetric(key, metrics[key]);
    });
  }

  function showZeroMetrics() {
    if (metricsRegion) metricsRegion.classList.remove('hidden');
    renderMetrics(zeroMetrics);
  }

  // DEV_MOCK_PM5_START
  // A development-only PM5 simulator. It rows at the panel's stroke rate and
  // pace, encodes its readings as Concept2 PM5 Bluetooth notification payloads
  // and feeds them to onNotify: the same decode, render, animation and race
  // stream path a real rowing machine's notifications take.
  var mockPm5State = {
    running: true,
    strokeRate: mockPm5Config.strokeRate,
    pace: mockPm5Config.pace,
    elapsedTime: 0,
    distance: 0,
    strokeCount: 0,
    calories: 0,
    work: 0,
    strokeTimerMs: null
  };
  var mockPm5PanelStarted = false;

  function mockPm5Rowing() {
    return mockPm5State.running && mockPm5State.strokeRate > 0;
  }

  // Concept2's pace-to-watts relation: watts = 2.80 / (seconds per metre)^3.
  // Calories accumulate at (4 x watts + 350) kcal per hour.
  function mockPm5Power() {
    if (!mockPm5Rowing()) return 0;
    return 2.8 / Math.pow(mockPm5State.pace / 500, 3);
  }

  function mockPm5Uint(value, max) {
    var n = Math.round(Number(value));
    if (!Number.isFinite(n) || n < 0) return 0;
    return n > max ? max : n;
  }

  function putU8(dv, offset, value) {
    dv.setUint8(offset, mockPm5Uint(value, 0xff));
  }

  function putU16(dv, offset, value) {
    dv.setUint16(offset, mockPm5Uint(value, 0xffff), true);
  }

  function putU24(dv, offset, value) {
    var n = mockPm5Uint(value, 0xffffff);
    dv.setUint8(offset, n & 0xff);
    dv.setUint8(offset + 1, (n >> 8) & 0xff);
    dv.setUint8(offset + 2, (n >> 16) & 0xff);
  }

  function mockPm5AveragePace() {
    if (mockPm5State.distance <= 0) return 0;
    return mockPm5State.elapsedTime / mockPm5State.distance * 500;
  }

  function mockPm5AveragePower() {
    if (mockPm5State.elapsedTime <= 0) return 0;
    return mockPm5State.work / mockPm5State.elapsedTime;
  }

  // Payloads follow the Concept2 PM5 Bluetooth Smart Interface Definition.
  function mockPm5GeneralStatus() {
    var dv = new DataView(new ArrayBuffer(19));
    putU24(dv, 0, mockPm5State.elapsedTime * 100);
    putU24(dv, 3, mockPm5State.distance * 10);
    putU8(dv, 6, 1); // workout type: just row, splits
    putU8(dv, 7, 255); // interval type: none
    putU8(dv, 8, 1); // workout state: workout row
    putU8(dv, 9, mockPm5Rowing() ? 1 : 0); // rowing state
    putU8(dv, 10, 0); // stroke state
    putU24(dv, 11, Math.floor(mockPm5State.distance)); // total work distance, m
    putU24(dv, 14, 0); // workout duration
    putU8(dv, 17, 0); // workout duration type
    putU8(dv, 18, 120); // drag factor
    return dv;
  }

  function mockPm5AdditionalStatus1() {
    var rowing = mockPm5Rowing();
    var dv = new DataView(new ArrayBuffer(17));
    putU24(dv, 0, mockPm5State.elapsedTime * 100);
    putU16(dv, 3, rowing ? 500 / mockPm5State.pace * 1000 : 0); // speed, 0.001 m/s
    putU8(dv, PM5_ADDITIONAL_STATUS_1_STROKE_RATE_OFFSET, rowing ? mockPm5State.strokeRate : 0);
    putU8(dv, PM5_ADDITIONAL_STATUS_1_HEART_RATE_OFFSET, PM5_ADDITIONAL_STATUS_1_HEART_RATE_INVALID);
    putU16(dv, PM5_ADDITIONAL_STATUS_1_CURRENT_PACE_OFFSET, rowing ? mockPm5State.pace * 100 : 0);
    putU16(dv, 9, mockPm5AveragePace() * 100);
    putU16(dv, 11, 0); // rest distance
    putU24(dv, 13, 0); // rest time
    putU8(dv, 16, 0); // erg machine type
    return dv;
  }

  function mockPm5AdditionalStatus2() {
    var dv = new DataView(new ArrayBuffer(20));
    putU24(dv, 0, mockPm5State.elapsedTime * 100);
    putU8(dv, 3, 0); // interval count
    putU16(dv, 4, mockPm5AveragePower());
    putU16(dv, PM5_ADDITIONAL_STATUS_2_TOTAL_CALORIES_OFFSET, mockPm5State.calories);
    putU16(dv, 8, mockPm5AveragePace() * 100); // split average pace
    putU16(dv, 10, mockPm5AveragePower()); // split average power
    putU16(dv, 12, mockPm5State.elapsedTime > 0 ? mockPm5State.calories * 3600 / mockPm5State.elapsedTime : 0);
    putU24(dv, 14, 0); // last split time
    putU24(dv, 17, 0); // last split distance
    return dv;
  }

  function mockPm5StrokeData(strokeSeconds, power) {
    var driveSeconds = Math.min(0.8, strokeSeconds / 2);
    var driveLength = 1.4;
    var work = power * strokeSeconds;
    var averageForceLbs = work / driveLength / 4.448;
    var dv = new DataView(new ArrayBuffer(20));
    putU24(dv, 0, mockPm5State.elapsedTime * 100);
    putU24(dv, 3, mockPm5State.distance * 10);
    putU8(dv, 6, driveLength * 100);
    putU8(dv, 7, driveSeconds * 100);
    putU16(dv, 8, (strokeSeconds - driveSeconds) * 100);
    putU16(dv, 10, 500 / mockPm5State.pace * strokeSeconds * 100);
    putU16(dv, 12, averageForceLbs * 1.5 * 10);
    putU16(dv, 14, averageForceLbs * 10);
    putU16(dv, 16, work * 10); // work per stroke, 0.1 J
    putU16(dv, PM5_STROKE_DATA_STROKE_COUNT_OFFSET, mockPm5State.strokeCount);
    return dv;
  }

  function mockPm5AdditionalStrokeData(power) {
    var dv = new DataView(new ArrayBuffer(15));
    putU24(dv, 0, mockPm5State.elapsedTime * 100);
    putU16(dv, PM5_ADDITIONAL_STROKE_DATA_STROKE_POWER_OFFSET, power);
    putU16(dv, 5, power > 0 ? 4 * power + 350 : 0); // stroke calories, cal/hr
    putU16(dv, PM5_ADDITIONAL_STROKE_DATA_STROKE_COUNT_OFFSET, mockPm5State.strokeCount);
    putU24(dv, 9, 0); // projected work time
    putU24(dv, 12, 0); // projected work distance
    return dv;
  }

  function notifyMockPm5(key, value) {
    onNotify({ target: { uuid: pm5Characteristics[key].uuid, value: value } });
  }

  function emitMockPm5Status() {
    notifyMockPm5('generalStatus', mockPm5GeneralStatus());
    notifyMockPm5('additionalStatus1', mockPm5AdditionalStatus1());
    notifyMockPm5('additionalStatus2', mockPm5AdditionalStatus2());
  }

  function mockPm5StatusTick() {
    var dt = mockPm5Config.statusIntervalMs / 1000;
    mockPm5State.elapsedTime += dt;
    if (mockPm5Rowing()) {
      var power = mockPm5Power();
      mockPm5State.distance += dt * 500 / mockPm5State.pace;
      mockPm5State.work += power * dt;
      mockPm5State.calories += dt * (4 * power + 350) / 3600;
    }
    emitMockPm5Status();
  }

  function mockPm5StrokeTick() {
    if (!mockPm5Rowing()) return;
    var power = mockPm5Power();
    mockPm5State.strokeCount += 1;
    notifyMockPm5('strokeData', mockPm5StrokeData(60 / mockPm5State.strokeRate, power));
    notifyMockPm5('additionalStrokeData', mockPm5AdditionalStrokeData(power));
  }

  // One stroke timer at 60000 / SPM ms while rowing; none at rest. Coming to
  // rest sends one closing stroke notification carrying zero power, so the
  // power reading drops to 0 instead of holding the last stroke's.
  function scheduleMockPm5Strokes() {
    var intervalMs = mockPm5Rowing() ? Math.round(60000 / mockPm5State.strokeRate) : null;
    if (intervalMs === mockPm5State.strokeTimerMs) return;
    var wasRowing = mockPm5State.strokeTimerMs !== null;
    if (mockPm5StrokeTimer) clearInterval(mockPm5StrokeTimer);
    mockPm5StrokeTimer = null;
    mockPm5State.strokeTimerMs = intervalMs;
    if (intervalMs === null) {
      if (wasRowing) notifyMockPm5('additionalStrokeData', mockPm5AdditionalStrokeData(0));
      return;
    }
    mockPm5StrokeTimer = setInterval(mockPm5StrokeTick, intervalMs);
  }

  function parseMockPm5Spm(text) {
    var s = String(text == null ? '' : text).trim();
    if (!/^[+-]?\d+(\.\d+)?$/.test(s)) return null;
    var spm = Math.round(Number(s));
    return Math.min(mockPm5Config.maxStrokeRate, Math.max(mockPm5Config.minStrokeRate, spm));
  }

  function parseMockPm5Pace(text) {
    var match = String(text == null ? '' : text).trim().match(/^(\d+):(\d{1,2})$/);
    if (!match) return null;
    var seconds = Number(match[1]) * 60 + Number(match[2]);
    return Math.min(mockPm5Config.maxPace, Math.max(mockPm5Config.minPace, seconds));
  }

  // 'input' applies a valid value as it is typed; 'change' also writes the
  // clamped value (or the last good one) back into the field.
  function bindMockPm5Setting(input, parse, apply, format) {
    if (!input || !input.addEventListener) return;
    function update(writeBack) {
      var value = parse(input.value);
      if (value !== null) apply(value);
      if (writeBack) input.value = format();
    }
    input.addEventListener('input', function () { update(false); });
    input.addEventListener('change', function () { update(true); });
  }

  function renderMockPm5StartButton(button) {
    if (button) button.textContent = mockPm5State.running ? 'Stop rowing' : 'Start rowing';
  }

  function startMockPm5Panel() {
    if (mockPm5PanelStarted) return;
    mockPm5PanelStarted = true;
    var spmInput = document.getElementById('sim-spm');
    var paceInput = document.getElementById('sim-pace');
    var startButton = document.getElementById('sim-start');

    bindMockPm5Setting(spmInput, parseMockPm5Spm, function (spm) {
      mockPm5State.strokeRate = spm;
      scheduleMockPm5Strokes();
    }, function () { return String(mockPm5State.strokeRate); });
    bindMockPm5Setting(paceInput, parseMockPm5Pace, function (pace) {
      mockPm5State.pace = pace;
    }, function () { return formatDuration(mockPm5State.pace); });

    if (startButton && startButton.addEventListener) {
      startButton.addEventListener('click', function () {
        mockPm5State.running = !mockPm5State.running;
        renderMockPm5StartButton(startButton);
        scheduleMockPm5Strokes();
      });
    }
    renderMockPm5StartButton(startButton);
  }

  function startMockPm5() {
    if (!devMode) return;
    revealRowerCanvas();
    showZeroMetrics();
    showConnectionInfo('Development mock PM5 — streaming synthetic rowing metrics.');
    startMockPm5Panel();
    setPm5Connected(true);

    if (mockPm5StatusTimer) clearInterval(mockPm5StatusTimer);
    mockPm5StatusTimer = setInterval(mockPm5StatusTick, mockPm5Config.statusIntervalMs);
    scheduleMockPm5Strokes();
    emitMockPm5Status();
  }

  if (devMode) startMockPm5();
  // DEV_MOCK_PM5_END

  function decodePm5Notification(uuid, dv) {
    var def = pm5CharacteristicsByUuid[uuid];
    var metrics = {};
    if (!def || !dv) return metrics;

    if (typeof def.elapsedTimeOffset === 'number' && hasBytes(dv, def.elapsedTimeOffset, 3)) {
      metrics.elapsedTime = u24le(dv, def.elapsedTimeOffset) * def.elapsedTimeScaleSeconds;
    }

    if (typeof def.distanceOffset === 'number' && hasBytes(dv, def.distanceOffset, 3)) {
      metrics.distance = u24le(dv, def.distanceOffset) * def.distanceScaleMeters;
    }

    if (typeof def.strokeRateOffset === 'number' && hasBytes(dv, def.strokeRateOffset, 1)) {
      metrics.strokeRate = dv.getUint8(def.strokeRateOffset);
    }

    if (typeof def.heartRateOffset === 'number' && hasBytes(dv, def.heartRateOffset, 1)) {
      var heartRate = dv.getUint8(def.heartRateOffset);
      metrics.heartRate = heartRate === def.heartRateInvalid ? 0 : heartRate;
    }

    if (typeof def.currentPaceOffset === 'number' && hasBytes(dv, def.currentPaceOffset, 2)) {
      metrics.pace = dv.getUint16(def.currentPaceOffset, true) * def.currentPaceScaleSeconds;
    }

    if (typeof def.totalCaloriesOffset === 'number' && hasBytes(dv, def.totalCaloriesOffset, 2)) {
      metrics.calories = dv.getUint16(def.totalCaloriesOffset, true);
    }

    if (typeof def.strokePowerOffset === 'number' && hasBytes(dv, def.strokePowerOffset, 2)) {
      metrics.power = dv.getUint16(def.strokePowerOffset, true);
    }

    if (typeof def.strokeCountOffset === 'number' && hasBytes(dv, def.strokeCountOffset, 2)) {
      metrics.strokeCount = dv.getUint16(def.strokeCountOffset, true);
    }

    return metrics;
  }

  function onNotify(event) {
    var metrics = decodePm5Notification(event.target.uuid, event.target.value);
    renderMetrics(metrics);
    if (typeof metrics.strokeRate === 'number') syncRowerAnimation(metrics.strokeRate);
    streamRaceMetrics(metrics);
  }

  async function getOptionalCharacteristic(service, def) {
    try {
      return await service.getCharacteristic(def.uuid);
    } catch (_) {
      return null;
    }
  }

  async function subscribeToPm5Metrics(server) {
    var svc = await server.getPrimaryService(concept2RowingServiceUuid);
    var keys = Object.keys(pm5Characteristics);

    for (var i = 0; i < keys.length; i++) {
      var ch = await getOptionalCharacteristic(svc, pm5Characteristics[keys[i]]);
      if (!ch) continue;

      await ch.startNotifications();
      ch.addEventListener('characteristicvaluechanged', onNotify);
    }
  }

  connectBtn.addEventListener('click', async function () {
    if (!window.isSecureContext || !navigator.bluetooth) {
      showConnectionInfo('Web Bluetooth is not available in this browser or this page is not a secure context. Use a supported browser over HTTPS or localhost.');
      return;
    }

    showConnectionInfo('Searching for a Concept2 rowing machine...');

    try {
      var device = await navigator.bluetooth.requestDevice({
        filters: [{ services: [concept2DiscoveryServiceUuid] }],
        optionalServices: [concept2RowingServiceUuid]
      });

      device.addEventListener('gattserverdisconnected', function () {
        stopRowerAnimation();
        hideRowerCanvas();
        showConnectionInfo((device.name || 'Concept2 rowing machine') + ' — Disconnected');
        setPm5Connected(false);
      });

      var server = await device.gatt.connect();
      var status = server.connected ? 'Connected' : 'Connection status unknown';
      showConnectionInfo((device.name || 'Concept2 rowing machine') + ' — ' + status);
      revealRowerCanvas();
      showZeroMetrics();

      try {
        await subscribeToPm5Metrics(server);
        setPm5Connected(true);
      } catch (err) {
        console.error('[PM5] failed to start live rowing data:', err);
        showConnectionInfo((device.name || 'Concept2 rowing machine') + ' — ' + status + '. Connected, but live rowing data could not be started.');
      }
    } catch (err) {
      if (err && err.name === 'NotFoundError') {
        showConnectionInfo('No rowing machine selected. Choose a Concept2 rowing machine to connect.');
        return;
      }

      console.error('[PM5] failed to connect to rowing machine:', err);
      showConnectionInfo('Could not connect to the rowing machine. Please try again.');
    }
  });
})();
