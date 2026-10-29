import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';

var appJs = readFileSync(new URL('./app.js', import.meta.url), 'utf8');

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

function runDashboardApp(devMode = true, protocol = 'https:') {
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
  var click;
  connectBtn.addEventListener = function (type, fn) { if (type === 'click') click = fn; };
  var rowingMachineCard = metricElement();
  rowingMachineCard.getAttribute = function (name) {
    return name === 'data-dev-mode' && devMode ? 'true' : 'false';
  };
  var draws = 0;
  var rowerCanvas = metricElement();
  rowerCanvas.width = 320;
  rowerCanvas.height = 180;
  rowerCanvas.getContext = function () {
    return {
      fillRect: function () { draws++; },
      beginPath: function () {},
      moveTo: function () {},
      lineTo: function () {},
      closePath: function () {},
      fill: function () {},
      stroke: function () {},
      arc: function () {}
    };
  };
  var rosterEl = metricElement();
  rosterEl.classList = { add: function () { this.hidden = true; }, remove: function () { this.hidden = false; }, hidden: true };
  var byID = {
    'race-roster': rosterEl,
    'bt-connect-btn': connectBtn,
    'rowing-machine-card': rowingMachineCard,
    'bt-connection-info': metricElement(),
    'bt-metrics': metricsRegion,
    'bt-rower-canvas': rowerCanvas,
    // The development-mode simulator panel dashboard.html and race.html render.
    'sim-panel': metricElement(),
    'sim-spm': Object.assign(metricElement(), { value: '26' }),
    'sim-pace': Object.assign(metricElement(), { value: '2:12' }),
    'sim-start': Object.assign(metricElement(), { textContent: 'Stop rowing' })
  };
  var sockets = [];
  var constructorFails = false;
  class MockWebSocket {
    static CONNECTING = 0;
    static OPEN = 1;
    static CLOSING = 2;
    static CLOSED = 3;

    constructor(url) {
      if (constructorFails) throw new Error('Connection unavailable');
      this.url = url;
      this.readyState = MockWebSocket.CONNECTING;
      this.frames = [];
      this.onmessage = null;
      this.failAt = -1;
      sockets.push(this);
    }

    send(frame) {
      assert.equal(this.readyState, MockWebSocket.OPEN);
      assert.equal(typeof frame, 'string', 'telemetry uses text frames');
      if (this.frames.length === this.failAt) throw new Error('Send failed');
      this.frames.push(frame);
    }

    open() {
      assert.equal(this.readyState, MockWebSocket.CONNECTING, 'closed sockets cannot reopen');
      this.readyState = MockWebSocket.OPEN;
      this.onopen();
    }

    close() {
      this.readyState = MockWebSocket.CLOSING;
    }

    finishClose() {
      this.readyState = MockWebSocket.CLOSED;
      this.onclose();
    }
  }
  var now = '2026-10-08T12:00:00.000Z';
  var dateReads = 0;
  class SampleDate {
    toISOString() { dateReads++; return now; }
  }
  var timers = [];
  var timeouts = [];
  var notifications = {};
  var documentListeners = {};
  var dispatched = [];
  class MockCustomEvent {
    constructor(type, init) {
      this.type = type;
      this.detail = init && init.detail;
    }
  }
  var window = { isSecureContext: true, location: { protocol: protocol, host: 'pixelpace.test:8080' } };
  runInNewContext(appJs, {
    document: {
      addEventListener: function (type, fn) {
        if (!documentListeners[type]) documentListeners[type] = [];
        documentListeners[type].push(fn);
      },
      dispatchEvent: function (evt) {
        dispatched.push(evt);
        (documentListeners[evt.type] || []).forEach(function (fn) { fn(evt); });
        return true;
      },
      body: { addEventListener: function () {} },
      getElementById: function (id) { return byID[id] || null; },
      querySelector: function () { return null; }
    },
    window: window,
    navigator: {
      bluetooth: {
        requestDevice: async function () {
          return {
            name: 'PM5',
            addEventListener: function () {},
            gatt: {
              connect: async function () {
                return {
                  connected: true,
                  getPrimaryService: async function () {
                    return {
                      getCharacteristic: async function (uuid) {
                        return {
                          startNotifications: async function () {},
                          addEventListener: function (type, fn) { notifications[uuid] = fn; }
                        };
                      }
                    };
                  }
                };
              }
            }
          };
        }
      }
    },
    htmx: { trigger: function () {} },
    Math: Math,
    Number: Number,
    Object: Object,
    String: String,
    WebSocket: MockWebSocket,
    CustomEvent: MockCustomEvent,
    Date: SampleDate,
    JSON: JSON,
    setInterval: function (fn, delay) {
      timers.push({ fn: fn, delay: delay, active: true });
      return timers.length;
    },
    clearInterval: function (id) { timers[id - 1].active = false; },
    setTimeout: function (fn, delay) {
      timeouts.push({ fn: fn, delay: delay, active: true });
      return timeouts.length;
    },
    clearTimeout: function (id) { timeouts[id - 1].active = false; }
  });

  return {
    metricEls: metricEls,
    rosterEl: rosterEl,
    sockets: sockets,
    timers: timers,
    timeouts: timeouts,
    start: window.startRaceStream,
    window: window,
    connectPm5: function () { return click(); },
    notify: function (characteristic, bytes) {
      var uuid = 'ce0600' + characteristic + '-43e5-11e4-916c-0800200c9a66';
      notifications[uuid]({ target: { uuid: uuid, value: new DataView(Uint8Array.from(bytes).buffer) } });
    },
    // One simulator status tick: 500 ms of rowing, three status notifications.
    tick: function () { timers.find(function (timer) { return timer.active && timer.fn.name === 'mockPm5StatusTick'; }).fn(); },
    rowerTimer: function () {
      return timers.find(function (timer) { return timer.active && !/^mockPm5/.test(timer.fn.name); });
    },
    setTime: function (timestamp) { now = timestamp; },
    dateReads: function () { return dateReads; },
    events: function (type) {
      return dispatched.filter(function (evt) { return evt.type === type; }).map(function (evt) { return evt.detail; });
    },
    dispatch: function (type) {
      var evt = new MockCustomEvent(type);
      (documentListeners[type] || []).forEach(function (fn) { fn(evt); });
    },
    draws: function () { return draws; },
    failConstruction: function (fail) { constructorFails = fail; },
    retry: function () {
      var timer = timeouts.find(function (entry) { return entry.active; });
      assert.ok(timer, 'client scheduled a reconnect');
      timer.active = false;
      timer.fn();
    }
  };
}

