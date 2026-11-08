#include <Arduino.h>
#include <MCUFRIEND_kbv.h>

MCUFRIEND_kbv tft;

#define BLACK  0x0000
#define WHITE  0xFFFF
#define GREY   0x7BEF
#define DARK   0x2104
#define PANEL  0x0841
#define CYAN   0x07FF
#define MAGENTA 0xF81F
#define GREEN  0x07E0
#define YELLOW 0xFFE0
#define RED    0xF800
#define ORANGE 0xFD20
#define SWAP_OK 0xB4DF   // light purple for the swap ring
#define POWER_OK 0xFB56  // hot pink: GPU power ring
#define TEMP_OK 0x3D9F   // sky blue: temperature rings use a different palette than load gauges

static int W, H;

// ---- latest stats ----
struct Stats {
  uint8_t cpu, ram, swap, cpuT, gpu, gpuT, vram;
  uint32_t down, up;          // KB/s
  uint32_t uptime;
  uint16_t load[3];            // load averages x100
  uint8_t ncpu;
  uint16_t gpuW, gpuLim;
  char clock[8];
} st;

#define HIST 87
static uint32_t lastRx;
static bool online = false;

static uint16_t levelColor(uint8_t v, uint8_t warn = 60, uint8_t crit = 85) {
  return v >= crit ? RED : v >= warn ? YELLOW : GREEN;
}

// ---- arc gauge: 270 degrees, N segments, only changed segments get redrawn ----
#define SEGS 30
struct Gauge { int cx, cy, ro, ri; const char *label; uint8_t lit; uint16_t col; uint8_t val; uint8_t warn, crit; uint16_t cOk, cWarn; };
static Gauge G[3] = {
  {90, 88, 62, 55, "CPU", 255, 0, 255, 60, 85, GREEN, YELLOW},
  {240, 88, 62, 55, "RAM", 255, 0, 255, 60, 85, GREEN, YELLOW},
  {390, 88, 62, 55, "GPU", 255, 0, 255, 60, 85, GREEN, YELLOW},
};
// inner temperature rings (CPU / GPU), 20..100 C mapped over the arc
static Gauge T[4] = {
  {90, 88, 53, 47, nullptr, 255, 0, 255, 60, 80, TEMP_OK, ORANGE},
  {390, 88, 53, 47, nullptr, 255, 0, 255, 65, 80, TEMP_OK, ORANGE},
  {240, 88, 53, 47, nullptr, 255, 0, 255, 25, 60, SWAP_OK, ORANGE},   // RAM gauge: swap usage
  {390, 88, 45, 39, nullptr, 255, 0, 255, 85, 98, POWER_OK, ORANGE},  // GPU gauge: power draw / limit
};

static void segment(const Gauge &g, int i, uint16_t c) {
  float a0 = (135 + i * 9.0f + 0.8f) * DEG_TO_RAD;
  float a1 = (135 + (i + 1) * 9.0f - 0.8f) * DEG_TO_RAD;
  float s0 = sin(a0), c0 = cos(a0), s1 = sin(a1), c1 = cos(a1);
  int x0 = g.cx + g.ro * c0, y0 = g.cy + g.ro * s0;
  int x1 = g.cx + g.ro * c1, y1 = g.cy + g.ro * s1;
  int x2 = g.cx + g.ri * c1, y2 = g.cy + g.ri * s1;
  int x3 = g.cx + g.ri * c0, y3 = g.cy + g.ri * s0;
  tft.fillTriangle(x0, y0, x1, y1, x2, y2, c);
  tft.fillTriangle(x0, y0, x2, y2, x3, y3, c);
}

static void gaugeFrame(Gauge &g) {
  for (int i = 0; i < SEGS; i++) segment(g, i, DARK);
  if (g.label) {
    tft.setTextSize(2); tft.setTextColor(GREY);
    tft.setCursor(g.cx - 18, g.cy + 50); tft.print(g.label);
  }
  g.lit = 0; g.col = 0; g.val = 255;
}

// v = fill percent, cv = value that decides the colour (same as v for load gauges, the temperature for rings)
static void gaugeUpdate(Gauge &g, uint8_t v, uint8_t cv) {
  if (cv == g.val) return;
  g.val = cv;
  uint8_t lit = (uint16_t)v * SEGS / 100;
  uint16_t col = cv >= g.crit ? RED : cv >= g.warn ? g.cWarn : g.cOk;
  if (col != g.col) {
    for (int i = 0; i < lit; i++) segment(g, i, col);
    for (int i = lit; i < g.lit; i++) segment(g, i, DARK);
  } else if (lit > g.lit) {
    for (int i = g.lit; i < lit; i++) segment(g, i, col);
  } else {
    for (int i = lit; i < g.lit; i++) segment(g, i, DARK);
  }
  g.lit = lit; g.col = col;
  if (g.label) {
    char b[8]; snprintf(b, sizeof b, "%3d%%", v);
    tft.setTextSize(3); tft.setTextColor(col, BLACK);
    tft.setCursor(g.cx - 36, g.cy - 12); tft.print(b);
  }
}

static uint8_t tempPct(uint8_t t) { return t <= 20 ? 0 : t >= 100 ? 100 : (t - 20) * 100 / 80; }

// ---- helpers ----
static void panelTitle(int x, int y, const char *s) {
  tft.setTextSize(1); tft.setTextColor(GREY);
  tft.setCursor(x, y); tft.print(s);
}

// ---- info panel (bottom band): the PC lays out cells and pages, we only draw ----
#define PX 28
#define PY 262
static const uint16_t PAL[9] = {WHITE, GREY, GREEN, YELLOW, ORANGE, RED, CYAN, TEMP_OK, MAGENTA};
static uint16_t cellHash[6];                 // skip redraws of identical cells
static uint8_t bIdx = 255, bCnt = 255, bSev = 255, bBorder = 255;

