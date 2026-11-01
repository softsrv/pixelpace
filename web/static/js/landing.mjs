// Landing page PM5 readout: a purely decorative simulated row that ticks the
// LCD values so the monitor looks alive. No network, no Bluetooth.

var monitor = document.querySelector('[data-pp-monitor]');
var reduceMotion = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;

function pad(n, width) {
  var s = String(n);
  while (s.length < width) s = '0' + s;
  return s;
}

// Formats seconds as m:ss.t (e.g. 109.6 -> "1:49.6").
function formatSplit(seconds) {
  var tenths = Math.round(seconds * 10);
  var m = Math.floor(tenths / 600);
  var rest = tenths - m * 600;
  return m + ':' + pad(Math.floor(rest / 10), 2) + '.' + (rest % 10);
}

// Formats seconds as mm:ss.t.
function formatTime(seconds) {
  var tenths = Math.floor(seconds * 10);
  var m = Math.floor(tenths / 600);
  var rest = tenths - m * 600;
  return pad(m, 2) + ':' + pad(Math.floor(rest / 10), 2) + '.' + (rest % 10);
}

if (monitor && !reduceMotion) {
  var el = {
    time: monitor.querySelector('[data-pp-time]'),
    meters: monitor.querySelector('[data-pp-meters]'),
    split: monitor.querySelector('[data-pp-split]'),
    spm: monitor.querySelector('[data-pp-spm]'),
    watts: monitor.querySelector('[data-pp-watts]'),
  };
  var elapsed = 192.4;
  var meters = 876;
  var split = 109.6;
  var spm = 28;
  var tick = 100;

  setInterval(function () {
    elapsed += tick / 1000;
    meters += (500 / split) * (tick / 1000);
    el.time.textContent = formatTime(elapsed);
    el.meters.textContent = pad(Math.floor(meters) % 10000, 4);
  }, tick);

  // Once per stroke, nudge pace, rate and power like a real piece would.
  setInterval(function () {
    split = Math.min(114, Math.max(104, split + (Math.random() - 0.5) * 1.6));
    spm = Math.min(32, Math.max(25, spm + Math.round((Math.random() - 0.5) * 2)));
    // Concept2 power formula: watts = 2.8 / (pace per meter)^3.
    var watts = Math.round(2.8 / Math.pow(split / 500, 3));
    el.split.textContent = formatSplit(split);
    el.spm.textContent = String(spm);
    el.watts.textContent = String(watts);
  }, 2100);
}
