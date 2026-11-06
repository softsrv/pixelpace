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
//
// The look is pixel art: sprites and the countdown font live in
// race-pixels.mjs and are drawn as solid squares on an un-antialiased canvas.
// Each boat's oars sweep at that racer's live stroke rate.
//
// In a Time Trial with an earlier finished time, the page also lists a pacer
// lane (data-pacer, data-pacer-ms): a boat labelled with that personal best's
// average pace per 500m (m:ss.t, from data-pacer-ms over data-target-mm) that
// rows the race at that personal best, placed from the server clock. It is
// never an entrant: telemetry never moves it, it takes no rival colour, and the
// results never list it. The status poll adds it (pacer set), relabels it when
// its time changes, or removes it (pacer null).

import { countdownLabel, laneCenterX, pacerDistance, progressFraction, trackY } from './race-track.mjs';
import { formatDistance, formatPace } from './race-results.mjs';
import {
  BOAT_POSES, BOAT_ROWS, BOAT_COLS, COLORS, GLYPH_ROWS, PACER_HULL,
  advanceStroke, boatCells, cellColor, hullColor, pixelScale, snap, strokePose, textCells
} from './race-pixels.mjs';

var FINISH_MARGIN = 40; // space above the finish line for its label
var START_MARGIN = 112; // space below the start line for the rowers (up to 80px long at 4x), wake and labels
var CHECKER_SIZE = 8;
var BUOY_SPACING = 18; // screen pixels between lane-divider buoys
var RIPPLE_COUNT = 70; // ripple dashes per screen height of water
var RIPPLE_IDLE_SPEED = 6; // px/s the water drifts while nobody rows
var RIPPLE_ROW_SPEED = 40; // px/s it rushes past once we are rowing
var POLL_INTERVAL_MS = 500;
var FINISH_POLL_MS = 5000; // while racing, how often to check whether the race has ended
var READY_RETRY_MS = 2000;
var WARMUP_TICK_MS = 200; // how often a warmup's clock is redrawn
var UNREADY_ALPHA = 0.35; // rivals still connecting their equipment are dimmed
var EASE_PER_FRAME = 0.15; // share of the remaining gap a rower closes each 60fps frame
var PACER_STROKE_RATE = 24; // the pacer has no live stroke rate; its oars sweep at this one
var PACER_ID = 'pacer';
var LABEL_FONT = 'ui-monospace, "SFMono-Regular", Menlo, Consolas, monospace';

var REMOVED_MESSAGE = 'You were removed from this race because your rowing machine was not connected in time.';
var RESULTS_ERROR_MESSAGE = 'We couldn\u2019t load the results. You can still head back to the dashboard.';

// Guarded so node tests can import this module without a DOM.
var trackEl = typeof document !== 'undefined'
  ? document.getElementById('race-track') || document.getElementById('race-warmup')
  : null;
// A warmup draws no track, so it does not wait for PIXI.
if (trackEl && (window.PIXI || trackEl.dataset.activityType === 'warmup')) startRaceView(trackEl, window.PIXI);