struct Item { uint8_t type, len; uint8_t d[75]; };   // frames waiting to be drawn, one per loop pass
static Item q[6];
static uint8_t qh, qn;

static void enqueue(uint8_t type, const uint8_t *p, uint8_t len) {
  if (qn >= 6 || len > sizeof q[0].d) return;        // dropped frames self-heal: the PC resends every 5 s
  Item &it = q[(qh + qn++) % 6];
  it.type = type; it.len = len; memcpy(it.d, p, len);
}

static uint16_t fnv(const uint8_t *p, uint8_t n) {
  uint16_t h = 0x811C;
  while (n--) { h ^= *p++; h *= 0x0101; }
  return h ? h : 1;
}

static void drawCell(const uint8_t *p, uint8_t len) {   // slot | style | pct | "label\tvalue"
  if (len < 3) return;
  uint8_t slot = p[0], style = p[1], pct = p[2];
  bool bar = style & 0x10, wide = style & 0x20, big = style & 0x40, bright = style & 0x80;
  if (slot >= 4 || (wide && (slot & 1))) return;
  uint16_t h = fnv(p, len);
  if (cellHash[slot] == h) return;
  cellHash[slot] = h;
  if (wide) cellHash[slot + 1] = 0;
  char txt[73];
  uint8_t tl = min((int)len - 3, 72);
  memcpy(txt, p + 3, tl); txt[tl] = 0;
  char *val = strchr(txt, '\t');
  if (val) *val++ = 0;
  int x0 = PX + (slot & 1) * 224, y0 = PY + (slot >> 1) * 28, w = wide ? 446 : 222;
  uint16_t col = PAL[min(style & 15, 8)];
  uint8_t sz = big ? 3 : 2;
  int ty = big ? y0 + 2 : y0 + 6;
  tft.fillRect(x0, y0, w, 27, BLACK);
  tft.setTextSize(sz);
  tft.setTextColor((bright || big) ? col : bar ? WHITE : GREY, BLACK);
  tft.setCursor(x0, ty); tft.print(txt);
  if (!val) return;
  tft.setTextColor(col, BLACK);
  if (bar) {
    int bx = x0 + (wide ? 96 : 76), bw = wide ? 230 : 68, fill = (long)(bw - 2) * min((int)pct, 100) / 100;
    tft.drawRect(bx, y0 + 4, bw, 19, GREY);
    tft.fillRect(bx + 1, y0 + 5, fill, 17, col);
    tft.setCursor(bx + bw + 5, ty); tft.print(val);
  } else {
    tft.setCursor(x0 + w - (int)strlen(val) * 6 * sz, ty); tft.print(val);
  }
}

static void drawBand(const uint8_t *p, uint8_t len) {   // page idx | page count | flags
  if (len < 3) return;
  uint8_t idx = p[0], cnt = min((int)p[1], 6), sev = p[2] & 3, border = (p[2] & 8) != 0;
  if (p[2] & 4) { tft.fillRect(PX, PY, 446, 57, BLACK); memset(cellHash, 0, sizeof cellHash); }
  if (cnt != bCnt || idx != bIdx) {
    if (cnt != bCnt) tft.fillRect(14, 260, 12, 58, BLACK);
    for (int i = 0; i < cnt; i++) tft.fillRect(18, 262 + i * 10, 4, 4, i == idx ? CYAN : DARK);
    bCnt = cnt; bIdx = idx;
  }
  if (sev != bSev || border != bBorder) {
    uint16_t c = !sev || !border ? BLACK : sev == 3 ? RED : sev == 2 ? ORANGE : CYAN;
    tft.drawRect(27, 260, 451, 60, c);
    bSev = sev; bBorder = border;
  }
}

static void drawPanelItem() {
  if (!qn) return;
  Item &it = q[qh];
  if (it.type == 5) drawCell(it.d, it.len); else drawBand(it.d, it.len);
  qh = (qh + 1) % 6; qn--;
}

// ---- small text under the gauges (temperatures, VRAM) ----
static int lastVram = -1;
static uint16_t lastW;
static uint16_t lastLoad[3] = {65535, 0, 0};

static uint16_t tempColor(uint8_t t, uint8_t warn) { return t >= 80 ? RED : t >= warn ? ORANGE : TEMP_OK; }

static void gaugeNotes() {
  char b[32];
  gaugeUpdate(T[0], tempPct(st.cpuT), st.cpuT);
  gaugeUpdate(T[1], tempPct(st.gpuT), st.gpuT);
  gaugeUpdate(T[2], st.swap, st.swap);
  uint8_t wp = st.gpuLim ? min(100UL, (unsigned long)st.gpuW * 100 / st.gpuLim) : 0;
  gaugeUpdate(T[3], wp, wp);
  tft.setTextSize(2);
  snprintf(b, sizeof b, "%3dC", st.cpuT);
  tft.setTextColor(tempColor(st.cpuT, 60), BLACK);
  tft.setCursor(G[0].cx - 24, G[0].cy + 16); tft.print(b);
  snprintf(b, sizeof b, "%3dC", st.gpuT);
  tft.setTextColor(tempColor(st.gpuT, 65), BLACK);
  tft.setCursor(G[2].cx - 24, G[2].cy + 16); tft.print(b);
  snprintf(b, sizeof b, "S%2d%%", st.swap);
  tft.setTextColor(st.swap >= 60 ? RED : st.swap >= 25 ? ORANGE : SWAP_OK, BLACK);
  tft.setCursor(G[1].cx - 24, G[1].cy + 16); tft.print(b);
  if (memcmp(lastLoad, st.load, sizeof lastLoad)) {   // load averages under the CPU gauge
    memcpy(lastLoad, st.load, sizeof lastLoad);
    uint16_t t[3];                                      // tenths, rounded
    for (int i = 0; i < 3; i++) t[i] = (st.load[i] + 5) / 10;
    char tmp[24];
    snprintf(tmp, sizeof tmp, "%u.%u %u.%u %u.%u", t[0] / 10, t[0] % 10, t[1] / 10, t[1] % 10, t[2] / 10, t[2] % 10);
    snprintf(b, sizeof b, "%-12s", tmp);                // pad so a shorter value overwrites the old one
    uint32_t per = (uint32_t)st.load[0] * 100 / st.ncpu;   // 1-min load per core, percent
    tft.setTextSize(2);
    tft.setTextColor(per >= 100 ? RED : per >= 60 ? ORANGE : GREY, BLACK);
    tft.setCursor(G[0].cx - 72, G[0].cy + 64); tft.print(b);
  }
  if (st.vram != lastVram || st.gpuW != lastW) {
    lastVram = st.vram; lastW = st.gpuW;
    snprintf(b, sizeof b, "VRAM %d%% ", st.vram);
    tft.setTextSize(1); tft.setTextColor(levelColor(st.vram, 70, 90), BLACK);
    tft.setCursor(G[2].cx - 45, G[2].cy + 70); tft.print(b);
    snprintf(b, sizeof b, "%3uW", st.gpuW);
    tft.setTextColor(wp >= 98 ? RED : wp >= 85 ? ORANGE : POWER_OK, BLACK);
    tft.setCursor(G[2].cx + 21, G[2].cy + 70); tft.print(b);
  }
}

