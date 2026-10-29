// ── Race view ─────────────────────────────────────────────────────────────────
//
// Draws the race on a PixiJS canvas: one lane per participant, the start line at
// the bottom and the finish line at the top. Each rower sits the same share of
// the way up the course as the share of the race distance they have rowed.
//
// Before the race the page reports its racer ready once the view has loaded and
// the rowing machine is connected (app.js signals `pixelpace:pm5-status`), then
// polls the room until the server schedules the start. The countdown runs off
// the server's clock and shows "GO!" at zero; only then does our rower move.
//
// Telemetry arrives as DOM events from app.js: `pixelpace:local-telemetry` for
// our own samples and `pixelpace:race-telemetry` for every sample the server
// broadcasts. PIXI is the global from the vendored classic scripts.

import { countdownLabel, laneCenterX, progressFraction, trackY } from './race-track.mjs';

var FINISH_MARGIN = 40; // space above the finish line for its label
var START_MARGIN = 80; // space below the start line for the rowers and labels
var BOAT_WIDTH = 16;
var BOAT_LENGTH = 44;
var CHECKER_SIZE = 8;
var POLL_INTERVAL_MS = 500;
var FINISH_POLL_MS = 5000; // while racing, how often to check whether the race has ended
var READY_RETRY_MS = 2000;
var UNREADY_ALPHA = 0.35; // rivals still connecting their equipment are dimmed
var EASE_PER_FRAME = 0.15; // share of the remaining gap a rower closes each 60fps frame
var WATER_COLOR = 0x1e63c9;
var SELF_COLOR = 0xfacc15;
var RIVAL_COLOR = 0xe5e7eb;

var REMOVED_MESSAGE = 'You were removed from this race because your rowing machine was not connected in time.';
var RESULTS_ERROR_MESSAGE = 'We couldn\u2019t load the results. You can still head back to the dashboard.';

// Guarded so node tests can import this module without a DOM.
var trackEl = typeof document !== 'undefined' ? document.getElementById('race-track') : null;
if (trackEl && window.PIXI) startRaceView(trackEl, window.PIXI);

