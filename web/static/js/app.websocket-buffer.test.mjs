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
  var byID = {
    'bt-connect-btn': connectBtn,
    'rowing-machine-card': rowingMachineCard,
    'bt-connection-info': metricElement(),
    'bt-metrics': metricsRegion,
    'bt-rower-canvas': rowerCanvas
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
  var window = { isSecureContext: true, location: { protocol: protocol, host: 'pixelpace.test:8080' } };
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
    sockets: sockets,
    timers: timers,
    timeouts: timeouts,
    start: window.startRaceStream,
    connectPm5: function () { return click(); },
    notify: function (characteristic, bytes) {
      var uuid = 'ce0600' + characteristic + '-43e5-11e4-916c-0800200c9a66';
      notifications[uuid]({ target: { uuid: uuid, value: new DataView(Uint8Array.from(bytes).buffer) } });
    },
    tick: function () { timers.find(function (timer) { return timer.active && timer.delay === 1000; }).fn(); },
    setTime: function (timestamp) { now = timestamp; },
    dateReads: function () { return dateReads; },
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

test('open race socket sends one converted sample while retaining local rendering and animation', function () {
  var app = runDashboardApp();
  assert.equal(app.sockets.length, 0, 'no streaming until a race is active');
  app.tick();
  assert.equal(app.dateReads(), 0);
  var stream = app.start('race/one');
  assert.equal(stream.socket.url, 'wss://pixelpace.test:8080/races/race%2Fone/ws');
  stream.socket.open();
  app.setTime('2026-10-08T12:00:02.000Z');
  app.tick();

  assert.equal(stream.socket.frames.length, 1);
  assert.deepEqual(JSON.parse(stream.socket.frames[0]), {
    elapsed_milliseconds: 14000,
    distance_millimeters: 59333,
    stroke_rate: 26,
    power: 180,
    sampled_at: '2026-10-08T12:00:02.000Z'
  });
  assert.equal(app.dateReads(), 1, 'capture time only once per emission');
  assert.equal(stream.buffer.length, 0);
  assert.equal(app.metricEls.elapsedTime.textContent, '0:14');
  assert.equal(app.metricEls.distance.textContent, '59 m');
  var before = app.draws();
  app.timers.find(function (timer) { return timer.active && timer.delay !== 1000; }).fn();
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
  assert.equal(stream.buffer.length, 3);
  assert.deepEqual(Array.from(stream.buffer, function (sample) { return sample.sampled_at; }), timestamps);
  assert.equal(app.dateReads(), 3);
  assert.equal(app.metricEls.elapsedTime.textContent, '0:15', 'disconnected rendering continues');
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
  assert.equal(firstSocket.onmessage, null);
  assert.equal(reconnected.onmessage, null, 'no inbound control handler is required');
  reconnected.open();

  // All frames are present before onopen returns: no extra timer or inbound message.
  assert.deepEqual(reconnected.frames, original);
  assert.deepEqual(reconnected.frames.map(function (frame) { return JSON.parse(frame).elapsed_milliseconds; }), [13000, 14000, 15000]);
  assert.equal(stream.buffer, buffer, 'drain the inspectable array in place');
  assert.equal(buffer.length, 0);
  assert.equal(app.dateReads(), 3, 'replay does not re-timestamp samples');
  reconnected.frames.forEach(function (frame) {
    assert.notEqual(JSON.parse(frame).sampled_at, '2026-10-08T12:05:00.000Z');
  });
  app.tick();
  assert.equal(reconnected.frames.length, 4);
  assert.equal(JSON.parse(reconnected.frames[3]).sampled_at, '2026-10-08T12:05:00.000Z');
  assert.equal(JSON.parse(reconnected.frames[3]).elapsed_milliseconds, 16000);
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
  assert.equal(stream.buffer.length, 2);
  app.tick();
  assert.equal(app.metricEls.elapsedTime.textContent, '0:16');
  assert.equal(stream.buffer.length, 3);
  firstSocket.finishClose();
  app.retry();
  stream.socket.open();
  assert.deepEqual(stream.socket.frames.slice(0, 2), original.slice(1));
  assert.equal(stream.socket.frames.length, 3);
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
  assert.equal(stream.buffer.length, 1);
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
  assert.equal(stream.buffer.length, 1);
  assert.equal(stream.buffer[0].sampled_at, '2026-10-08T12:00:01.000Z');
  app.failConstruction(false);
  app.setTime('2026-10-08T12:05:00.000Z');
  app.retry();
  stream.socket.open();
  assert.equal(stream.socket.frames.length, 1);
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
  assert.equal(first.buffer.length, 1);
  first.stop();
  app.tick();
  assert.equal(second.socket.frames.length, 1, 'a stale stop cannot stop the current race');
});