static void fmtRate(char *out, size_t n, uint32_t kbs) {
  if (kbs >= 1024) snprintf(out, n, "%u.%u MB/s ", (unsigned)(kbs / 1024), (unsigned)((kbs % 1024) * 10 / 1024));
  else snprintf(out, n, "%u KB/s   ", (unsigned)kbs);
}

static char lastClock[8], lastUp[16];

static void drawHeader() {  // full redraw (on state change)
  tft.fillRect(0, 0, W, 22, PANEL);
  if (!online) {
    tft.setTextSize(2); tft.setTextColor(RED, PANEL);
    tft.setCursor(28, 4); tft.print(F("NO DATA - start tft-monitor"));
  }
  lastClock[0] = lastUp[0] = 0;
}

static void updateHeader() {
  if (!online) return;
  uint32_t u = st.uptime;
  char b[16]; snprintf(b, sizeof b, "UP %lud %02luh ", u / 86400, (u % 86400) / 3600);
  if (strcmp(b, lastUp)) {
    strcpy(lastUp, b);
    tft.setTextSize(2); tft.setTextColor(GREY, PANEL);
    tft.setCursor(150, 4); tft.print(b);
  }
  if (strcmp(st.clock, lastClock)) {
    strcpy(lastClock, st.clock);
    tft.setTextSize(2); tft.setTextColor(WHITE, PANEL);
    tft.setCursor(W - 72, 4); tft.print(st.clock);
  }
}

// ---- oscilloscope-style load history ----
#define GX 42
#define GY 184
#define GSTEP 3
#define GW ((HIST - 1) * GSTEP)
#define GH 66
#define SC_FRAME 0x0540
#define SC_GRID  0x0320
#define SC_AXIS  0x04A0
// trace colours: head (bright), normal, fading 1, fading 2
static const uint16_t CPU_C[4] = {0xDFFF, CYAN, 0x0495, 0x0229};
static const uint16_t GPU_C[4] = {0xFEFF, MAGENTA, 0x9013, 0x4809};
#define AGE1 25
#define AGE2 55

static uint8_t hC[HIST], hG[HIST];
static uint8_t gPos;
static uint16_t gTotal;

static void graphGrid(int x, int w) {   // redraw background for columns [x, x+w)
  if (x + w > GX + GW + 1) w = GX + GW + 1 - x;
  if (w <= 0) return;
  tft.fillRect(x, GY, w, GH, BLACK);
  // dotted vertical lines, 10 divisions
  for (int k = 1; k < 10; k++) {
    int xv = GX + k * GW / 10;
    if (xv >= x && xv < x + w && k != 5)
      for (int y = GY + 1; y < GY + GH; y += 3) tft.drawPixel(xv, y, SC_GRID);
  }
  // dotted horizontal lines, 4 divisions
  for (int k = 1; k < 4; k++) {
    int yh = GY + k * GH / 4;
    if (k == 2) continue;
    for (int xx = x - (x - GX) % 3 + ((x - GX) % 3 ? 3 : 0); xx < x + w; xx += 3) tft.drawPixel(xx, yh, SC_GRID);
  }
  // centre axes with minor ticks
  int cy = GY + GH / 2, cx = GX + GW / 2;
  tft.drawFastHLine(x, cy, w, SC_AXIS);
  for (int k = 0; k <= 50; k++) {
    int xt = GX + k * GW / 50;
    if (xt >= x && xt < x + w) tft.drawFastVLine(xt, cy - (k % 5 ? 2 : 3), (k % 5 ? 5 : 7), SC_AXIS);
  }
  if (cx >= x && cx < x + w) {
    tft.drawFastVLine(cx, GY, GH, SC_AXIS);
    for (int k = 0; k <= 20; k++) {
      int yt = GY + k * (GH - 1) / 20;
      tft.drawFastHLine(cx - (k % 5 ? 2 : 3), yt, (k % 5 ? 5 : 7), SC_AXIS);
    }
  }
}

static void graphReset() {
  tft.drawRect(GX - 1, GY - 1, GW + 3, GH + 2, SC_FRAME);
  graphGrid(GX, GW + 1);
  tft.setTextSize(1); tft.setTextColor(SC_AXIS, BLACK);
  tft.setCursor(22, GY); tft.print(F("100"));
  tft.setCursor(28, GY + GH / 2 - 3); tft.print(F("50"));
  tft.setCursor(34, GY + GH - 8); tft.print(F("0"));
  gPos = 0; gTotal = 0;
}

