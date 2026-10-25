// Tetris: a 10×20 falling-blocks game on a plugin page.
//
// What it shows:
// - a `canvas` node with `keys` and `frames`, painted with `ui.draw` display lists;
// - `ui.onFrame` for gravity, so the game stops by itself while the page is hidden;
// - `ui.onInput` for the keyboard, plus on-screen buttons for mouse and touch;
// - `ui.sound.tone` for short effects (an operator setting turns them off);
// - the high score in plugin storage, so it survives restarts.
//
// Keys (click the board first): ← → move, ↓ soft drop, ↑ or X rotate clockwise,
// Z rotate anticlockwise, Space hard drop, P pause, Enter start or restart.
const PAGE = 'tetris';
const CANVAS = 'board';
const COLS = 10;
const ROWS = 20;
const CELL = 24;
const SIDE = 132;
const WIDTH = COLS * CELL + SIDE;
const HEIGHT = ROWS * CELL;
const LINE_SCORES = [0, 100, 300, 500, 800];

// Each piece as rows of a square matrix; rotation turns the matrix.
const PIECES = {
  I: { color: '#4fb6d8', cells: ['....', 'IIII', '....', '....'] },
  O: { color: '#e2c044', cells: ['OO', 'OO'] },
  T: { color: '#a77bd6', cells: ['.T.', 'TTT', '...'] },
  S: { color: '#6cbf6a', cells: ['.SS', 'SS.', '...'] },
  Z: { color: '#dc6a5f', cells: ['ZZ.', '.ZZ', '...'] },
  J: { color: '#5a83d6', cells: ['J..', 'JJJ', '...'] },
  L: { color: '#e09a4a', cells: ['..L', 'LLL', '...'] },
};
// Offsets tried when a rotation collides: in place, one step aside, one up, two aside.
// A compact cousin of the standard wall-kick tables that frees pieces from walls and the floor.
const KICKS = [[0, 0], [-1, 0], [1, 0], [0, -1], [-2, 0], [2, 0], [-1, -1], [1, -1]];

const matrix = (kind) => PIECES[kind].cells.map((row) => [...row].map((cell) => cell !== '.'));
const turn = (shape, clockwise) => shape.map((row, y) => row.map((_, x) => (clockwise ? shape[shape.length - 1 - x][y] : shape[x][shape.length - 1 - y])));

let best = Number(await switchboard.storage.get('best')) || 0;
let game = fresh();
let bag = [];
let fallMs = 0;
let shown = '';

function fresh() {
  return { state: 'ready', grid: Array.from({ length: ROWS }, () => Array(COLS).fill('')), piece: null, next: null, score: 0, lines: 0, level: startLevel() };
}

function startLevel() {
  const value = Number(switchboard.settings.get('startLevel'));
  return Number.isInteger(value) && value >= 1 && value <= 10 ? value : 1;
}

/** The 7-bag: every piece once, in a shuffled order, then again. */
function nextKind() {
  if (bag.length === 0) {
    bag = Object.keys(PIECES);
    for (let i = bag.length - 1; i > 0; i -= 1) {
      const j = Math.floor(Math.random() * (i + 1));
      [bag[i], bag[j]] = [bag[j], bag[i]];
    }
  }
  return bag.pop();
}

function spawn(kind) {
  const shape = matrix(kind);
  return { kind, shape, x: Math.floor((COLS - shape.length) / 2), y: kind === 'I' ? -1 : 0 };
}

function fits(piece, shape = piece.shape, dx = 0, dy = 0) {
  for (let y = 0; y < shape.length; y += 1) {
    for (let x = 0; x < shape.length; x += 1) {
      if (!shape[y][x]) continue;
      const col = piece.x + x + dx;
      const row = piece.y + y + dy;
      if (col < 0 || col >= COLS || row >= ROWS) return false;
      if (row >= 0 && game.grid[row][col]) return false;
    }
  }
  return true;
}

function gravityMs() {
  return Math.max(60, 800 - (game.level - 1) * 75);
}

function beep(frequency, durationMs, type = 'square') {
  if (switchboard.settings.get('sound') && switchboard.has('ui:sound')) switchboard.ui.sound.tone({ frequency, durationMs, type, volume: 0.4 });
}

function start() {
  game = fresh();
  bag = [];
  game.next = nextKind();
  game.piece = spawn(nextKind());
  game.state = 'playing';
  fallMs = 0;
  beep(520, 60, 'triangle');
}