// Once the race is over, the server sends a finished frame (app.js turns it
// into `pixelpace:race-finished`) or, on a reload, renders data-finished="true".
// Either way the FINISH! modal opens with the results, unless this racer was
// removed before the start.
export async function startRaceView(trackEl, PIXI) {
  var selfId = trackEl.dataset.selfId;
  var targetMillimeters = Number(trackEl.dataset.targetMm);
  var statusUrl = trackEl.dataset.statusUrl;
  var readyUrl = trackEl.dataset.readyUrl;
  var lobbyUrl = trackEl.dataset.lobbyUrl;
  var resultsUrl = trackEl.dataset.resultsUrl;
  var dashboardUrl = trackEl.dataset.dashboardUrl || '/dashboard';
  var finishedOnLoad = trackEl.dataset.finished === 'true';
  var finishPollMs = trackEl.dataset.finishPollMs ? Number(trackEl.dataset.finishPollMs) : FINISH_POLL_MS;
  var statusEl = document.getElementById('race-status');
  var modalEl = document.getElementById('race-finish-modal');
  var lanes = Array.from(document.querySelectorAll('#race-lanes [data-user-id]'), function (el) {
    return {
      userId: el.dataset.userId,
      label: el.textContent.trim(),
      isSelf: el.dataset.self === 'true',
      distanceMillimeters: 0,
      y: null,
      sprite: null
    };
  });
  var lanesById = Object.create(null);
  lanes.forEach(function (lane) { lanesById[lane.userId] = lane; });
  var self = lanesById[selfId];
  var raceId = trackEl.dataset.raceId || null;
  var startsAt = null; // server-scheduled start, in server-clock milliseconds
  var clockOffset = 0; // server clock minus ours
  var reported = false;
  var streaming = false;
  var stopped = false;
  var racing = false;
  var finished = false;
  var removed = false; // dropped before the start: never shown the results
  var modalOpened = false;
  var checkingRemoval = false; // a finished reload's status check is in flight
  var countdownText = null; // assigned once the canvas has initialised

  document.addEventListener('pixelpace:race-finished', openFinishModal);
  var doneBtn = modalEl ? modalEl.querySelector('button.btn-primary') : null;
  if (doneBtn) doneBtn.addEventListener('click', leaveRace);
  // Escape would close the dialog and strand the racer on a finished track;
  // the way out is Done.
  if (modalEl) modalEl.addEventListener('cancel', function (evt) { evt.preventDefault(); });
  // A reload after the race ended: check this racer is still in the race
  // before showing the results. Starts before the canvas initialises, so a
  // removal can be reported while it is still loading.
  if (finishedOnLoad) checkFinishedOnLoad();

  var app = new PIXI.Application();
  await app.init({
    resizeTo: trackEl,
    background: WATER_COLOR,
    antialias: true,
    autoDensity: true,
    resolution: window.devicePixelRatio || 1
  });
  trackEl.appendChild(app.canvas);

  var course = new PIXI.Graphics();
  var startText = courseLabel(PIXI, 'START');
  var finishText = courseLabel(PIXI, 'FINISH');
  countdownText = new PIXI.Text({
    text: '',
    style: { fill: 0xffffff, fontSize: 120, fontFamily: 'sans-serif', fontWeight: 'bold', stroke: { color: 0x111827, width: 8 } }
  });
  countdownText.anchor.set(0.5);
  app.stage.addChild(course, startText, finishText);
  lanes.forEach(function (lane) {
    lane.sprite = createRower(PIXI, lane);
    app.stage.addChild(lane.sprite);
  });
  app.stage.addChild(countdownText);

  var drawnWidth = 0;
  var drawnHeight = 0;
  app.ticker.add(function (ticker) {
    var geometry = courseGeometry(app.screen);
    if (geometry.width !== drawnWidth || geometry.height !== drawnHeight) {
      drawCourse(course, startText, finishText, geometry, lanes.length);
      countdownText.position.set(geometry.width / 2, geometry.height / 2);
      // "GO!" is the widest label; keep it inside narrow (phone) tracks.
      countdownText.style.fontSize = Math.min(120, Math.floor(geometry.width * 0.4));
      drawnWidth = geometry.width;
      drawnHeight = geometry.height;
    }
    if (startsAt !== null) {
      var remaining = startsAt - (Date.now() + clockOffset);
      countdownText.text = countdownLabel(remaining);
      if (remaining <= 0 && !racing) go();
    }
    var ease = 1 - Math.pow(1 - EASE_PER_FRAME, ticker.deltaTime);
    lanes.forEach(function (lane, index) {
      var fraction = progressFraction(lane.distanceMillimeters, targetMillimeters);
      var targetY = trackY(fraction, geometry.startY, geometry.finishY);
      lane.y = lane.y === null ? targetY : lane.y + (targetY - lane.y) * ease;
      lane.sprite.position.set(laneCenterX(index, lanes.length, geometry.width), lane.y);
    });
  });

  document.addEventListener('pixelpace:local-telemetry', function (evt) {
    if (!racing || !self) return;
    self.distanceMillimeters = evt.detail.distance_millimeters;
    if (!finished && progressFraction(self.distanceMillimeters, targetMillimeters) >= 1) {
      finished = true;
      if (!modalOpened) setStatus('Finished! Waiting for the other rowers to finish\u2026');
    }
  });

  document.addEventListener('pixelpace:race-telemetry', function (evt) {
    var sample = evt.detail;
    // Our own rower follows local telemetry; the echo can lag behind it.
    if (!sample || sample.user_id === selfId) return;
    var lane = lanesById[sample.user_id];
    if (lane) lane.distanceMillimeters = sample.distance_millimeters;
  });

  // The view is drawn; the racer is ready once their rowing machine is too.
  document.addEventListener('pixelpace:pm5-status', function (evt) {
    if (evt.detail && evt.detail.connected) reportReady();
    else if (!racing && !stopped) setStatus('Your rowing machine disconnected. Reconnect it to make the start.');
  });
  // A finished race has nothing left to join or poll for.
  if (finishedOnLoad) return;
  reportReady();
  if (!reported) setStatus('Connect your rowing machine to take your place at the start.');
  pollStatus();

  // Tells the server this racer is ready. Repeats are harmless, so failures retry.
  async function reportReady() {
    if (reported || stopped || !pm5Connected()) return;
    reported = true;
    setStatus('Ready! Waiting for the other rowers…');
    var res = await request(readyUrl, { method: 'POST' });
    if (stopped) return;
    if (res && res.ok) return;
    if (res && res.status === 404) return dropped();
    if (res && res.status === 409) return backToLobby();
    reported = false;
    setTimeout(reportReady, READY_RETRY_MS);
  }

  // Follows the room until the race exists and our telemetry is streaming.
  async function pollStatus() {
    while (!stopped && !streaming) {
      var sentAt = Date.now();
      var res = await request(statusUrl, { headers: { Accept: 'application/json' } });
      if (stopped) return;
      if (res && res.status === 404) return dropped();
      if (res && res.status === 409) return backToLobby();
      if (res && res.ok) {
        var body = await res.json().catch(function () { return null; });
        if (body) applyStatus(body, sentAt, Date.now());
      }
      if (!streaming) await new Promise(function (resolve) { setTimeout(resolve, POLL_INTERVAL_MS); });
    }
  }

  function applyStatus(status, sentAt, receivedAt) {
    var serverTime = Date.parse(status.server_time);
    if (!Number.isNaN(serverTime)) clockOffset = serverTime - (sentAt + receivedAt) / 2;
    if (Array.isArray(status.racers)) syncLanes(status.racers);
    if (status.race_starts_at && startsAt === null) {
      startsAt = Date.parse(status.race_starts_at);
      if (!racing) setStatus('Everyone is ready. Get set!');
    } else if (startsAt === null && reported && Array.isArray(status.racers)) {
      var ready = status.racers.filter(function (racer) { return racer.equipment_ready; }).length;
      setStatus('Ready! Waiting for the other rowers: ' + ready + ' of ' + status.racers.length + ' connected.');
    }
    if (status.race_id) {
      raceId = status.race_id;
      if (racing) startStreaming();
    }
  }

  // Drops lanes for racers removed from the start, and dims rivals who are
  // still connecting their equipment.
  function syncLanes(racers) {
    var present = {};
    racers.forEach(function (racer) {
      present[racer.user_id] = racer;
    });
    var kept = lanes.filter(function (lane) {
      if (present[lane.userId]) return true;
      app.stage.removeChild(lane.sprite);
      lane.sprite.destroy({ children: true });
      delete lanesById[lane.userId];
      return false;
    });
    if (kept.length !== lanes.length) {
      lanes = kept;
      drawnWidth = 0; // redraw the lane dividers
    }
    lanes.forEach(function (lane) {
      lane.sprite.alpha = racing || lane.isSelf || present[lane.userId].equipment_ready ? 1 : UNREADY_ALPHA;
    });
  }

  // The countdown has reached zero: from here our telemetry moves the boat.
  function go() {
    racing = true;
    lanes.forEach(function (lane) { lane.sprite.alpha = 1; });
    document.dispatchEvent(new CustomEvent('pixelpace:race-started', { detail: { raceId: raceId } }));
    setStatus('GO! Row toward the finish line.');
    if (raceId) startStreaming();
  }

  function startStreaming() {
    if (streaming) return;
    streaming = true;
    if (typeof window.startRaceStream === 'function') window.startRaceStream(raceId);
    watchForFinish();
  }

  // The finished frame is sent once; a socket that is down at that moment, or
  // a publish that fails, would leave this racer on the track. So while
  // racing, the status endpoint is checked too, and finished:true opens the
  // modal the same way the frame does.
  async function watchForFinish() {
    while (!modalOpened && !removed) {
      await new Promise(function (resolve) { setTimeout(resolve, finishPollMs); });
      if (modalOpened || removed) return;
      var res = await request(statusUrl, { headers: { Accept: 'application/json' } });
      if (modalOpened || removed) return;
      if (res && res.status === 404) return dropped();
      if (!res || !res.ok) continue;
      var body = await res.json().catch(function () { return null; });
      if (body && body.finished === true) return openFinishModal();
    }
  }

  function dropped() {
    stopped = true;
    removed = true;
    document.removeEventListener('pixelpace:race-finished', openFinishModal);
    // A 404 can arrive before the canvas (and its countdown text) exists.
    if (countdownText) countdownText.text = '';
    setStatus(REMOVED_MESSAGE);
  }

  // On a reload of a finished race, the status check tells a removed racer
  // (404) apart from one who raced, before the modal may open.
  async function checkFinishedOnLoad() {
    stopped = true;
    checkingRemoval = true;
    setStatus('The race is over.');
    var res = await request(statusUrl, { headers: { Accept: 'application/json' } });
    checkingRemoval = false;
    if (res && res.status === 404) return dropped();
    if (res && res.status === 409) return backToLobby();
    openFinishModal();
  }

  // Opens the FINISH! modal once, stops following the race and loads the results.
  function openFinishModal() {
    if (removed || checkingRemoval || modalOpened || !modalEl) return;
    modalOpened = true;
    stopped = true;
    if (typeof modalEl.showModal === 'function') modalEl.showModal();
    else modalEl.setAttribute('open', '');
    loadResults();
  }

  async function loadResults() {
    var res = resultsUrl ? await request(resultsUrl, { headers: { Accept: 'application/json' } }) : null;
    var body = res && res.ok ? await res.json().catch(function () { return null; }) : null;
    if (!body) return showResultsError();
    try {
      var helpers = await import('./race-results.mjs');
      renderResults(helpers.resultRows(body, selfId));
    } catch (_) {
      showResultsError();
    }
  }

  function renderResults(result) {
    var rows = Array.isArray(result) ? result : (result && result.rows) || [];
    var message = result && (result.message || result.emptyMessage);
    var tbody = modalEl.querySelector('tbody');
    if (!tbody) return showResultsError();
    tbody.textContent = '';
    if (message) {
      var messageRow = document.createElement('tr');
      var messageCell = document.createElement('td');
      messageCell.colSpan = 5;
      messageCell.textContent = message;
      messageRow.appendChild(messageCell);
      tbody.appendChild(messageRow);
    }
    rows.forEach(function (row) {
      var tr = document.createElement('tr');
      if (row.isSelf) tr.classList.add('font-bold');
      var isDnf = String(row.status || '').toLowerCase() === 'dnf';
      [row.rank || (isDnf ? 'DNF' : ''), row.name, row.time, row.distance, row.pace].forEach(function (text) {
        var td = document.createElement('td');
        td.textContent = text == null ? '' : String(text);
        tr.appendChild(td);
      });
      tbody.appendChild(tr);
    });
    setHidden(modalEl.querySelector('table'), false);
    setHidden(modalEl.querySelector('.text-error'), true);
  }

  // The error takes the table's place; Done still works.
  function showResultsError() {
    setHidden(modalEl.querySelector('table'), true);
    var errorEl = modalEl.querySelector('.text-error');
    if (errorEl) {
      errorEl.textContent = RESULTS_ERROR_MESSAGE;
      setHidden(errorEl, false);
    }
  }

  // Done closes the race connection and goes to the dashboard, whether or
  // not the results loaded.
  function leaveRace() {
    stopped = true;
    if (typeof window.stopRaceStream === 'function') {
      try { window.stopRaceStream(); } catch (_) { /* leave regardless */ }
    }
    window.location.assign(dashboardUrl);
  }

  function backToLobby() {
    stopped = true;
    if (lobbyUrl) window.location.assign(lobbyUrl);
  }

  function setStatus(message) {
    if (statusEl) statusEl.textContent = message;
  }
}