static inline int gy(uint8_t v) { return GY + GH - 3 - v * (GH - 4) / 100; }

static void drawSeg(const uint8_t *h, int j, uint16_t col) {
  int x1 = GX + j * GSTEP, x0 = x1 - GSTEP;
  int y0 = gy(h[j - 1]), y1 = gy(h[j]);
  tft.drawLine(x0, y0, x1, y1, col);
  tft.drawLine(x0, y0 + 1, x1, y1 + 1, col);
}

static void recolor(int ageSamples, int level) {
  if (gTotal <= ageSamples) return;
  int j = ((int)gPos - ageSamples + HIST) % HIST;
  if (j < 1) return;
  drawSeg(hG, j, GPU_C[level]);
  drawSeg(hC, j, CPU_C[level]);
}

static void graphPush(uint8_t cpu, uint8_t gpu) {
  int x = GX + gPos * GSTEP;
  hC[gPos] = cpu; hG[gPos] = gpu;
  graphGrid(x + (gPos ? 1 : 0), GSTEP * 3);   // erase gap ahead of the beam
  if (gPos >= 1 && gTotal >= 1) {
    drawSeg(hG, gPos, GPU_C[0]);
    drawSeg(hC, gPos, CPU_C[0]);
  }
  recolor(1, 1);
  recolor(AGE1, 2);
  recolor(AGE2, 3);
  gTotal++;
  if (++gPos >= HIST) gPos = 0;
}

// ---- Claude session usage: two vertical bars (context fill, 5h limit) ----
struct Claude { int8_t ctx, s5h, wk, docker, busy; int16_t reset; } cl = {-1, -1, -1, -1, 0, -1}, clDrawn;
static bool clValid = false;
struct VBar { int8_t h; uint16_t col; } vb[2];
// Screen-edge bar (x0-8): top half = weekly usage fills downward, bottom half = 5h session fills upward, both toward the centre
#define EB_X    3                        // margin from the screen edge
#define EB_Y    28
#define EB_H    286
#define EB_LEN  129
#define EB_TOP  (EB_Y + 12)                      // first fill row of the top half
#define EB_BOT  (EB_TOP + 2 * EB_LEN + 4) // one past the last fill row of the bottom half (4px centre gap)

static void ebRange(int i, int from, int to, uint16_t c) {   // fill length coords [from,to)
  if (to <= from) return;
  if (i == 0) tft.fillRect(EB_X + 1, EB_TOP + from, 7, to - from, c);
  else        tft.fillRect(EB_X + 1, EB_BOT - to, 7, to - from, c);
}

static void vbarFrame() {
  tft.drawRect(EB_X, EB_Y, 9, EB_H, cl.busy ? CYAN : GREY);
  tft.fillRect(EB_X + 1, EB_Y + 1, 7, EB_H - 2, BLACK);
  tft.drawFastHLine(EB_X + 1, EB_TOP + EB_LEN + 2, 7, GREY);
  tft.drawFastHLine(EB_X + 1, EB_TOP + EB_LEN + 3, 7, GREY);
  tft.setTextSize(1); tft.setTextColor(WHITE, BLACK);
  tft.setCursor(EB_X + 2, EB_Y + 3); tft.print('W');
  tft.setCursor(EB_X + 2, EB_BOT + 2); tft.print('S');
  vb[0].h = vb[1].h = 0; vb[0].col = vb[1].col = 0;
}

static void vbar(int i, int8_t v) {
  uint8_t pv = v < 0 ? 0 : v;
  int h = (long)EB_LEN * min((int)pv, 100) / 100;
  uint16_t col = v < 0 ? DARK : levelColor(pv, 60, 80);
  VBar &b = vb[i];
  if (col != b.col) { ebRange(i, 0, h, col); ebRange(i, h, EB_LEN, BLACK); }
  else if (h > b.h) ebRange(i, b.h, h, col);
  else if (h < b.h) ebRange(i, h, b.h, BLACK);
  b.h = h; b.col = col;
}

static void drawClaude() {
  if (clValid && !memcmp(&cl, &clDrawn, sizeof cl)) return;
  if (!clValid || cl.busy != clDrawn.busy) tft.drawRect(EB_X, EB_Y, 9, EB_H, cl.busy ? CYAN : GREY);
  vbar(0, cl.wk); vbar(1, cl.s5h);
  char b[24];
  tft.setTextSize(1);
  if (cl.s5h >= 0) snprintf(b, sizeof b, "C%3d%% 5H%3d%% %d:%02d  ", cl.ctx, cl.s5h, cl.reset / 60, cl.reset % 60);
  else snprintf(b, sizeof b, "C%3d%% 5H --        ", cl.ctx);
  tft.setTextColor(levelColor(max((int)cl.ctx, (int)cl.s5h), 60, 80), BLACK);
  tft.setCursor(328, 242); tft.print(b);
  tft.setTextSize(2);
  snprintf(b, sizeof b, "DOCKER %d  ", cl.docker);
  tft.setTextColor(CYAN, PANEL);
  tft.setCursor(28, 4); tft.print(b);
  tft.setTextColor(cl.busy ? YELLOW : GREY, PANEL);
  tft.setCursor(300, 4); tft.print(cl.busy ? F("CLAUDE*") : F("CLAUDE "));
  clDrawn = cl; clValid = true;
}

static int32_t lastDown = -1, lastUpR = -1;