test('open race socket sends one converted sample per notification while retaining local rendering and animation', function () {
  var app = runDashboardApp();
  assert.equal(app.sockets.length, 0, 'no streaming until a race is active');
  app.tick();
  assert.equal(app.dateReads(), 0);
  var stream = app.start('race/one');
  assert.equal(stream.socket.url, 'wss://pixelpace.test:8080/races/race%2Fone/ws');
  stream.socket.open();
  app.setTime('2026-10-08T12:00:02.000Z');
  app.tick();

  // 1 s at 2:12/500m is 3.79 m, encoded in 0.1 m steps; one sample per status notification.
  assert.equal(stream.socket.frames.length, 3);
  assert.deepEqual(JSON.parse(stream.socket.frames[0]), {
    elapsed_milliseconds: 1000,
    distance_millimeters: 3800,
    stroke_rate: 26,
    power: 0,
    sampled_at: '2026-10-08T12:00:02.000Z'
  });
  assert.equal(app.dateReads(), 3, 'capture time only once per emission');
  assert.equal(stream.buffer.length, 0);
  assert.equal(app.metricEls.elapsedTime.textContent, '0:01');
  assert.equal(app.metricEls.distance.textContent, '4 m');
  var before = app.draws();
  app.rowerTimer().fn();
  assert.equal(app.draws(), before + 1, 'rower animation still runs');
});