function move(dx) {
  if (game.state !== 'playing' || !fits(game.piece, game.piece.shape, dx, 0)) return;
  game.piece.x += dx;
  beep(220, 15);
}

function rotate(clockwise) {
  if (game.state !== 'playing') return;
  const shape = turn(game.piece.shape, clockwise);
  const kick = KICKS.find(([dx, dy]) => fits(game.piece, shape, dx, dy));
  if (!kick) return;
  game.piece.shape = shape;
  game.piece.x += kick[0];
  game.piece.y += kick[1];
  beep(330, 20);
}

/** One row down; locks the piece when it cannot fall. */
function step(points = 0) {
  if (fits(game.piece, game.piece.shape, 0, 1)) {
    game.piece.y += 1;
    game.score += points;
    return true;
  }
  lock();
  return false;
}

function hardDrop() {
  if (game.state !== 'playing') return;
  while (step(2)) { /* falls until it locks */ }
  beep(140, 50);
}

function lock() {
  const { piece } = game;
  let above = false;
  piece.shape.forEach((row, y) => row.forEach((filled, x) => {
    if (!filled) return;
    if (piece.y + y < 0) above = true;
    else game.grid[piece.y + y][piece.x + x] = piece.kind;
  }));
  const kept = game.grid.filter((row) => row.some((cell) => !cell));
  const cleared = ROWS - kept.length;
  game.grid = [...Array.from({ length: cleared }, () => Array(COLS).fill('')), ...kept];
  if (cleared > 0) {
    game.lines += cleared;
    game.score += LINE_SCORES[cleared] * game.level;
    game.level = Math.max(game.level, startLevel() + Math.floor(game.lines / 10));
    beep(cleared === 4 ? 880 : 660, 90 + cleared * 30, 'triangle');
  }
  game.piece = spawn(game.next);
  game.next = nextKind();
  if (above || !fits(game.piece)) void gameOver();
}

async function gameOver() {
  game.state = 'over';
  beep(110, 400, 'sawtooth');
  if (game.score > best) {
    best = game.score;
    await switchboard.storage.set('best', String(best));
  }
  sync();
}

function togglePause() {
  if (game.state === 'playing') game.state = 'paused';
  else if (game.state === 'paused') game.state = 'playing';
}

// ── drawing ─────────────────────────────────────────────────────────

function cell(ops, col, row, color, x0 = 0, y0 = 0) {
  ops.push({ op: 'rect', x: x0 + col * CELL + 1, y: y0 + row * CELL + 1, w: CELL - 2, h: CELL - 2, fill: color, radius: 3 });
}

function paint() {
  const ops = [{ op: 'clear', color: 'token:background' }];
  ops.push({ op: 'rect', x: 0, y: 0, w: COLS * CELL, h: HEIGHT, fill: 'token:surface', stroke: 'token:border', lineWidth: 1 });
  game.grid.forEach((row, y) => row.forEach((kind, x) => { if (kind) cell(ops, x, y, PIECES[kind].color); }));
  const { piece } = game;
  if (piece && game.state !== 'ready') {
    let ghost = 0;
    while (fits(piece, piece.shape, 0, ghost + 1)) ghost += 1;
    piece.shape.forEach((row, y) => row.forEach((filled, x) => {
      if (!filled) return;
      if (piece.y + y + ghost >= 0) ops.push({ op: 'rect', x: (piece.x + x) * CELL + 2, y: (piece.y + y + ghost) * CELL + 2, w: CELL - 4, h: CELL - 4, stroke: 'token:muted', lineWidth: 1, radius: 3 });
      if (piece.y + y >= 0) cell(ops, piece.x + x, piece.y + y, PIECES[piece.kind].color);
    }));
  }
  const side = COLS * CELL + 16;
  ops.push({ op: 'text', x: side, y: 8, text: 'Next', color: 'token:muted', size: 12, baseline: 'top' });
  if (game.next) matrix(game.next).forEach((row, y) => row.forEach((filled, x) => { if (filled) cell(ops, x, y, PIECES[game.next].color, side, 28); }));
  const stats = [['Score', game.score], ['Lines', game.lines], ['Level', game.level], ['Best', best]];
  stats.forEach(([label, value], index) => {
    ops.push({ op: 'text', x: side, y: 140 + index * 48, text: label, color: 'token:muted', size: 12, baseline: 'top' });
    ops.push({ op: 'text', x: side, y: 156 + index * 48, text: String(value), color: 'token:text', size: 20, weight: 'bold', font: 'mono', baseline: 'top' });
  });
  const banner = { ready: 'Press Enter', paused: 'Paused', over: 'Game over' }[game.state];
  if (banner) {
    ops.push({ op: 'alpha', value: 0.85 }, { op: 'rect', x: 0, y: HEIGHT / 2 - 36, w: COLS * CELL, h: 72, fill: 'token:background' }, { op: 'alpha', value: 1 });
    ops.push({ op: 'text', x: (COLS * CELL) / 2, y: HEIGHT / 2, text: banner, color: 'token:text', size: 22, weight: 'bold', align: 'center', baseline: 'middle' });
  }
  switchboard.ui.draw(PAGE, CANVAS, ops);
}