static void drawStatic() {
  tft.fillScreen(BLACK);
  for (int i = 0; i < 3; i++) gaugeFrame(G[i]);
  for (int i = 0; i < 4; i++) gaugeFrame(T[i]);
  panelTitle(28, 168, "LOAD HISTORY");
  tft.setTextColor(CYAN); tft.setCursor(100, 168); tft.print(F("CPU"));
  tft.setTextColor(MAGENTA); tft.setCursor(124, 168); tft.print(F("GPU"));
  panelTitle(328, 168, "NETWORK");
  tft.setTextSize(2);
  tft.setTextColor(GREEN); tft.setCursor(328, 186); tft.print(F("v"));
  tft.setTextColor(ORANGE); tft.setCursor(328, 222); tft.print(F("^"));
  tft.drawFastVLine(318, 166, 86, DARK);
  tft.drawFastHLine(0, 258, W, DARK);
  graphReset();
  vbarFrame(); clValid = false;
  memset(cellHash, 0, sizeof cellHash); bIdx = bCnt = bSev = bBorder = 255; qn = 0;
  lastVram = -1; lastW = 0; lastLoad[0] = 65535;
  lastDown = lastUpR = -1;
}

static void redraw() {
  if (!online) { drawHeader(); return; }
  updateHeader();
  gaugeUpdate(G[0], st.cpu, st.cpu);
  gaugeUpdate(G[1], st.ram, st.ram);
  gaugeUpdate(G[2], st.gpu, st.gpu);
  graphPush(st.cpu, st.gpu);
  char b[16];
  tft.setTextSize(2);
  if (st.down != lastDown) {
    lastDown = st.down; fmtRate(b, sizeof b, st.down);
    tft.setTextColor(GREEN, BLACK); tft.setCursor(348, 186); tft.print(b);
  }
  if (st.up != lastUpR) {
    lastUpR = st.up; fmtRate(b, sizeof b, st.up);
    tft.setTextColor(ORANGE, BLACK); tft.setCursor(348, 222); tft.print(b);
  }
  gaugeNotes();
}

// ---- screensaver: shown when the PC powers off, the monitor stops or the link is lost ----
struct Sum { uint32_t uptime, wh10, downMB, upMB; uint8_t peakCpu, peakGpu, peakCpuT, peakGpuT, avgCpu, disk; } sum;
static bool sumValid, saver, byeGot;
static uint8_t byeReason, offH, offM;      // reason: 0 link lost, 1 power off, 2 reboot, 3 monitor stopped
static uint32_t ssStart, ssPageT, ssT0, ssLast;
static uint8_t ssPage;
#define SS_PAGES 5
static const uint8_t SS_DUR[SS_PAGES] = {12, 9, 8, 9, 12};   // seconds per page
struct Star { int16_t x; uint8_t y, sp; };
static Star stars[36];
static int16_t orbX[8], orbY[8];
static uint16_t orbA;
static int32_t ssShown[5];                 // values already on screen, so only changes are redrawn
static int16_t zx[3], zy[3]; static uint8_t zs[3];
static const uint16_t STAR_C[3] = {0x39E7, 0x7BEF, WHITE};
static const uint16_t ORB_C[8] = {0xFFFF, 0x07FF, 0x05F7, 0x03EF, 0x02CF, 0x01AF, 0x0127, 0x00A3};
static const uint8_t ORB_R[8] = {6, 5, 5, 4, 4, 3, 3, 2};

static void ctext(int y, uint8_t sz, uint16_t col, const char *s) {
  tft.setTextSize(sz); tft.setTextColor(col, BLACK);
  tft.setCursor(240 - (int)strlen(s) * 3 * sz, y); tft.print(s);
}
static void rtext(int xr, int y, uint8_t sz, uint16_t col, const char *s, uint8_t clr) {
  int w = clr * 6 * sz;
  tft.fillRect(xr - w, y, w, 8 * sz, BLACK);
  tft.setTextSize(sz); tft.setTextColor(col, BLACK);
  tft.setCursor(xr - (int)strlen(s) * 6 * sz, y); tft.print(s);
}
static uint32_t ease(uint32_t t, uint32_t dur) {   // 0..1000, quadratic ease-out
  if (t >= dur) return 1000;
  uint32_t p = t * 1000 / dur;
  return p * (2000 - p) / 1000;
}
static void fmtWh(char *o, size_t n, uint32_t wh10) {
  uint32_t wh = wh10 / 10;
  if (wh >= 1000) snprintf(o, n, "%lu.%02lu kWh", wh / 1000, (wh % 1000) / 10);
  else snprintf(o, n, "%lu Wh", wh);
}
static void fmtMB(char *o, size_t n, uint32_t mb) {
  if (mb >= 1024) snprintf(o, n, "%lu.%lu GB", mb / 1024, (mb % 1024) * 10 / 1024);
  else snprintf(o, n, "%lu MB", mb);
}
static void fmtHMS(char *o, size_t n, uint32_t s) { snprintf(o, n, "%02lu:%02lu:%02lu", (s / 3600) % 100, (s / 60) % 60, s % 60); }
static const char *ssTitle() {
  switch (byeReason) { case 1: return "POWERED OFF"; case 2: return "REBOOTING"; case 3: return "MONITOR STOPPED"; }
  return "LINK LOST";
}
static uint16_t ssTitleCol() { return byeReason == 1 ? ORANGE : byeReason == 2 ? YELLOW : byeReason == 3 ? GREY : CYAN; }
static uint16_t grey565(uint8_t g) { return ((g >> 3) << 11) | ((g >> 2) << 5) | (g >> 3); }
static uint16_t peakColor(uint8_t v, bool temp) {
  if (temp) return v >= 80 ? RED : v >= 60 ? ORANGE : TEMP_OK;
  return v >= 85 ? RED : v >= 60 ? YELLOW : GREEN;
}
static void ssClock(char *o, size_t n, uint32_t elapsedS) {   // wall clock estimate: time of the signal + elapsed
  uint32_t m = (offH * 60UL + offM + elapsedS / 60) % 1440;
  snprintf(o, n, "%02lu:%02lu", m / 60, m % 60);
}