function pm5Connected() {
  return typeof window.isPm5Connected === 'function' && window.isPm5Connected();
}

function setHidden(el, hidden) {
  if (!el) return;
  if (hidden) el.classList.add('hidden');
  else el.classList.remove('hidden');
}

// fetch that silently refreshes the short-lived access token once on 401.
// Resolves to null on a network error.
async function request(url, options) {
  try {
    var res = await fetch(url, options);
    if (res.status !== 401) return res;
    await fetch('/auth/refresh', { method: 'POST' });
    return await fetch(url, options);
  } catch (_) {
    return null;
  }
}

function courseGeometry(screen) {
  return {
    width: screen.width,
    height: screen.height,
    startY: screen.height - START_MARGIN,
    finishY: FINISH_MARGIN
  };
}

function drawCourse(course, startText, finishText, geometry, laneCount) {
  course.clear();
  for (var i = 1; i < laneCount; i++) {
    var x = (geometry.width * i) / laneCount;
    course.moveTo(x, geometry.finishY).lineTo(x, geometry.startY);
  }
  course.stroke({ width: 1, color: 0xffffff, alpha: 0.3 });

  course.rect(0, geometry.startY - 2, geometry.width, 4).fill(0xffffff);

  // A two-row checkered band whose lower edge is the finish line.
  for (var row = 0; row < 2; row++) {
    for (var col = 0; col * CHECKER_SIZE < geometry.width; col++) {
      var color = (row + col) % 2 === 0 ? 0xffffff : 0x111827;
      course.rect(col * CHECKER_SIZE, geometry.finishY - (2 - row) * CHECKER_SIZE, CHECKER_SIZE, CHECKER_SIZE).fill(color);
    }
  }

  startText.position.set(geometry.width - 6, geometry.startY + 4);
  finishText.position.set(geometry.width - 6, geometry.finishY - 2 * CHECKER_SIZE - 4);
  finishText.anchor.set(1, 1);
}

function courseLabel(PIXI, text) {
  var label = new PIXI.Text({ text: text, style: { fill: 0xffffff, fontSize: 11, fontFamily: 'sans-serif', fontWeight: 'bold' } });
  label.anchor.set(1, 0);
  label.alpha = 0.8;
  return label;
}

// A rower's placeholder: a hull whose bow sits at the sprite's origin, so the
// bow touches the start line at 0% and the finish line at 100%.
function createRower(PIXI, lane) {
  var rower = new PIXI.Container();
  var hull = new PIXI.Graphics()
    .roundRect(-BOAT_WIDTH / 2, 0, BOAT_WIDTH, BOAT_LENGTH, BOAT_WIDTH / 2)
    .fill(lane.isSelf ? SELF_COLOR : RIVAL_COLOR);
  var label = new PIXI.Text({
    text: lane.label,
    style: { fill: 0xffffff, fontSize: 12, fontFamily: 'sans-serif', fontWeight: lane.isSelf ? 'bold' : 'normal' }
  });
  label.anchor.set(0.5, 0);
  label.y = BOAT_LENGTH + 4;
  rower.addChild(hull, label);
  return rower;
}
