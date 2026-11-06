// Race track geometry, kept free of PixiJS so it can be unit-tested in Node.

// Share of the race a rower has covered: distance rowed over the race's target
// distance, clamped to [0, 1].
export function progressFraction(distanceMillimeters, targetMillimeters) {
  var distance = Number(distanceMillimeters);
  var target = Number(targetMillimeters);
  if (!(target > 0) || !(distance > 0)) return 0;
  return Math.min(1, distance / target);
}

// Distance covered by a pacer rowing at a steady personal-best pace:
// zero at or before the start, target * elapsed / pacerMs while racing,
// and capped at the target distance once finished. Returns 0 if pacerMs
// is missing or non-positive.
export function pacerDistance(elapsedMs, pacerMs, targetMm) {
  var elapsed = Number(elapsedMs);
  var pacer = Number(pacerMs);
  var target = Number(targetMm);
  if (!(pacer > 0) || !(target > 0) || !(elapsed > 0)) return 0;
  if (elapsed >= pacer) return target;
  return (target * elapsed) / pacer;
}

// Screen y for a rower who has covered `fraction` of the race: the same share of
// the way from the start line (bottom, larger y) to the finish line (top).
export function trackY(fraction, startY, finishY) {
  return startY - fraction * (startY - finishY);
}

// Horizontal centre of lane `index` when the track width is split evenly.
export function laneCenterX(index, laneCount, width) {
  return (width * (index + 0.5)) / laneCount;
}

// How long "GO!" stays on screen after the countdown reaches zero.
export var GO_DISPLAY_MS = 1500;

// The countdown overlay's text with `remainingMs` until the start: whole
// seconds rounded up ("10" … "1"), then "GO!" instead of "0" for a moment
// after the start, then nothing.
// When called as countdownLabel(durationMs, elapsedMs), returns the remaining
// time in m:ss clamped at 0:00.
export function countdownLabel(durationMs, elapsedMs) {
  if (arguments.length <= 1 || elapsedMs === undefined) {
    var remaining = Number(durationMs);
    if (Number.isNaN(remaining)) return '';
    if (remaining > 0) return String(Math.ceil(remaining / 1000));
    return remaining > -GO_DISPLAY_MS ? 'GO!' : '';
  }
  var remaining = Math.min(Number(durationMs), Number(durationMs) - Number(elapsedMs));
  if (Number.isNaN(remaining) || remaining <= 0) return '0:00';
  var totalSeconds = Math.ceil(remaining / 1000);
  var minutes = Math.floor(totalSeconds / 60);
  var seconds = totalSeconds % 60;
  return minutes + ':' + (seconds < 10 ? '0' : '') + seconds;
}