static void ssGoto(uint8_t p) {
  ssPage = p; ssPageT = ssT0 = millis();
  tft.fillScreen(BLACK);
  for (int i = 0; i < SS_PAGES; i++) tft.fillCircle(240 + (i - 2) * 14, 312, 3, i == p ? CYAN : DARK);
  for (uint8_t i = 0; i < 5; i++) ssShown[i] = -1;
  char b[28];
  switch (p) {
    case 0:
      for (uint8_t i = 0; i < 36; i++) { stars[i].x = random(480); stars[i].y = random(300); stars[i].sp = 1 + random(3); }
      for (uint8_t i = 0; i < 8; i++) orbX[i] = -100;
      ctext(22, 3, ssTitleCol(), ssTitle());
      ctext(100, 2, GREY, byeReason ? "OFF FOR" : "NO DATA FOR");
      break;
    case 1: {
      ctext(10, 3, CYAN, "LAST SESSION");
      static const char *const L[5] = {"UPTIME", "ENERGY ~", "DOWNLOAD", "UPLOAD", "FULLEST DISK"};
      tft.setTextSize(2); tft.setTextColor(GREY, BLACK);
      for (uint8_t i = 0; i < 5; i++) { tft.setCursor(30, 66 + i * 48); tft.print(L[i]); }
      break; }
    case 2: {
      ctext(10, 3, CYAN, "PEAKS");
      static const char *const L[4] = {"CPU", "GPU", "CPU T", "GPU T"};
      tft.setTextSize(2); tft.setTextColor(GREY, BLACK);
      for (uint8_t i = 0; i < 4; i++) { tft.setCursor(50 + i * 110 + (60 - (int)strlen(L[i]) * 12) / 2, 280); tft.print(L[i]); }
      tft.drawFastHLine(30, 270, 420, DARK);
      break; }
    case 3: {
      ctext(10, 3, CYAN, "ENERGY");
      uint32_t ch = sum.wh10 / 10 / 12;
      if (ch) snprintf(b, sizeof b, "~ %lu phone charges", ch); else snprintf(b, sizeof b, "less than 1 phone charge");
      ctext(108, 2, GREY, b);
      if (ch) { tft.setTextSize(1); tft.setTextColor(DARK, BLACK); tft.setCursor(240 - 111, 130); tft.print(F("each battery = 1 charge (12 Wh)")); }
      ssShown[1] = 0;
      break; }
    case 4:
      ctext(14, 3, ssTitleCol(), ssTitle());
      tft.fillCircle(100, 150, 46, 0xEF7D); tft.fillCircle(120, 136, 40, BLACK);
      for (uint8_t i = 0; i < 3; i++) { zx[i] = -1; zs[i] = 2; }
      break;
  }
}

static void startSaver(uint8_t reason, uint8_t h, uint8_t m, uint32_t since) {
  saver = true; online = false;
  byeReason = reason; offH = h; offM = m; ssStart = since;
  ssGoto(0);
}