test('every real PM5 notification renders and streams a complete latest-reading sample', async function () {
  var app = runDashboardApp(false, 'http:');
  await app.connectPm5();
  var stream = app.start('race-ble');
  assert.equal(stream.socket.url, 'ws://pixelpace.test:8080/races/race-ble/ws');
  stream.socket.open();

  // General status: 1234 hundredths of a second, 427 tenths of a meter.
  app.notify('31', [210, 4, 0, 171, 1, 0]);
  assert.equal(stream.socket.frames.length, 1);
  assert.deepEqual(JSON.parse(stream.socket.frames[0]), {
    elapsed_milliseconds: 12340, distance_millimeters: 42700,
    stroke_rate: 0, power: 0, sampled_at: '2026-10-08T12:00:00.000Z'
  });
  assert.equal(app.metricEls.distance.textContent, '43 m');
  assert.equal(app.metricEls.elapsedTime.textContent, '0:12');

  // Additional status 1: elapsed 12.35s and 27 SPM; additional stroke data: 183 W.
  app.notify('32', [211, 4, 0, 0, 0, 27, 142, 144, 51]);
  assert.equal(stream.socket.frames.length, 2);
  assert.equal(JSON.parse(stream.socket.frames[1]).stroke_rate, 27);
  assert.equal(app.metricEls.strokeRate.textContent, '27 spm');
  assert.equal(app.timers.filter(function (timer) { return timer.active; }).length, 1);
  app.notify('36', [0, 0, 0, 183, 0, 0, 0, 6, 0]);
  assert.equal(stream.socket.frames.length, 3);
  assert.deepEqual(JSON.parse(stream.socket.frames[2]), {
    elapsed_milliseconds: 12350, distance_millimeters: 42700,
    stroke_rate: 27, power: 183, sampled_at: '2026-10-08T12:00:00.000Z'
  });
  assert.equal(app.metricEls.power.textContent, '183 W');
  app.notify('31', []);
  assert.equal(stream.socket.frames.length, 3, 'empty notifications are not samples');
});

test('connecting, closing, and closed sockets buffer each emission with its own timestamp', function () {
  var app = runDashboardApp();
  var stream = app.start('race-one');
  var timestamps = ['2026-10-08T12:00:01.000Z', '2026-10-08T12:00:02.000Z', '2026-10-08T12:00:03.000Z'];
  timestamps.forEach(function (timestamp, index) {
    if (index === 1) stream.socket.close();
    if (index === 2) stream.socket.finishClose();
    app.setTime(timestamp);
    app.tick();
  });
  assert.equal(stream.socket.frames.length, 0);
  assert.ok(Array.isArray(stream.buffer));
  assert.equal(stream.buffer.length, 9);
  assert.deepEqual(Array.from(stream.buffer, function (sample) { return sample.sampled_at; }), timestamps.flatMap(function (t) { return [t, t, t]; }));
  assert.equal(app.dateReads(), 9);
  assert.equal(app.metricEls.elapsedTime.textContent, '0:01', 'disconnected rendering continues');
  assert.equal(app.metricEls.distance.textContent, '6 m');
});

test('reconnect alone synchronously replays the whole ordered batch with original timestamps and clears it', function () {
  var app = runDashboardApp();
  var stream = app.start('race-one');
  var firstSocket = stream.socket;
  firstSocket.open();
  firstSocket.finishClose();
  ['2026-10-08T12:00:01.000Z', '2026-10-08T12:00:02.000Z', '2026-10-08T12:00:03.000Z'].forEach(function (timestamp) {
    app.setTime(timestamp);
    app.tick();
  });
  var original = Array.from(stream.buffer, function (sample) { return JSON.stringify(sample); });
  var buffer = stream.buffer;
  assert.equal(firstSocket.frames.length, 0);
  app.setTime('2026-10-08T12:05:00.000Z');
  app.retry();
  var reconnected = stream.socket;
  assert.notEqual(reconnected, firstSocket, 'reconnect creates a new native socket');
  assert.equal(reconnected.frames.length, 0);
  assert.equal(typeof firstSocket.onmessage, 'function');
  assert.equal(typeof reconnected.onmessage, 'function', 'every socket handles inbound frames');
  reconnected.open();

  // All frames are present before onopen returns: no extra timer or inbound message.
  assert.deepEqual(reconnected.frames, original);
  assert.deepEqual(reconnected.frames.map(function (frame) { return JSON.parse(frame).elapsed_milliseconds; }), [500, 500, 500, 1000, 1000, 1000, 1500, 1500, 1500]);
  assert.equal(stream.buffer, buffer, 'drain the inspectable array in place');
  assert.equal(buffer.length, 0);
  assert.equal(app.dateReads(), 9, 'replay does not re-timestamp samples');
  reconnected.frames.forEach(function (frame) {
    assert.notEqual(JSON.parse(frame).sampled_at, '2026-10-08T12:05:00.000Z');
  });
  app.tick();
  assert.equal(reconnected.frames.length, 12);
  assert.equal(JSON.parse(reconnected.frames[9]).sampled_at, '2026-10-08T12:05:00.000Z');
  assert.equal(JSON.parse(reconnected.frames[9]).elapsed_milliseconds, 2000);
  assert.equal(buffer.length, 0);
});

