// Pixel-art data and timing for the race view, kept free of PixiJS so it can
// be unit-tested in Node. Sprites are small grids of characters, one character
// per art pixel ('.' is transparent); race.mjs draws them as squares of
// `scale` screen pixels, which keeps every edge crisp.

// ── Palette (Dracula, matching the landing page) ─────────────────────────────

export var COLORS = {
  water: 0x10183c,
  waterBand: 0x15204a,
  ripple: 0x2c3b80,
  buoyRed: 0xff5555,
  buoyWhite: 0xf8f8f2,
  ink: 0x0d0b1e,
  white: 0xf8f8f2,
  oar: 0xe9e9f0,
  blade: 0x8be9fd,
  skin: 0xffcf9e,
  hair: 0x44475a,
  rigger: 0x6272a4,
  wake: 0xbfefff,
  countdown: 0xf1fa8c,
  go: 0x50fa7b
};

// Our own boat is pink, like "YOU" on the landing page; rivals take the other
// Dracula accents in lane order.
export var SELF_HULL = 0xff79c6;
export var PACER_HULL = 0x6272a4;
var RIVAL_HULLS = [0x8be9fd, 0x50fa7b, 0xffb86c, 0xbd93f9, 0xf1fa8c, 0xff5555, 0xf8f8f2];

export function hullColor(isSelf, rivalIndex) {
  if (isSelf) return SELF_HULL;
  var i = Math.max(0, Math.floor(Number(rivalIndex) || 0));
  return RIVAL_HULLS[i % RIVAL_HULLS.length];
}

// ── Top-down single scull, bow up ────────────────────────────────────────────
//
// H hull, h hull shading, S shirt (hull colour), K hair, R rigger, O oar,
// B blade. The bow is row 0, so the sprite's origin is the bow tip. The rower
// faces the stern (down): at the catch the blades reach toward the bow, at the
// finish they have swept back toward the stern.

var HULL_ROWS = [
  '.......H.......',
  '.......H.......',
  '......HHH......',
  '......HhH......',
  '......HhH......',
  '......HhH......',
  '......HhH......',
  '......HhH......',
  '......HHH......',
  '......SKS......',
  '....RSSSSSR....',
  '......SSS......',
  '......HHH......',
  '......HhH......',
  '......HhH......',
  '......HhH......',
  '......HhH......',
  '......HHH......',
  '.......H.......',
  '.......H.......'
];

// Oar overlays, one per pose; mirrored left/right around the centre column.
var OARS = {
  catch: [[3, 9, 'O'], [2, 8, 'O'], [1, 7, 'B'], [0, 6, 'B'], [1, 6, 'B']],
  square: [[3, 10, 'O'], [2, 10, 'O'], [1, 10, 'B'], [0, 10, 'B'], [0, 9, 'B']],
  finish: [[3, 11, 'O'], [2, 12, 'O'], [1, 13, 'B'], [0, 14, 'B'], [1, 14, 'B']]
};

export var BOAT_COLS = HULL_ROWS[0].length;
export var BOAT_ROWS = HULL_ROWS.length;
export var BOAT_POSES = ['catch', 'square', 'finish'];

// The cells of one pose as [col, row, key] triples.
export function boatCells(pose) {
  var oars = OARS[pose];
  if (!oars) throw new Error('unknown pose: ' + pose);
  var cells = [];
  HULL_ROWS.forEach(function (row, y) {
    for (var x = 0; x < row.length; x++) {
      if (row[x] !== '.') cells.push([x, y, row[x]]);
    }
  });
  oars.forEach(function (cell) {
    cells.push([cell[0], cell[1], cell[2]]);
    cells.push([BOAT_COLS - 1 - cell[0], cell[1], cell[2]]);
  });
  return cells;
}

// Colour for a sprite key; the hull keys take the boat's own colour.
export function cellColor(key, hull) {
  switch (key) {
    case 'H': case 'S': return hull;
    case 'h': return shade(hull, 0.72);
    case 'K': return COLORS.hair;
    case 'R': return COLORS.rigger;
    case 'O': return COLORS.oar;
    case 'B': return COLORS.blade;
    default: return COLORS.white;
  }
}