static void ssTick() {
  uint32_t now = millis();
  if (now - ssLast < 40) return;
  ssLast = now;
  if (now - ssPageT > (uint32_t)SS_DUR[ssPage] * 1000) { ssGoto((ssPage + 1) % SS_PAGES); return; }
  uint32_t t = now - ssT0, el = (now - ssStart) / 1000;
  char b[28], c[8];
  switch (ssPage) {
    case 0: {
      for (uint8_t i = 0; i < 36; i++) {   // starfield drifting left; the text box is kept clear
        Star &s = stars[i];
        bool box = s.y > 92 && s.y < 232 && s.x > 80 && s.x < 400;
        if (!box) tft.drawPixel(s.x, s.y, BLACK);
        s.x -= s.sp;
        if (s.x < 0) { s.x = 479; s.y = random(300); }
        box = s.y > 92 && s.y < 232 && s.x > 80 && s.x < 400;
        if (!box) tft.drawPixel(s.x, s.y, STAR_C[s.sp - 1]);
      }
      for (uint8_t i = 0; i < 8; i++) if (orbX[i] > -100) tft.fillCircle(orbX[i], orbY[i], 6, BLACK);
      orbA = (orbA + 4) % 360;   // a comet orbiting the clock
      for (uint8_t i = 7; i > 0; i--) { orbX[i] = orbX[i - 1]; orbY[i] = orbY[i - 1]; }
      float a = orbA * DEG_TO_RAD;
      orbX[0] = 240 + 215 * cos(a); orbY[0] = 160 + 105 * sin(a);
      for (int8_t i = 7; i >= 0; i--) if (orbX[i] > -100) tft.fillCircle(orbX[i], orbY[i], ORB_R[i], ORB_C[i]);
      if ((int32_t)el != ssShown[0]) {
        ssShown[0] = el;
        fmtHMS(b, sizeof b, el); ctext(130, 6, WHITE, b);
        ssClock(c, sizeof c, 0);
        char n[8]; ssClock(n, sizeof n, el);
        snprintf(b, sizeof b, "since %s  now %s", c, n); ctext(204, 2, GREY, b);
      }
      break; }
    case 1: {
      for (uint8_t i = 0; i < 5; i++) {
        if (t < i * 250UL) break;
        uint32_t e = ease(t - i * 250UL, 900);
        uint32_t target = i == 0 ? sum.uptime / 60 : i == 1 ? sum.wh10 : i == 2 ? sum.downMB : i == 3 ? sum.upMB : sum.disk;
        int32_t v = (uint32_t)(target * e / 1000);
        if (v == ssShown[i]) continue;
        if (ssShown[i] < 0) tft.drawFastHLine(30, 66 + i * 48 + 30, 420, DARK);
        ssShown[i] = v;
        uint16_t col = WHITE;
        if (i == 0) { col = CYAN; if (v >= 1440) snprintf(b, sizeof b, "%lud %02luh %02lum", v / 1440, (v / 60) % 24, v % 60); else snprintf(b, sizeof b, "%luh %02lum", v / 60, v % 60); }
        else if (i == 1) { col = YELLOW; fmtWh(b, sizeof b, v); }
        else if (i == 2) { col = GREEN; fmtMB(b, sizeof b, v); }
        else if (i == 3) { col = ORANGE; fmtMB(b, sizeof b, v); }
        else { col = peakColor(sum.disk, false); snprintf(b, sizeof b, "%ld%%", v); }
        rtext(450, 62 + i * 48, 3, col, b, 14);
      }
      break; }
    case 2: {
      static const uint16_t X[4] = {50, 160, 270, 380};
      uint8_t val[4] = {sum.peakCpu, sum.peakGpu, sum.peakCpuT, sum.peakGpuT};
      uint32_t e = ease(t, 1400);
      for (uint8_t i = 0; i < 4; i++) {
        int32_t h = (uint32_t)val[i] * e * 170 / 100000UL, prev = ssShown[i] < 0 ? 0 : ssShown[i];
        if (h > prev) tft.fillRect(X[i], 268 - h, 60, h - prev, peakColor(val[i], i >= 2));
        ssShown[i] = h;
      }
      if (t >= 1400 && ssShown[4] < 0) {
        ssShown[4] = 1;
        tft.setTextSize(2);
        for (uint8_t i = 0; i < 4; i++) {
          snprintf(b, sizeof b, i < 2 ? "%u%%" : "%uC", val[i]);
          tft.setTextColor(peakColor(val[i], i >= 2), BLACK);
          int h = (uint32_t)val[i] * 170 / 100;
          tft.setCursor(X[i] + (60 - (int)strlen(b) * 12) / 2, max(40, 268 - h - 22)); tft.print(b);
        }
        int ay = 268 - (int)sum.avgCpu * 170 / 100;   // average CPU marker on the first bar
        tft.drawFastHLine(X[0] - 6, ay, 72, WHITE);
        tft.setTextSize(1); tft.setTextColor(WHITE, BLACK); tft.setCursor(X[0] + 64, ay - 3); tft.print(F("avg"));
      }
      break; }
    case 3: {
      uint32_t e = ease(t, 1500), v = sum.wh10 * e / 1000;
      if ((int32_t)v != ssShown[0]) {
        ssShown[0] = v;
        fmtWh(b, sizeof b, v);
        tft.fillRect(30, 52, 420, 44, BLACK);
        ctext(56, 5, YELLOW, b);
      }
      uint32_t ch = min(30UL, sum.wh10 / 10 / 12), want = min(ch, t / 70);
      while ((uint32_t)ssShown[1] < want) {   // batteries fill in one by one
        uint32_t k = ssShown[1]++;
        int x = 24 + (k % 10) * 44, y = 150 + (k / 10) * 50;
        tft.drawRect(x, y, 36, 22, GREY); tft.fillRect(x + 3, y + 3, 30, 16, GREEN); tft.fillRect(x + 36, y + 7, 3, 8, GREY);
      }
      break; }
    case 4: {
      for (uint8_t k = 0; k < 3; k++) {   // floating Zs
        uint32_t tt = (now + k * 1000UL) % 3000, pr = tt / 3;
        if (zx[k] >= 0) tft.fillRect(zx[k], zy[k], 6 * zs[k], 8 * zs[k], BLACK);
        zs[k] = 2 + (pr > 400) + (pr > 700);
        zx[k] = 190 + pr * 180 / 1000; zy[k] = 170 - pr * 110 / 1000;
        uint8_t g = (pr < 500 ? pr : 1000 - pr) * 255 / 500;
        tft.setTextSize(zs[k]); tft.setTextColor(grey565(g)); tft.setCursor(zx[k], zy[k]); tft.print('Z');
      }
      if ((int32_t)el != ssShown[0]) {
        ssShown[0] = el;
        fmtHMS(b, sizeof b, el); ctext(216, 4, WHITE, b);
        ssClock(c, sizeof c, el); snprintf(b, sizeof b, "now %s", c); ctext(268, 2, GREY, b);
      }
      break; }
  }
}

// ---- binary protocol: 0xAA 0x55 | type | len | payload | crc8(type,len,payload) ----
enum : uint8_t { F_STATS = 1, F_CLAUDE = 2, F_BAND = 4, F_CELL = 5, F_SUM = 6, F_BYE = 7 };
static bool gotS, gotC;
static uint16_t framesOk, framesBad;   // reported back to the PC every 5 s for diagnostics

static uint8_t crcUpdate(uint8_t c, uint8_t b) {   // CRC-8, poly 0x07
  c ^= b;
  for (uint8_t i = 0; i < 8; i++) c = (c & 0x80) ? (c << 1) ^ 0x07 : (c << 1);
  return c;
}

static uint32_t rd32(const uint8_t *p) { return p[0] | (uint32_t)p[1] << 8 | (uint32_t)p[2] << 16 | (uint32_t)p[3] << 24; }