test('real BLE samples also buffer while disconnected and retain their original readings on replay', async function () {
  var app = runDashboardApp(false);
  await app.connectPm5();
  var stream = app.start('race-ble');
  stream.socket.finishClose();
  app.setTime('2026-10-08T12:00:01.000Z');
  app.notify('31', [210, 4, 0, 171, 1, 0]);
  app.setTime('2026-10-08T12:00:02.000Z');
  app.notify('32', [211, 4, 0, 0, 0, 27]);
  assert.equal(stream.socket.frames.length, 0);
  assert.equal(stream.buffer.length, 2);
  assert.equal(stream.buffer[0].stroke_rate, 0, 'later readings must not mutate buffered samples');
  assert.equal(stream.buffer[1].stroke_rate, 27);
  assert.equal(app.metricEls.strokeRate.textContent, '27 spm');
  app.setTime('2026-10-08T12:05:00.000Z');
  app.retry();
  stream.socket.open();
  assert.deepEqual(stream.socket.frames.map(function (frame) { return JSON.parse(frame).sampled_at; }), [
    '2026-10-08T12:00:01.000Z', '2026-10-08T12:00:02.000Z'
  ]);
  assert.equal(stream.buffer.length, 0);
});

test('a send failure retains only the unsent suffix for the next reconnect without interrupting rendering', function () {
  var app = runDashboardApp();
  var stream = app.start('race-one');
  app.tick();
  app.tick();
  app.tick();
  var original = Array.from(stream.buffer, function (sample) { return JSON.stringify(sample); });
  var firstSocket = stream.socket;
  firstSocket.failAt = 1;
  firstSocket.open();
  assert.deepEqual(firstSocket.frames, original.slice(0, 1));
  assert.equal(stream.buffer.length, 8);
  app.tick();
  assert.equal(app.metricEls.elapsedTime.textContent, '0:02');
  assert.equal(stream.buffer.length, 11);
  firstSocket.finishClose();
  app.retry();
  stream.socket.open();
  assert.deepEqual(stream.socket.frames.slice(0, 8), original.slice(1));
  assert.equal(stream.socket.frames.length, 11);
  assert.equal(stream.buffer.length, 0);
});

test('socket errors retry with bounded backoff, reset on open, and stop cancels reconnection', function () {
  var app = runDashboardApp();
  var stream = app.start('race-one');
  stream.socket.onerror();
  assert.equal(stream.socket.readyState, 2);
  stream.socket.finishClose();
  assert.equal(app.timeouts[0].delay, 1000);
  app.failConstruction(true);
  for (var i = 0; i < 6; i++) app.retry();
  assert.deepEqual(app.timeouts.map(function (timer) { return timer.delay; }), [1000, 2000, 4000, 8000, 16000, 30000, 30000]);
  app.tick();
  assert.equal(stream.buffer.length, 3);
  app.failConstruction(false);
  app.retry();
  stream.socket.open();
  assert.equal(stream.buffer.length, 0);
  stream.socket.finishClose();
  assert.equal(app.timeouts.at(-1).delay, 1000);
  stream.stop();
  assert.equal(app.timeouts.filter(function (timer) { return timer.active; }).length, 0);
  stream.socket.finishClose();
  assert.equal(app.timeouts.filter(function (timer) { return timer.active; }).length, 0);
  app.tick();
  assert.equal(stream.buffer.length, 0, 'stopped races do not accumulate samples');
});

