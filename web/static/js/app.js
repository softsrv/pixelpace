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

  var connectionInfo = document.getElementById('bt-connection-info');
  var metricsRegion = document.getElementById('bt-metrics');
  var rowerCanvas = document.getElementById('bt-rower-canvas');
  var rowerCtx = rowerCanvas && rowerCanvas.getContext ? rowerCanvas.getContext('2d') : null;
  var currentStrokeRate = 0;
  var rowerTimer = null;
  var rowerTimerIntervalMs = null;
  var currentRowerFrame = 0;
  var concept2ServiceUuid = 'ce060000-43e5-11e4-916c-0800200c9a66';
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
  var PM5_STROKE_DATA_STROKE_COUNT_OFFSET = 16;
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
  }

  async function getOptionalCharacteristic(service, def) {
    try {
      return await service.getCharacteristic(def.uuid);
    } catch (_) {
      return null;
    }
  }

  async function subscribeToPm5Metrics(server) {
    var svc = await server.getPrimaryService(concept2ServiceUuid);
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
        filters: [{ services: [concept2ServiceUuid] }],
        optionalServices: [concept2ServiceUuid]
      });

      device.addEventListener('gattserverdisconnected', function () {
        stopRowerAnimation();
        hideRowerCanvas();
        showConnectionInfo((device.name || 'Concept2 rowing machine') + ' — Disconnected');
      });

      var server = await device.gatt.connect();
      var status = server.connected ? 'Connected' : 'Connection status unknown';
      showConnectionInfo((device.name || 'Concept2 rowing machine') + ' — ' + status);
      revealRowerCanvas();
      showZeroMetrics();

      try {
        await subscribeToPm5Metrics(server);
      } catch (_) {
        showConnectionInfo((device.name || 'Concept2 rowing machine') + ' — ' + status + '. Connected, but live rowing data could not be started.');
      }
    } catch (err) {
      if (err && err.name === 'NotFoundError') {
        showConnectionInfo('No rowing machine selected. Choose a Concept2 rowing machine to connect.');
        return;
      }

      showConnectionInfo('Could not connect to the rowing machine. Please try again.');
    }
  });
})();
