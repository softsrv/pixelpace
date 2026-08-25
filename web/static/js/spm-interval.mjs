var SPM_INTERVAL_LOW = 16;
var SPM_INTERVAL_HIGH = 40;
var SPM_INTERVAL_SLOW_MS = 1000;
var SPM_INTERVAL_FAST_MS = 200;

export function spmToIntervalMs(strokeRate) {
  var spm = Number(strokeRate) || 0;
  if (spm <= SPM_INTERVAL_LOW) return SPM_INTERVAL_SLOW_MS;
  if (spm >= SPM_INTERVAL_HIGH) return SPM_INTERVAL_FAST_MS;

  return Math.round(SPM_INTERVAL_SLOW_MS - ((spm - SPM_INTERVAL_LOW) / (SPM_INTERVAL_HIGH - SPM_INTERVAL_LOW)) * (SPM_INTERVAL_SLOW_MS - SPM_INTERVAL_FAST_MS));
}