static void handleFrame(uint8_t type, const uint8_t *p, uint8_t len) {
  if (type == F_STATS && len == 32) {
    st.cpu = min((int)p[0], 100); st.ram = min((int)p[1], 100); st.cpuT = p[2];
    st.gpu = min((int)p[3], 100); st.gpuT = p[4]; st.vram = min((int)p[5], 100);
    st.down = rd32(p + 6); st.up = rd32(p + 10); st.uptime = rd32(p + 14);
    snprintf(st.clock, sizeof st.clock, "%02d:%02d", min((int)p[18], 23), min((int)p[19], 59));
    st.swap = min((int)p[20], 100);
    for (int i = 0; i < 3; i++) st.load[i] = p[21 + 2 * i] | p[22 + 2 * i] << 8;
    st.ncpu = max((int)p[27], 1);
    st.gpuW = p[28] | p[29] << 8; st.gpuLim = p[30] | p[31] << 8;
    gotS = true;
  } else if (type == F_CLAUDE && len == 7) {
    cl.ctx = (int8_t)p[0]; cl.s5h = (int8_t)p[1]; cl.reset = p[2] | p[3] << 8;
    cl.wk = (int8_t)p[4]; cl.busy = p[5] != 0; cl.docker = (int8_t)p[6];
    gotC = true;
  } else if (type == F_SUM && len == 22) {
    sum.uptime = rd32(p); sum.wh10 = rd32(p + 4);
    sum.peakCpu = p[8]; sum.peakGpu = p[9]; sum.peakCpuT = p[10]; sum.peakGpuT = p[11]; sum.avgCpu = p[12];
    sum.downMB = rd32(p + 13); sum.upMB = rd32(p + 17); sum.disk = p[21];
    sumValid = true;
  } else if (type == F_BYE && len == 3) {
    byeReason = p[0]; offH = min((int)p[1], 23); offM = min((int)p[2], 59); byeGot = true;
  } else if (type == F_BAND || type == F_CELL) {
    enqueue(type, p, len);
  }
}

static void feed(uint8_t b) {
  enum { SYNC0, SYNC1, TYPE, LEN, DATA, CRC };
  static uint8_t state = SYNC0, type, len, pos, crc, data[96];
  switch (state) {
    case SYNC0: if (b == 0xAA) state = SYNC1; break;
    case SYNC1: state = b == 0x55 ? TYPE : b == 0xAA ? SYNC1 : SYNC0; break;
    case TYPE: type = b; crc = crcUpdate(0, b); state = LEN; break;
    case LEN:
      len = b; crc = crcUpdate(crc, b); pos = 0;
      state = len > sizeof data ? SYNC0 : len ? DATA : CRC;
      break;
    case DATA:
      data[pos++] = b; crc = crcUpdate(crc, b);
      if (pos == len) state = CRC;
      break;
    case CRC:
      if (b == crc) { handleFrame(type, data, len); framesOk++; } else framesBad++;
      state = SYNC0;
      break;
  }
}

// ---- boot screen: one line per module, drawn as it initialises ----
static uint8_t bootY = 40;
static int freeRam() { extern int __heap_start, *__brkval; int v; return (int)&v - (__brkval ? (int)__brkval : (int)&__heap_start); }
static void bootLine(const __FlashStringHelper *name, const char *detail, bool ok = true) {
  tft.setTextSize(2);
  tft.setCursor(14, bootY); tft.setTextColor(GREY); tft.print('[');
  tft.setTextColor(ok ? GREEN : ORANGE); tft.print(ok ? F(" OK ") : F("WAIT")); tft.setTextColor(GREY); tft.print(F("] "));
  tft.setTextColor(WHITE); tft.print(name);
  tft.setTextColor(GREY); tft.print(' '); tft.print(detail);
  bootY += 22; delay(45);
}
static void bootScreen(uint16_t id) {
  tft.fillScreen(BLACK);
  tft.setTextSize(3); tft.setTextColor(CYAN); tft.setCursor(14, 8); tft.print(F("TFT MONITOR"));
  tft.drawFastHLine(14, 34, 452, DARK);
  char b[24];
  snprintf(b, sizeof b, "ILI%X %dx%d", id, W, H); bootLine(F("DISPLAY"), b);
  bootLine(F("SERIAL"), "115200 8N1"); 
  bootLine(F("PROTOCOL"), "CRC-8 frames");
  bootLine(F("GAUGES"), "CPU GPU RAM");
  bootLine(F("RINGS"), "TEMP SWAP");
  bootLine(F("SCOPE"), "load history");
  bootLine(F("INFO BAND"), "alerts+pages");
  snprintf(b, sizeof b, "%d B free", freeRam()); bootLine(F("MEMORY"), b);
  bootLine(F("HOST LINK"), "waiting...", false);
}

static bool booted;
void setup() {
  Serial.begin(115200);
  uint16_t id = tft.readID();
  Serial.print(F("TFT ID: 0x")); Serial.println(id, HEX);
  if (id == 0xD3D3) id = 0x9486;
  tft.begin(id);
  tft.setRotation(3);   // landscape, flipped 180 (use 1 for the original orientation)
  W = tft.width(); H = tft.height();
  bootScreen(id);
}

void loop() {
  while (Serial.available()) feed(Serial.read());   // drain first, then draw only the newest data once
  if (gotS) {
    gotS = false;
    if (!online) {
      online = true; saver = false;
      if (!booted) {   // finish the boot log, then swap to the dashboard
        booted = true;
        bootY -= 22; tft.fillRect(0, bootY, 480, 22, BLACK);
        bootLine(F("HOST LINK"), "connected");
        delay(350);
      }
      drawStatic(); drawHeader();
    }
    lastRx = millis();
    redraw();
    if (!clValid) drawClaude();
  }
  if (gotC) { gotC = false; if (online) drawClaude(); }
  if (byeGot) {
    byeGot = false;
    if (sumValid && !saver) startSaver(byeReason, offH, offM, millis());
  }
  if (saver) ssTick();
  if (online) drawPanelItem();
  static uint32_t lastStat;
  if (millis() - lastStat > 5000) {
    lastStat = millis();
    Serial.print(F("ok=")); Serial.print(framesOk); Serial.print(F(" bad=")); Serial.println(framesBad);
  }
  if (online && millis() - lastRx > 5000) {
    online = false;
    if (sumValid) {
      uint8_t h = (st.clock[0] - '0') * 10 + st.clock[1] - '0', m = (st.clock[3] - '0') * 10 + st.clock[4] - '0';
      startSaver(0, min((int)h, 23), min((int)m, 59), lastRx);
    } else {
      drawStatic();
      drawHeader();
    }
  }
}