// Once the race is over, the server sends a finished frame (app.js turns it
// into `pixelpace:race-finished`) or, on a reload, renders data-finished="true".
// Either way the FINISH! modal opens with the results, unless this racer was
// removed before the start.
export async function startRaceView(trackEl, PIXI) {
  var selfId = trackEl.dataset.selfId;
  var targetMillimeters = Number(trackEl.dataset.targetMm);
  var activityType = trackEl.dataset.activityType;
  var mode = activityType;
  // A warmup is a timed solo session: no track, no pacer, a countdown clock
  // beside the rower's live distance, pace and stroke rate.
  var warmup = activityType === 'warmup';
  var durationMs = Number(trackEl.dataset.durationMs);
  var statusUrl = trackEl.dataset.statusUrl;
  var readyUrl = trackEl.dataset.readyUrl;
  var lobbyUrl = trackEl.dataset.lobbyUrl;
  var resultsUrl = trackEl.dataset.resultsUrl;
  var dashboardUrl = trackEl.dataset.dashboardUrl || '/dashboard';
  var pageUrl = trackEl.dataset.pageUrl;
  var finishedOnLoad = trackEl.dataset.finished === 'true';
  var finishPollMs = trackEl.dataset.finishPollMs ? Number(trackEl.dataset.finishPollMs) : FINISH_POLL_MS;
  var statusEl = document.getElementById('race-status');
  var modalEl = document.getElementById('race-finish-modal');
  var lanesListEl = document.getElementById('race-lanes');
  var pacerNoteEl = document.getElementById('race-pacer-note');
  var lanes = Array.from(document.querySelectorAll('#race-lanes [data-user-id]'), function (el) {
    var isPacer = el.dataset.pacer === 'true';
    var pacerMs = isPacer ? Number(el.dataset.pacerMs) : 0;
    var label = isPacer ? formatPace(pacerMs, targetMillimeters) : el.textContent.trim();
    if (isPacer) el.textContent = label; // the lane list names the pacer as its boat does
    return {
      userId: el.dataset.userId,
      label: label,
      isSelf: !isPacer && el.dataset.self === 'true',
      isPacer: isPacer,
      pacerMs: pacerMs,
      el: el,
      distanceMillimeters: 0,
      strokeRate: 0,
      strokePhase: 0,
      y: null,
      sprite: null
    };
  });
  // Racers only: the pacer is never a racer, so telemetry can never reach it.
  var lanesById = Object.create(null);
  lanes.forEach(function (lane) { if (!lane.isPacer) lanesById[lane.userId] = lane; });
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
  var timeUp = false; // a warmup's clock has reached zero
  var clockEl = warmup ? document.getElementById('warmup-clock') : null;
  var distanceEl = warmup ? document.getElementById('warmup-distance') : null;
  var paceEl = warmup ? document.getElementById('warmup-pace') : null;
  var strokeRateEl = warmup ? document.getElementById('warmup-stroke-rate') : null;

  document.addEventListener('pixelpace:race-finished', openFinishModal);
  var doneBtn = modalEl ? modalEl.querySelector('button[data-finish-done]') : null;
  if (doneBtn) doneBtn.addEventListener('click', leaveRace);
  // Escape would close the dialog and strand the racer on a finished track;
  // the way out is Done.
  if (modalEl) modalEl.addEventListener('cancel', function (evt) { evt.preventDefault(); });
  // A reload after the race ended: check this racer is still in the race
  // before showing the results. Starts before the canvas initialises, so a
  // removal can be reported while it is still loading.
  if (finishedOnLoad) checkFinishedOnLoad();

  // A warmup has no track: no canvas, lanes, boats or pacer are ever created.
  var app = null;
  var scale = 1;
  var drawnWidth = 0;
  var drawnHeight = 0;
  if (!warmup) {
    app = new PIXI.Application();
    await app.init({
      resizeTo: trackEl,
      background: COLORS.water,
      antialias: false,
      roundPixels: true,
      autoDensity: true,
      resolution: window.devicePixelRatio || 1
    });
    trackEl.appendChild(app.canvas);

    var water = new PIXI.Graphics();
    var ripples = new PIXI.Container();
    var rippleArt = new PIXI.Graphics();
    ripples.addChild(rippleArt);
    var course = new PIXI.Graphics();
    var startText = courseLabel(PIXI, 'START');
    var finishText = courseLabel(PIXI, 'FINISH');
    countdownText = createPixelLabel(PIXI);
    app.stage.addChild(water, ripples, course, startText, finishText);
    scale = pixelScale(app.screen.width / Math.max(1, lanes.length));
    var rivalIndex = 0;
    // The pacer takes no rival slot, so rivals keep the colours they have without it.
    lanes.forEach(function (lane) {
      lane.sprite = createRower(PIXI, lane, lane.isSelf || lane.isPacer ? 0 : rivalIndex++, scale);
      app.stage.addChild(lane.sprite);
    });
    app.stage.addChild(countdownText.view);
    var rippleOffset = 0;
    var rippleSpeed = RIPPLE_IDLE_SPEED;

  
  
    app.ticker.add(function (ticker) {
      var geometry = courseGeometry(app.screen);
      if (geometry.width !== drawnWidth || geometry.height !== drawnHeight) {
        drawWater(water, rippleArt, geometry);
        drawCourse(course, startText, finishText, geometry, lanes.length);
        countdownText.view.position.set(geometry.width / 2, geometry.height / 2);
        // "GO!" is the widest label (13 cells); keep it inside narrow (phone) tracks.
        countdownText.setCellSize(Math.max(4, Math.min(16, Math.floor((geometry.width * 0.5) / 13))));
        var nextScale = pixelScale(geometry.width / Math.max(1, lanes.length));
        if (nextScale !== scale) {
          scale = nextScale;
          lanes.forEach(function (lane) { lane.sprite.setScale(scale); });
        }
        drawnWidth = geometry.width;
        drawnHeight = geometry.height;
      }
      if (startsAt !== null) {
        var remaining = startsAt - (Date.now() + clockOffset);
        countdownText.text = countdownLabel(remaining);
        if (remaining <= 0 && !racing) go();
      }
      var ease = 1 - Math.pow(1 - EASE_PER_FRAME, ticker.deltaTime);
      var elapsedMs = ticker.deltaMS || ticker.deltaTime * (1000 / 60);
      var serverNow = Date.now() + clockOffset;
      lanes.forEach(function (lane, index) {
        // The pacer rows its personal best from the start on the server clock:
        // still before GO, and already partway down the course on a mid-race load.
        if (lane.isPacer) lane.distanceMillimeters = startsAt === null ? 0 : pacerDistance(serverNow - startsAt, lane.pacerMs, targetMillimeters);
        var fraction = progressFraction(lane.distanceMillimeters, targetMillimeters);
        var targetY = trackY(fraction, geometry.startY, geometry.finishY);
        // The pacer's position is exact at every frame, so it needs no easing.
        lane.y = lane.y === null || lane.isPacer ? targetY : lane.y + (targetY - lane.y) * ease;
        lane.sprite.position.set(snap(laneCenterX(index, lanes.length, geometry.width), scale), snap(lane.y, scale));
        // Oars sweep at the racer's own stroke rate; across the line they rest.
        var rate = lane.isPacer ? PACER_STROKE_RATE : lane.strokeRate;
        var rowing = racing && fraction < 1 ? rate : 0;
        lane.strokePhase = advanceStroke(lane.strokePhase, rowing, elapsedMs);
        lane.sprite.setPose(strokePose(lane.strokePhase), rowing > 0);
      });
      // The water rushes past while we row and drifts while we don't.
      var wantSpeed = racing && !finished && self && self.strokeRate > 0 ? RIPPLE_ROW_SPEED : RIPPLE_IDLE_SPEED;
      rippleSpeed += (wantSpeed - rippleSpeed) * ease * 0.1;
      rippleOffset = (rippleOffset + (rippleSpeed * elapsedMs) / 1000) % Math.max(1, geometry.height);
      ripples.y = snap(rippleOffset, scale) - geometry.height;
    });
  }

  document.addEventListener('pixelpace:local-telemetry', function (evt) {
    if (warmup) return showWarmupMetrics(evt.detail);
    if (!racing || !self) return;
    self.distanceMillimeters = evt.detail.distance_millimeters;
    if (typeof evt.detail.stroke_rate === 'number') self.strokeRate = evt.detail.stroke_rate;
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
    if (!lane) return; // lanesById holds racers only, so 'pacer' never matches
    lane.distanceMillimeters = sample.distance_millimeters;
    if (typeof sample.stroke_rate === 'number') lane.strokeRate = sample.stroke_rate;
  });

  // The view is drawn; the racer is ready once their rowing machine is too.
  document.addEventListener('pixelpace:pm5-status', function (evt) {
    if (evt.detail && evt.detail.connected) reportReady();
    else if (!racing && !stopped) setStatus('Your rowing machine disconnected. Reconnect it to make the start.');
  });
  // A finished race has nothing left to join or poll for.
  if (finishedOnLoad) {
    if (warmup) setText(clockEl, countdownLabel(durationMs, durationMs));
    return;
  }
  if (warmup) tickWarmupClock();
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
    if (res && res.status === 410) return notConnected();
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
      if (res && res.status === 410) return notConnected();
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
    pacerNoteEl = pacerNoteEl || document.getElementById('race-pacer-note');
    // An absent key leaves the pacer as it is; null means there is none.
    if (warmup) {
      // A warmup never has a pacer or its note, whatever the frame says.
      setHidden(pacerNoteEl, true);
    } else if (status.pacer === null) {
      removePacer();
      if (mode === 'time_trial' && (!Array.isArray(status.racers) || status.racers.length <= 1)) {
        setHidden(pacerNoteEl, false);
      } else {
        setHidden(pacerNoteEl, true);
      }
    } else if (status.pacer) {
      // The server decides whether a pacer exists; an unusable time still
      // draws the boat, labelled by formatPace's '—' fallback, as on load.
      setPacer(Number(status.pacer.elapsed_milliseconds));
      setHidden(pacerNoteEl, true);
    } else if (mode !== 'time_trial' || (Array.isArray(status.racers) && status.racers.length >= 2)) {
      setHidden(pacerNoteEl, true);
    }
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
  // still connecting their equipment. The pacer is never among the racers and
  // is never dropped here.
  function syncLanes(racers) {
    var present = {};
    racers.forEach(function (racer) {
      present[racer.user_id] = racer;
    });
    var kept = lanes.filter(function (lane) {
      if (lane.isPacer || present[lane.userId]) return true;
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
      lane.sprite.alpha = racing || lane.isSelf || lane.isPacer || present[lane.userId].equipment_ready ? 1 : UNREADY_ALPHA;
    });
  }

  function pacerLane() {
    return lanes.find(function (lane) { return lane.isPacer; }) || null;
  }

  // Adds the pacer lane (last, after the racers, as the page renders it) or
  // updates its time and pace label, keeping the screen-reader lane list in step.
  function setPacer(pacerMs) {
    var label = formatPace(pacerMs, targetMillimeters);
    var lane = pacerLane();
    if (lane) {
      lane.pacerMs = pacerMs;
      var changed = lane.label !== label;
      lane.label = label;
      if (lane.el) {
        lane.el.dataset.pacerMs = String(pacerMs);
        lane.el.textContent = label;
      }
      if (changed) {
        lane.sprite.setLabel(label);
      }
      return;
    }
    var el = document.createElement('li');
    el.dataset.userId = PACER_ID;
    el.dataset.pacer = 'true';
    el.dataset.pacerMs = String(pacerMs);
    el.textContent = label;
    if (lanesListEl) lanesListEl.appendChild(el);
    lane = {
      userId: PACER_ID, label: label, isSelf: false, isPacer: true, pacerMs: pacerMs, el: el,
      distanceMillimeters: 0, strokeRate: 0, strokePhase: 0, y: null, sprite: null
    };
    lane.sprite = createRower(PIXI, lane, 0, scale);
    app.stage.addChild(lane.sprite);
    app.stage.addChild(countdownText.view); // keep the countdown drawn above every boat
    lanes.push(lane);
    drawnWidth = 0; // redraw the lane dividers
  }

  function removePacer() {
    var lane = pacerLane();
    if (!lane) return;
    app.stage.removeChild(lane.sprite);
    lane.sprite.destroy({ children: true });
    if (lane.el && typeof lane.el.remove === 'function') lane.el.remove();
    lanes = lanes.filter(function (other) { return other !== lane; });
    drawnWidth = 0;
  }

  // The countdown has reached zero: from here our telemetry moves the boat.
  function go() {
    racing = true;
    lanes.forEach(function (lane) { lane.sprite.alpha = 1; });
    document.dispatchEvent(new CustomEvent('pixelpace:race-started', { detail: { raceId: raceId } }));
    setStatus(warmup ? 'GO! Row until the clock reaches zero.' : 'GO! Row toward the finish line.');
    if (raceId) startStreaming();
  }

  // Redraws the warmup clock from the server clock until the time is up or the
  // view stops. Before the start it shows the 10-second countdown, then the
  // time left in m:ss, clamped at 0:00. Zero ends our rowing; the server's
  // finished frame (or the status check) then opens the summary.
  function tickWarmupClock() {
    if (stopped) return;
    if (startsAt === null) {
      setText(clockEl, countdownLabel(durationMs, 0));
    } else {
      var sinceStart = Date.now() + clockOffset - startsAt;
      if (sinceStart < 0) {
        setText(clockEl, countdownLabel(-sinceStart));
      } else {
        if (!racing) go();
        setText(clockEl, countdownLabel(durationMs, sinceStart));
        if (sinceStart >= durationMs && !timeUp) {
          timeUp = true;
          setStatus('Time is up! Finishing your warmup\u2026');
        }
        if (timeUp) return; // the clock rests at 0:00 until the finished frame
      }
    }
    setTimeout(tickWarmupClock, WARMUP_TICK_MS);
  }

  // Our own sample, as app.js sends it: distance and elapsed time since the
  // start, and the stroke rate. Pace is the time per 500m covered so far.
  function showWarmupMetrics(sample) {
    if (!racing || timeUp || !sample) return;
    var distanceMm = Number(sample.distance_millimeters);
    if (Number.isNaN(distanceMm)) return;
    setText(distanceEl, formatDistance(distanceMm / 1000));
    var elapsedMs = Number(sample.elapsed_milliseconds);
    setText(paceEl, distanceMm > 0 && elapsedMs > 0 ? formatPace((elapsedMs * 500000) / distanceMm) : '\u2014');
    if (typeof sample.stroke_rate === 'number') setText(strokeRateEl, Math.round(sample.stroke_rate) + ' spm');
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
      renderResults(helpers.resultRows(body, selfId, mode));
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
    var isWarmup = Boolean((result && (result.activityType === 'warmup' || result.mode === 'warmup' || result.isWarmup || result.hasRank === false)) || activityType === 'warmup');
    var rankTh = modalEl.querySelector('th:first-child');
    if (rankTh && rankTh.textContent.trim().toLowerCase() === 'rank') {
      setHidden(rankTh, isWarmup);
    }
    if (message) {
      var messageRow = document.createElement('tr');
      var messageCell = document.createElement('td');
      messageCell.colSpan = isWarmup ? 4 : 5;
      messageCell.textContent = message;
      messageRow.appendChild(messageCell);
      tbody.appendChild(messageRow);
    }
    rows.forEach(function (row) {
      var tr = document.createElement('tr');
      if (row.isSelf) tr.classList.add('font-bold');
      var isDnf = String(row.status || '').toLowerCase() === 'dnf';
      var cols = isWarmup
        ? [row.name, row.time, row.distance, row.pace]
        : [row.rank || (isDnf ? 'DNF' : ''), row.name, row.time, row.distance, row.pace];
      cols.forEach(function (text) {
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

  // 410: this time trial or warmup was dissolved before its start because
  // the rowing machine never connected. Reloading lands on the server's page
  // that says so and links back to the dashboard.
  function notConnected() {
    stopped = true;
    window.location.assign(pageUrl || dashboardUrl);
  }

  function setStatus(message) {
    if (statusEl) statusEl.textContent = message;
  }
}

function setText(el, text) {
  if (el) el.textContent = text;
}

function pm5Connected() {
  return typeof window.isPm5Connected === 'function' && window.isPm5Connected();
}

function setHidden(el, hidden) {
  if (!el) return;
  el.hidden = !!hidden;
  if (el.classList) {
    if (hidden) el.classList.add('hidden');
    else el.classList.remove('hidden');
  }
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

// The water: darker and lighter bands, plus a ripple layer two screens tall
// that the ticker slides down so the course seems to move under the boats.
function drawWater(water, rippleArt, geometry) {
  water.clear();
  var band = 48;
  for (var y = 0; y < geometry.height; y += band * 2) {
    water.rect(0, y + band, geometry.width, band).fill(COLORS.waterBand);
  }
  rippleArt.clear();
  var seed = 7;
  function rand() { seed = (seed * 9301 + 49297) % 233280; return seed / 233280; }
  var count = Math.round(RIPPLE_COUNT * Math.max(1, geometry.width / 400));
  for (var i = 0; i < count; i++) {
    var x = Math.floor(rand() * geometry.width);
    var ry = Math.floor(rand() * geometry.height);
    var w = 4 + Math.floor(rand() * 3) * 2;
    // The same dash in both screens keeps the wrap seamless.
    rippleArt.rect(x, ry, w, 2).fill(COLORS.ripple);
    rippleArt.rect(x, ry + geometry.height, w, 2).fill(COLORS.ripple);
  }
}

function drawCourse(course, startText, finishText, geometry, laneCount) {
  course.clear();
  // Lane dividers are lines of buoys, alternating red and white.
  for (var i = 1; i < laneCount; i++) {
    var x = Math.round((geometry.width * i) / laneCount);
    for (var n = 0, by = geometry.finishY + 6; by < geometry.startY; n++, by += BUOY_SPACING) {
      course.rect(x - 2, by, 4, 4).fill(n % 2 === 0 ? COLORS.buoyRed : COLORS.buoyWhite);
    }
  }

  course.rect(0, geometry.startY - 2, geometry.width, 4).fill(COLORS.white);

  // A two-row checkered band whose lower edge is the finish line.
  for (var row = 0; row < 2; row++) {
    for (var col = 0; col * CHECKER_SIZE < geometry.width; col++) {
      var color = (row + col) % 2 === 0 ? COLORS.white : COLORS.ink;
      course.rect(col * CHECKER_SIZE, geometry.finishY - (2 - row) * CHECKER_SIZE, CHECKER_SIZE, CHECKER_SIZE).fill(color);
    }
  }

  startText.position.set(geometry.width - 6, geometry.startY + 4);
  finishText.position.set(geometry.width - 6, geometry.finishY - 2 * CHECKER_SIZE - 4);
  finishText.anchor.set(1, 1);
}

function courseLabel(PIXI, text) {
  var label = new PIXI.Text({
    text: text,
    style: { fill: COLORS.white, fontSize: 11, fontFamily: LABEL_FONT, fontWeight: 'bold', letterSpacing: 2 }
  });
  label.anchor.set(1, 0);
  label.alpha = 0.8;
  return label;
}

// Draws pixel cells ([col, row, ...] with a colour per cell) as squares.
function paintCells(graphics, cells, size, colorOf, offsetX, offsetY) {
  cells.forEach(function (cell) {
    graphics.rect(offsetX + cell[0] * size, offsetY + cell[1] * size, size, size).fill(colorOf(cell));
  });
}

// A rower: one pre-drawn sprite per stroke pose (only one is visible), a
// wake that shows while they row, and their name. The bow sits at the
// sprite's origin, so it touches the start line at 0% and the finish at 100%.
function createRower(PIXI, lane, rivalIndex, scale) {
  var rower = new PIXI.Container();
  var hull = lane.isPacer ? PACER_HULL : hullColor(lane.isSelf, rivalIndex);
  var poses = {};
  BOAT_POSES.forEach(function (pose) {
    poses[pose] = new PIXI.Graphics();
    rower.addChild(poses[pose]);
  });
  var wake = new PIXI.Graphics();
  rower.addChild(wake);
  var label = new PIXI.Text({
    text: lane.isSelf ? 'you' : lane.label,
    style: {
      fill: lane.isSelf || lane.isPacer ? hull : COLORS.white,
      fontSize: 12,
      fontFamily: LABEL_FONT,
      fontWeight: 'bold',
      stroke: { color: COLORS.ink, width: 3 }
    }
  });
  label.anchor.set(0.5, 0);
  rower.addChild(label);

  var current = null;
  var wakeFrame = 0;

  // Renames the boat; its colour stays the lane's (the pacer keeps PACER_HULL).
  rower.setLabel = function (text) {
    label.text = text;
  };

  rower.setScale = function (size) {
    var left = -Math.floor(BOAT_COLS / 2) * size;
    BOAT_POSES.forEach(function (pose) {
      poses[pose].clear();
      paintCells(poses[pose], boatCells(pose), size, function (cell) { return cellColor(cell[2], hull); }, left, 0);
    });
    wake.clear();
    var stern = BOAT_ROWS * size;
    paintCells(wake, [[-1, 0], [1, 0], [-2, 2], [2, 2], [0, 3]], size, function () { return COLORS.wake; }, -Math.floor(size / 2), stern);
    label.y = stern + 4 * size + 2;
    current = null;
  };

  // Shows the pose for this point in the stroke; the wake flickers while rowing.
  rower.setPose = function (pose, rowing) {
    if (pose !== current) {
      BOAT_POSES.forEach(function (name) { poses[name].visible = name === pose; });
      if (rowing && pose === 'catch') wakeFrame++;
      current = pose;
    }
    wake.visible = rowing && wakeFrame % 2 === 0;
  };

  rower.setScale(scale);
  rower.setPose('square', false);
  return rower;
}

// The countdown in the 5x7 pixel font with a hard drop shadow. `text` reads
// and writes the label; the art is only redrawn when it changes.
function createPixelLabel(PIXI) {
  var view = new PIXI.Container();
  var art = new PIXI.Graphics();
  view.addChild(art);
  var text = '';
  var cellSize = 12;
  var label = {
    view: view,
    isPixelLabel: true,
    setCellSize: function (size) {
      if (size === cellSize) return;
      cellSize = size;
      redraw();
    }
  };
  Object.defineProperty(label, 'text', {
    get: function () { return text; },
    set: function (value) {
      var next = value == null ? '' : String(value);
      if (next === text) return;
      text = next;
      redraw();
    }
  });
  view.pixelLabel = label;

  function redraw() {
    art.clear();
    if (!text) return;
    var layout = textCells(text);
    var x0 = -Math.round((layout.width * cellSize) / 2);
    var y0 = -Math.round((GLYPH_ROWS * cellSize) / 2);
    var shadow = Math.max(2, Math.round(cellSize / 3));
    var fill = text === 'GO!' ? COLORS.go : COLORS.countdown;
    paintCells(art, layout.cells, cellSize, function () { return COLORS.ink; }, x0 + shadow, y0 + shadow);
    paintCells(art, layout.cells, cellSize, function () { return fill; }, x0, y0);
  }

  return label;
}