test('samples buffer even when socket construction fails before a socket exists', function () {
  var app = runDashboardApp();
  app.failConstruction(true);
  var stream = app.start('race-one');
  assert.equal(stream.socket, null);
  app.setTime('2026-10-08T12:00:01.000Z');
  app.tick();
  assert.equal(app.sockets.length, 0);
  assert.equal(stream.buffer.length, 3);
  assert.equal(stream.buffer[0].sampled_at, '2026-10-08T12:00:01.000Z');
  app.failConstruction(false);
  app.setTime('2026-10-08T12:05:00.000Z');
  app.retry();
  stream.socket.open();
  assert.equal(stream.socket.frames.length, 3);
  assert.equal(JSON.parse(stream.socket.frames[0]).sampled_at, '2026-10-08T12:00:01.000Z');
  assert.equal(stream.buffer.length, 0);
});

test('starting the same race is idempotent and switching races cannot replay an old race buffer', function () {
  var app = runDashboardApp();
  assert.throws(function () { app.start(''); }, /race id/);
  assert.equal(app.sockets.length, 0);
  var first = app.start('race-one');
  assert.equal(app.start('race-one'), first);
  assert.equal(app.sockets.length, 1);
  app.tick();
  first.socket.finishClose();
  var second = app.start('race-two');
  assert.equal(first.socket.readyState, 2);
  first.socket.onopen();
  first.socket.onclose();
  assert.equal(app.timeouts.filter(function (timer) { return timer.active; }).length, 0);
  second.socket.open();
  assert.equal(second.socket.frames.length, 0);
  assert.equal(first.buffer.length, 3);
  first.stop();
  app.tick();
  assert.equal(second.socket.frames.length, 3, 'a stale stop cannot stop the current race');
});

function rosterFrame(players) {
  return { data: JSON.stringify({ type: 'roster', players: players }) };
}

test('a roster frame populates stream.roster, nameFor and the rendered roster; reconnect replaces it', function () {
  var app = runDashboardApp();
  var stream = app.start('race-one');
  var first = stream.socket;
  first.open();
  assert.equal(stream.nameFor('u1'), null);
  first.onmessage(rosterFrame([{ user_id: 'u1', name: 'Ada' }, { user_id: 'u2', name: 'Player abc' }]));
  assert.equal(stream.nameFor('u1'), 'Ada');
  assert.equal(stream.nameFor('u2'), 'Player abc');
  assert.equal(stream.roster.u1, 'Ada');
  assert.equal(app.rosterEl.textContent, 'Racers: Ada, Player abc', 'names are rendered, not just stored');
  assert.equal(app.rosterEl.classList.hidden, false);

  first.finishClose();
  app.retry();
  var second = stream.socket;
  assert.notEqual(second, first);
  second.open();
  second.onmessage(rosterFrame([{ user_id: 'u3', name: 'Grace' }]));
  assert.equal(stream.nameFor('u1'), null, 'old names are gone');
  assert.equal(stream.nameFor('u3'), 'Grace');
  assert.equal(app.rosterEl.textContent, 'Racers: Grace');
  first.onmessage(rosterFrame([{ user_id: 'u9', name: 'Stale' }]));
  assert.equal(stream.nameFor('u9'), null, 'a superseded socket cannot change the roster');
});

test('a roster id of __proto__ is an own key and cannot reach the prototype', function () {
  var app = runDashboardApp();
  var stream = app.start('race-one');
  stream.socket.open();
  stream.socket.onmessage(rosterFrame([{ user_id: '__proto__', name: 'Eve' }]));
  assert.equal(stream.nameFor('__proto__'), 'Eve');
  assert.equal(stream.nameFor('toString'), null);
  assert.equal(stream.nameFor('constructor'), null);
});