// ── the page ────────────────────────────────────────────────────────

function view() {
  const playing = game.state === 'playing';
  const control = (text, action) => ({ type: 'button', text, action, disabled: !playing });
  return {
    type: 'row',
    gap: 'lg',
    align: 'start',
    wrap: true,
    children: [
      { type: 'canvas', id: CANVAS, width: WIDTH, height: HEIGHT, label: 'Tetris board. Click it, then play with the arrow keys.', keys: true, frames: true },
      { type: 'stack', gap: 'md', children: [
        { type: 'panel', title: 'Controls', subtitle: 'Or click the board and use the keyboard', children: [
          { type: 'row', gap: 'sm', children: [control('←', 'left'), control('Rotate', 'rotate'), control('→', 'right')] },
          { type: 'row', gap: 'sm', children: [control('Down', 'down'), control('Drop', 'drop')] },
          { type: 'row', gap: 'sm', children: [
            { type: 'button', text: game.state === 'paused' ? 'Resume' : 'Pause', action: 'pause', disabled: !['playing', 'paused'].includes(game.state) },
            { type: 'button', text: game.state === 'ready' ? 'Start' : 'Restart', action: 'restart', variant: 'primary' },
          ] },
        ] },
        { type: 'kv', items: [
          { key: 'Move', value: '← → move · ↓ soft drop · ↑/X/Z rotate' },
          { key: 'Game', value: 'Space drop · P pause · Enter restart' },
        ] },
      ] },
    ],
  };
}

/** Repaints the board every time and re-renders the page tree only when its buttons change. */
function sync() {
  paint();
  const key = game.state;
  if (key === shown) return;
  shown = key;
  void switchboard.ui.render(PAGE, view());
}

const ACTIONS = {
  left: () => move(-1),
  right: () => move(1),
  rotate: () => rotate(true),
  down: () => step(1),
  drop: hardDrop,
  pause: togglePause,
  restart: start,
};

const KEYS = {
  ArrowLeft: () => move(-1),
  ArrowRight: () => move(1),
  ArrowDown: () => { if (game.state === 'playing') step(1); },
  ArrowUp: () => rotate(true),
  x: () => rotate(true),
  z: () => rotate(false),
  ' ': hardDrop,
  p: togglePause,
  Enter: () => { if (game.state !== 'playing' && game.state !== 'paused') start(); },
};

await switchboard.ui.registerPage({ id: PAGE, title: 'Tetris', icon: 'box' });

switchboard.ui.onEvent(PAGE, (event) => {
  if (event.kind === 'hide' && game.state === 'playing') game.state = 'paused';
  if (event.kind === 'show') shown = '';
  if (event.kind === 'action' && ACTIONS[event.action]) ACTIONS[event.action]();
  sync();
});

switchboard.ui.onInput((event) => {
  if (event.canvas !== CANVAS || event.kind !== 'keydown') return;
  const handler = KEYS[event.key.length === 1 ? event.key.toLowerCase() : event.key];
  if (!handler) return;
  handler();
  sync();
});

// Frames arrive only while the board is on screen, about 60 a second.
switchboard.ui.onFrame((dtMs) => {
  if (game.state !== 'playing') return;
  fallMs += Math.min(dtMs, 250);
  let moved = false;
  while (fallMs >= gravityMs() && game.state === 'playing') {
    fallMs -= gravityMs();
    step();
    moved = true;
  }
  if (moved) sync();
});

switchboard.settings.onChange(() => { if (game.state === 'ready') game.level = startLevel(); sync(); });

sync();
