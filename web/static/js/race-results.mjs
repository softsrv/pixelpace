// Pure helpers for formatting race results and building ranked result rows.

// Formats a duration in milliseconds as m:ss.t (e.g. 95000 -> "1:35.0").
export function formatDuration(milliseconds) {
  var ms = Number(milliseconds);
  if (milliseconds == null || Number.isNaN(ms) || ms < 0) return '';
  var totalTenths = Math.floor(ms / 100);
  var tenths = totalTenths % 10;
  var totalSeconds = Math.floor(totalTenths / 10);
  var s = totalSeconds % 60;
  var m = Math.floor(totalSeconds / 60);
  return m + ':' + String(s).padStart(2, '0') + '.' + tenths;
}

// Formats a pace per 500m. Returns '—' for null, zero, negative or NaN input,
// or when distance is zero (never divides by zero or returns NaN).
export function formatPace(paceOrElapsed, distance) {
  if (distance !== undefined) {
    var dist = Number(distance);
    if (distance == null || Number.isNaN(dist) || dist <= 0) return '—';
    var elapsed = Number(paceOrElapsed);
    if (paceOrElapsed == null || Number.isNaN(elapsed) || elapsed <= 0) return '—';
    var meters = dist > 50000 ? dist / 1000 : dist;
    var paceMs = (elapsed * 500) / meters;
    return formatPace(paceMs);
  }

  var pace = Number(paceOrElapsed);
  if (paceOrElapsed == null || Number.isNaN(pace) || pace <= 0) return '—';
  var totalSeconds = pace > 1000 ? Math.round(pace / 1000) : Math.round(pace);
  var m = Math.floor(totalSeconds / 60);
  var s = totalSeconds % 60;
  return m + ':' + String(s).padStart(2, '0');
}

// Formats a distance in metres (e.g. 500 -> "500 m").
export function formatDistance(meters) {
  var m = Number(meters);
  if (meters == null || Number.isNaN(m) || m < 0) return '0 m';
  return Math.round(m) + ' m';
}

// Builds result rows numbering finishers 1..n with time as m:ss.t, distance in
// metres and pace per 500m. DNF rows follow finishers with no rank or finish
// time, marked DNF with their distance and pace. Flags the viewer's own row.
// If there are no finishers, message is 'No one finished'.
export function resultRows(data, viewerId) {
  var list = [];
  if (Array.isArray(data)) {
    list = data;
  } else if (data && Array.isArray(data.results)) {
    list = data.results;
  }

  var finishers = [];
  var dnfs = [];

  list.forEach(function (item) {
    var isDnf = String(item.status || '').toLowerCase() === 'dnf';
    if (isDnf) {
      dnfs.push(item);
    } else {
      finishers.push(item);
    }
  });

  var rows = [];
  var message = finishers.length === 0 ? 'No one finished' : null;

  finishers.forEach(function (item, index) {
    var elapsed = item.elapsed_milliseconds != null ? item.elapsed_milliseconds
      : (item.elapsed_ms != null ? item.elapsed_ms : item.elapsed);
    var meters = item.distance_m != null ? item.distance_m
      : (item.distance_millimeters != null ? item.distance_millimeters / 1000
      : (item.distance_mm != null ? item.distance_mm / 1000
      : (item.distance != null ? (item.distance > 50000 ? item.distance / 1000 : item.distance) : 0)));
    var pace = item.pace_milliseconds_per_500m != null ? item.pace_milliseconds_per_500m
      : (item.pace_ms_per_500m != null ? item.pace_ms_per_500m
      : (item.pace_ms != null ? item.pace_ms : item.pace));
    if (pace == null && meters > 0 && elapsed > 0) {
      pace = (elapsed * 500) / meters;
    }
    var userId = item.user_id || item.userId || item.id;
    var isSelf = Boolean(viewerId && userId && String(userId) === String(viewerId));

    rows.push({
      rank: item.rank != null ? item.rank : (index + 1),
      name: item.name || item.displayName || item.display_name || '',
      time: formatDuration(elapsed),
      distance: formatDistance(meters),
      pace: formatPace(pace),
      status: item.status || 'finished',
      isSelf: isSelf,
      userId: userId
    });
  });

  dnfs.forEach(function (item) {
    var meters = item.distance_m != null ? item.distance_m
      : (item.distance_millimeters != null ? item.distance_millimeters / 1000
      : (item.distance_mm != null ? item.distance_mm / 1000
      : (item.distance != null ? (item.distance > 50000 ? item.distance / 1000 : item.distance) : 0)));
    var pace = item.pace_milliseconds_per_500m != null ? item.pace_milliseconds_per_500m
      : (item.pace_ms_per_500m != null ? item.pace_ms_per_500m
      : (item.pace_ms != null ? item.pace_ms : item.pace));
    var elapsed = item.elapsed_milliseconds != null ? item.elapsed_milliseconds
      : (item.elapsed_ms != null ? item.elapsed_ms : item.elapsed);
    if (pace == null && meters > 0 && elapsed > 0) {
      pace = (elapsed * 500) / meters;
    }
    var userId = item.user_id || item.userId || item.id;
    var isSelf = Boolean(viewerId && userId && String(userId) === String(viewerId));

    rows.push({
      rank: null,
      name: item.name || item.displayName || item.display_name || '',
      time: '',
      distance: formatDistance(meters),
      pace: formatPace(pace),
      status: 'dnf',
      isSelf: isSelf,
      userId: userId
    });
  });

  var result = rows.slice();
  result.rows = rows;
  result.message = message;
  return result;
}