test('inbound telemetry is recorded on stream.racers labelled with the roster name only', function () {
  var app = runDashboardApp();
  var stream = app.start('race-one');
  var socket = stream.socket;
  socket.open();
  socket.onmessage({ data: JSON.stringify({ user_id: 'u1', distance_millimeters: 5, email: 'a@b.c' }) });
  assert.equal(stream.racers.u1.name, null, 'no roster yet: no name, never an email');
  assert.equal(stream.racers.u1.distance_millimeters, 5);
  socket.onmessage(rosterFrame([{ user_id: 'u1', name: 'Ada' }]));
  assert.equal(stream.racers.u1.name, 'Ada', 'existing racers are relabelled');
  socket.onmessage({ data: JSON.stringify({ user_id: 'u1', distance_millimeters: 9 }) });
  assert.equal(stream.racers.u1.distance_millimeters, 9);
  assert.equal(stream.racers.u1.name, 'Ada');
});

test('malformed inbound frames are ignored and leave buffering and replay unaffected', function () {
  var app = runDashboardApp();
  var stream = app.start('race-one');
  var socket = stream.socket;
  socket.open();
  ['not json', 'null', '42', '[]', '{}', '{"user_id":7}', '{"type":"roster","players":"x"}', undefined].forEach(function (data) {
    socket.onmessage({ data: data });
  });
  socket.onmessage(undefined);
  assert.equal(Object.keys(stream.racers).length, 0);
  assert.equal(Object.keys(stream.roster).length, 0);
  socket.finishClose();
  app.setTime('2026-10-08T12:00:01.000Z');
  app.tick();
  assert.equal(stream.buffer.length, 3);
  app.retry();
  stream.socket.open();
  assert.equal(stream.socket.frames.length, 3);
  assert.equal(JSON.parse(stream.socket.frames[0]).sampled_at, '2026-10-08T12:00:01.000Z');
  assert.equal(stream.buffer.length, 0);
});

test('broadcast frames from the current socket are re-dispatched as race telemetry', function () {
  var app = runDashboardApp();
  var stream = app.start('race-one');
  var firstSocket = stream.socket;
  firstSocket.open();
  var sample = { user_id: 'rival', distance_millimeters: 120000, elapsed_milliseconds: 30000 };

  firstSocket.onmessage({ data: JSON.stringify(sample) });
  firstSocket.onmessage({ data: 'not json' });

  var dispatched = JSON.parse(JSON.stringify(app.events('pixelpace:race-telemetry')));
  assert.deepEqual(dispatched, [Object.assign({ name: null }, sample)], 'the stored racer, labelled from the roster');
  firstSocket.finishClose();
  app.retry();
  firstSocket.onmessage({ data: JSON.stringify({ user_id: 'stale' }) });
  assert.equal(app.events('pixelpace:race-telemetry').length, 1, 'a replaced socket no longer dispatches');
  stream.stop();
  stream.socket.onmessage({ data: JSON.stringify({ user_id: 'stopped' }) });
  assert.equal(app.events('pixelpace:race-telemetry').length, 1, 'a stopped stream no longer dispatches');
});

test('a finished frame dispatches pixelpace:race-finished and no telemetry, and window.stopRaceStream closes the active stream', function () {
  var app = runDashboardApp();
  var stream = app.start('race-one');
  var socket = stream.socket;
  socket.open();

  socket.onmessage({ data: JSON.stringify({ type: 'finished' }) });
  assert.equal(app.events('pixelpace:race-finished').length, 1, 'exactly one race-finished event');
  assert.equal(app.events('pixelpace:race-telemetry').length, 0, 'no race-telemetry event from finished frame');

  socket.finishClose();
  app.retry();
  socket.onmessage({ data: JSON.stringify({ type: 'finished' }) });
  assert.equal(app.events('pixelpace:race-finished').length, 1, 'stale socket dispatches none');

  var currentStream = app.window.startRaceStream('race-one');
  var currentSocket = currentStream.socket;
  currentSocket.open();
  assert.equal(typeof app.window.stopRaceStream, 'function');
  assert.equal(currentSocket.readyState, 1);
  app.window.stopRaceStream();
  assert.equal(currentSocket.readyState, 2, 'active socket was closed by stopRaceStream');
  currentSocket.onmessage({ data: JSON.stringify({ type: 'finished' }) });
  assert.equal(app.events('pixelpace:race-finished').length, 1, 'stopped stream dispatches none');

  // Calling stopRaceStream when no active stream is running is a safe no-op.
  assert.doesNotThrow(function () {
    app.window.stopRaceStream();
  });
});