// Darkens a 0xRRGGBB colour by `factor` (0..1).
export function shade(color, factor) {
  var r = Math.round(((color >> 16) & 0xff) * factor);
  var g = Math.round(((color >> 8) & 0xff) * factor);
  var b = Math.round((color & 0xff) * factor);
  return (r << 16) | (g << 8) | b;
}

// ── Stroke timing ────────────────────────────────────────────────────────────
//
// A stroke is a phase from 0 to 1. The drive is quick and the recovery slow,
// as on the water: catch → drive (oars square) → finish → recovery (oars
// square, travelling back up the slide) → catch.

export function strokePose(phase) {
  var p = ((Number(phase) % 1) + 1) % 1;
  if (p < 0.12) return 'catch';
  if (p < 0.24) return 'square';
  if (p < 0.44) return 'finish';
  if (p < 0.76) return 'square';
  return 'catch';
}

// Advances a stroke phase by `elapsedMs` at `strokeRate` strokes per minute.
// A racer who has stopped (or not started) holds their pose.
export function advanceStroke(phase, strokeRate, elapsedMs) {
  var rate = Number(strokeRate);
  var dt = Number(elapsedMs);
  if (!(rate > 0) || !(dt > 0)) return phase;
  return (phase + (rate * dt) / 60000) % 1;
}

// Screen pixels per art pixel for a lane `laneWidth` wide: the boat plus a
// little water on each side, between 2 and 4.
export function pixelScale(laneWidth) {
  var fit = Math.floor(Number(laneWidth) / (BOAT_COLS + 2));
  if (!(fit > 2)) return 2;
  return Math.min(4, fit);
}

// Rounds a screen coordinate to the art-pixel grid so sprites never sit
// between pixels.
export function snap(value, scale) {
  return Math.round(value / scale) * scale;
}

// ── 5×7 pixel font for the countdown ─────────────────────────────────────────

var GLYPHS = {
  '0': ['01110', '10001', '10011', '10101', '11001', '10001', '01110'],
  '1': ['00100', '01100', '00100', '00100', '00100', '00100', '01110'],
  '2': ['01110', '10001', '00001', '00110', '01000', '10000', '11111'],
  '3': ['11110', '00001', '00001', '01110', '00001', '00001', '11110'],
  '4': ['00010', '00110', '01010', '10010', '11111', '00010', '00010'],
  '5': ['11111', '10000', '11110', '00001', '00001', '10001', '01110'],
  '6': ['00110', '01000', '10000', '11110', '10001', '10001', '01110'],
  '7': ['11111', '00001', '00010', '00100', '01000', '01000', '01000'],
  '8': ['01110', '10001', '10001', '01110', '10001', '10001', '01110'],
  '9': ['01110', '10001', '10001', '01111', '00001', '00010', '01100'],
  'G': ['01111', '10000', '10000', '10111', '10001', '10001', '01111'],
  'O': ['01110', '10001', '10001', '10001', '10001', '10001', '01110'],
  '!': ['1', '1', '1', '1', '1', '0', '1']
};

export var GLYPH_ROWS = 7;

// Lays `text` out in the pixel font: the lit cells as [col, row] pairs and the
// total width in cells, with one blank column between glyphs. Characters the
// font lacks are skipped.
export function textCells(text) {
  var cells = [];
  var x = 0;
  String(text == null ? '' : text).toUpperCase().split('').forEach(function (ch) {
    var glyph = GLYPHS[ch];
    if (!glyph) return;
    glyph.forEach(function (row, y) {
      for (var i = 0; i < row.length; i++) {
        if (row[i] === '1') cells.push([x + i, y]);
      }
    });
    x += glyph[0].length + 1;
  });
  return { cells: cells, width: Math.max(0, x - 1) };
}