test('each streamed sample is also dispatched locally, only while a race is active', function () {
  var app = runDashboardApp();
  app.tick();
  assert.equal(app.events('pixelpace:local-telemetry').length, 0, 'no local race telemetry before a race');
  var stream = app.start('race-one');
  stream.socket.finishClose();
  app.setTime('2026-10-08T12:00:02.000Z');
  app.tick();

  var local = app.events('pixelpace:local-telemetry');
  assert.equal(local.length, 3, 'dispatched even while disconnected, once per notification');
  assert.deepEqual(local[0], stream.buffer[0]);
});

test('the race-started event measures streamed samples from the race start', function () {
  var app = runDashboardApp();
  app.tick();
  app.tick();
  var stream = app.start('race-one');
  stream.socket.open();

  app.dispatch('pixelpace:race-started');
  app.tick();

  var frames = stream.socket.frames.map(function (frame) { return JSON.parse(frame); });
  // Baseline 1.0 s / 3.8 m; after one more 500 ms tick the machine reads 1.5 s / 5.7 m.
  assert.deepEqual(frames.map(function (f) { return [f.elapsed_milliseconds, f.distance_millimeters]; }), [[500, 1900], [500, 1900], [500, 1900]]);
  assert.equal(app.metricEls.distance.textContent, '6 m', 'the dashboard metrics keep showing the machine total');
});

test('simulated race samples carry the simulator state decoded at 0.01 s and 0.1 m', function () {
  var app = runDashboardApp();
  var stream = app.start('race-one');
  stream.socket.open();
  var n = 25;
  for (var i = 0; i < n; i++) app.tick();
  app.timers.find(function (timer) { return timer.active && timer.fn.name === 'mockPm5StrokeTick'; }).fn();

  var seconds = n * 0.5;
  var metres = seconds * 500 / 132;
  var watts = 2.8 / Math.pow(132 / 500, 3);
  var frames = stream.socket.frames.map(function (frame) { return JSON.parse(frame); });
  assert.equal(frames.length, 3 * n + 2, 'one sample per notification: 3 per status tick, 2 per stroke');
  var last = frames[frames.length - 1];
  assert.equal(last.elapsed_milliseconds, Math.round(seconds * 100) * 10, 'elapsed at 0.01 s');
  assert.equal(last.distance_millimeters, Math.round(metres * 10) * 100, 'distance at 0.1 m');
  assert.equal(last.stroke_rate, 26);
  assert.equal(last.power, Math.round(watts), 'stroke power decoded from 0x36');
  assert.equal(app.metricEls.distance.textContent, Math.round(Math.round(metres * 10) / 10) + ' m');
  assert.equal(app.metricEls.power.textContent, Math.round(watts) + ' W');
});

test('the dev mock reports a connected rowing machine', function () {
  var app = runDashboardApp();
  assert.deepEqual(JSON.parse(JSON.stringify(app.events('pixelpace:pm5-status'))), [{ connected: true }]);
  assert.equal(app.window.isPm5Connected(), true);
  var real = runDashboardApp(false);
  assert.equal(real.events('pixelpace:pm5-status').length, 0);
  assert.equal(real.window.isPm5Connected(), false);
});

test('the race-started event does not touch real PM5 readings', function () {
  var app = runDashboardApp(false);
  var stream = app.start('race-one');
  stream.socket.open();
  app.dispatch('pixelpace:race-started');
  assert.equal(stream.socket.frames.length, 0);
});
