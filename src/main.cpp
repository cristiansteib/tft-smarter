#include <Arduino.h>
#include <MCUFRIEND_kbv.h>
#include <TouchScreen.h>
#include <EEPROM.h>

#include <Fonts/FreeSans9pt7b.h>
#include <Fonts/FreeSansBold9pt7b.h>
#include <Fonts/FreeSans12pt7b.h>
#include <Fonts/FreeSansBold12pt7b.h>
#include <Fonts/FreeSansBold18pt7b.h>
#include <glcdfont.c>   // the classic 5x7 font (Adafruit_GFX keeps its copy private), for the compositor

static void pump();   // drain the serial port; safe to call between draw steps (it never draws)

// ---- fast LCD access ----
// MCUFRIEND_kbv on a UNO shield plugged into a Mega spreads the 8-bit bus over three ports and rebuilds every
// byte bit by bit: ~4.5 us per filled pixel, and ~128 us per drawPixel (four commands for the address window per
// pixel), which is what every GFX-font glyph pixel goes through. For the ILI9486 we drive the bus directly with
// precomputed port values (UNO shield pinout: D0 PH5, D1 PH6, D2 PE4, D3 PE5, D4 PG5, D5 PE3, D6 PH3, D7 PH4;
// WR PF1, RS PF2, CS PF3). Other controllers / boards fall back to the library.
#if defined(__AVR_ATmega2560__)
// The serial protocol this firmware speaks, reported to the PC as "V <proto> <version>" at boot and every 5 s so
// tft-monitor can tell whether it can drive it (internal/version: Proto, MinProto). Bump it with any frame change.
#define PROTO 1
#ifndef FW_VERSION      // set by tools/fw_version.py from `git describe`
#define FW_VERSION "dev"
#endif
static void hello() { Serial.print(F("V ")); Serial.print(PROTO); Serial.print(' '); Serial.println(F(FW_VERSION)); }

#define FAST_LCD 1
#define AINL inline __attribute__((always_inline))
// data byte -> port bits, as tables (the shifts cost ~40 cycles per byte otherwise)
#define BH_(b) (uint8_t)((((b) & 0x03) << 5) | (((b) & 0xC0) >> 3))
#define BE_(b) (uint8_t)((((b) & 0x0C) << 2) | (((b) & 0x20) >> 2))
#define R4_(M, b) M(b), M(b + 1), M(b + 2), M(b + 3)
#define R16_(M, b) R4_(M, b), R4_(M, b + 4), R4_(M, b + 8), R4_(M, b + 12)
#define R64_(M, b) R16_(M, b), R16_(M, b + 16), R16_(M, b + 32), R16_(M, b + 48)
#define R256_(M) R64_(M, 0), R64_(M, 64), R64_(M, 128), R64_(M, 192)
static const uint8_t BUS_H[256] PROGMEM = {R256_(BH_)};
static const uint8_t BUS_E[256] PROGMEM = {R256_(BE_)};
struct Bus { uint8_t h, e, g; };          // PORTH / PORTE / PORTG values that put one byte on the bus
static uint8_t busH, busE, busG;           // the ports with the data bits cleared (other pins keep their state)
static AINL void busSnap() { busH = PORTH & ~0x78; busE = PORTE & ~0x38; busG = PORTG & ~0x20; }
static AINL Bus busFor(uint8_t b) {
  Bus r;
  r.h = busH | pgm_read_byte(&BUS_H[b]);
  r.e = busE | pgm_read_byte(&BUS_E[b]);
  r.g = busG | ((b & 0x10) << 1);
  return r;
}
#define WR_PULSE() do { PORTF &= ~_BV(1); PORTF |= _BV(1); } while (0)
static AINL void busSet(const Bus &b) { PORTH = b.h; PORTE = b.e; PORTG = b.g; }
static AINL void busPut(const Bus &b) { busSet(b); WR_PULSE(); }
static AINL void lcdByte(uint8_t v) { Bus b = busFor(v); busPut(b); }
static AINL void lcdCmd(uint8_t c) { PORTF &= ~_BV(2); lcdByte(c); PORTF |= _BV(2); }
// the controller keeps the column / page range between writes: only resend what changed
static int16_t winX0 = -1, winX1, winY0, winY1;
static void lcdWin(int16_t x0, int16_t y0, int16_t x1, int16_t y1) {   // opens a memory write; CS stays low
  busSnap();
  PORTF &= ~_BV(3);
  if (x0 != winX0 || x1 != winX1) {
    lcdCmd(0x2A); lcdByte(x0 >> 8); lcdByte(x0); lcdByte(x1 >> 8); lcdByte(x1);
    winX0 = x0; winX1 = x1;
  }
  if (y0 != winY0 || y1 != winY1) {
    lcdCmd(0x2B); lcdByte(y0 >> 8); lcdByte(y0); lcdByte(y1 >> 8); lcdByte(y1);
    winY0 = y0; winY1 = y1;
  }
  lcdCmd(0x2C);
}
static inline void lcdDone() { PORTF |= _BV(3); }
#endif

class FastTFT : public MCUFRIEND_kbv {
 public:
  bool fast = false;   // set once the controller is known to be an ILI9486
#ifdef FAST_LCD
  void drawPixel(int16_t x, int16_t y, uint16_t c) override {
    if (!fast) { MCUFRIEND_kbv::drawPixel(x, y, c); return; }
    if (x < 0 || y < 0 || x >= _width || y >= _height) return;
    lcdWin(x, y, x, y);
    Bus hi = busFor(c >> 8), lo = busFor(c);
    busPut(hi); busPut(lo);
    lcdDone();
  }
  void fillRect(int16_t x, int16_t y, int16_t w, int16_t h, uint16_t c) override {
    if (!fast) { MCUFRIEND_kbv::fillRect(x, y, w, h, c); return; }
    if (w < 0) { w = -w; x -= w; }
    if (h < 0) { h = -h; y -= h; }
    if (x < 0) { w += x; x = 0; }
    if (y < 0) { h += y; y = 0; }
    if (x + w > _width) w = _width - x;
    if (y + h > _height) h = _height - y;
    if (w <= 0 || h <= 0) return;
    lcdWin(x, y, x + w - 1, y + h - 1);
    uint8_t hb = c >> 8, lb = c;
    Bus hi = busFor(hb), lo = busFor(lb);
    for (int16_t r = 0; r < h; r++) {
      uint16_t n = w;
      if (hb == lb) { busSet(hi); do { WR_PULSE(); WR_PULSE(); } while (--n); }   // e.g. black: just strobe
      else do { busPut(hi); busPut(lo); } while (--n);
      if ((r & 15) == 15) pump();
    }
    lcdDone();
  }
#endif
};
static FastTFT tft;

// Palette: calm teal / amber / coral are the three states; sky = temperatures, violet = GPU / second series
#define BLACK  0x0000
#define WHITE  0xEF9F   // cloud   #EAF0F8  primary text
#define GREY   0x8CD6   // mist    #8A9BB4  secondary text
#define DARK   0x1926   // slate   #1C2636  tracks, rules
#define PANEL  0x0841
#define CYAN   0x4F3F   // #4FE6FF  CPU trace, accents
#define MAGENTA 0xB47F  // violet  #B48CFF  GPU
#define GREEN  0x26B6   // calm    #27D6B4
#define YELLOW 0xFD84   // amber   #FFB020
#define RED    0xFAAC   // coral   #FF5560
#define ORANGE 0xFC47   // #FF8A3D
#define SWAP_OK 0xB4DF   // light purple for the swap ring
#define POWER_OK 0xFB56  // hot pink: GPU power ring
#define TEMP_OK 0x4D5F   // sky     #4AA8FF  temperature rings

static int W, H;
static void menuBar();   // the bottom menu strip, defined with the touch code
static void claudeLogo(int cx, int cy, int r);

// ---- text measuring (same numbers as Adafruit_GFX getTextBounds) ----
struct Ext { int16_t x0, x1, y0, y1; };    // pixel bounding box, inclusive; x0 > x1 when nothing is drawn
static Ext textExt(const GFXfont *f, const char *s, int16_t x, int16_t base) {
  Ext e = {32767, -32768, 32767, -32768};
  if (!f) {                                // classic 5x7 font: 6x8 cells, base = top
    int16_t n = strlen(s);
    if (n) { e.x0 = x; e.x1 = x + 6 * n - 1; e.y0 = base; e.y1 = base + 7; }
    return e;
  }
  uint8_t first = pgm_read_byte(&f->first), last = pgm_read_byte(&f->last);
  const GFXglyph *gt = (const GFXglyph *)pgm_read_word(&f->glyph);
  for (; *s; s++) {
    uint8_t c = *s;
    if (c < first || c > last) continue;
    const GFXglyph *g = gt + (c - first);
    uint8_t gw = pgm_read_byte(&g->width), gh = pgm_read_byte(&g->height);
    if (gw && gh) {
      int16_t xo = (int8_t)pgm_read_byte(&g->xOffset), yo = (int8_t)pgm_read_byte(&g->yOffset);
      if (x + xo < e.x0) e.x0 = x + xo;
      if (x + xo + gw - 1 > e.x1) e.x1 = x + xo + gw - 1;
      if (base + yo < e.y0) e.y0 = base + yo;
      if (base + yo + gh - 1 > e.y1) e.y1 = base + yo + gh - 1;
    }
    x += pgm_read_byte(&g->xAdvance);
  }
  return e;
}
static uint16_t textW(const GFXfont *f, const char *t) { Ext e = textExt(f, t, 0, 0); return e.x1 >= e.x0 ? e.x1 - e.x0 + 1 : 0; }
// print with a font at a baseline; align 0 = left at x, 1 = centred on x, 2 = right edge at x
// (direct GFX drawing, no background: only for static text on a cleared screen; readouts use the compositor)
static void put(const GFXfont *f, uint16_t col, int x, int base, const char *t, uint8_t align = 0) {
  tft.setFont(f); tft.setTextSize(1); tft.setTextColor(col);
  if (align) x -= align == 1 ? textW(f, t) / 2 : textW(f, t);
  tft.setCursor(x, base); tft.print(t);
  tft.setFont();
}
static void put(const GFXfont *f, uint16_t col, int x, int base, const __FlashStringHelper *t) {   // left-aligned text from flash
  tft.setFont(f); tft.setTextSize(1); tft.setTextColor(col);
  tft.setCursor(x, base); tft.print(t);
  tft.setFont();
}

// ---- compositor: paints a box (background + bars + text) in one top-to-bottom pass ----
// Every scanline is built in a byte buffer of palette indices and streamed into one address window, so old
// pixels are overwritten in place: nothing goes blank before the new text shows up.
#define BX_LAYERS 8
#define BX_COLS 8
struct Layer { const GFXfont *f; const char *s; int16_t x, y; Ext e; uint8_t kind, col; };   // kind 0 rect, 1 GFX text, 2 classic text
static Layer bxL[BX_LAYERS];
static uint8_t bxN, bxNP;
static uint16_t bxPal[BX_COLS];
static int16_t bxX0, bxY0, bxX1, bxY1;      // clip box, inclusive
static uint8_t lineIdx[480];

// what a readout painted last: its columns (to wipe exactly the old text) and a signature of text + colour
struct Span { int16_t x0, x1; uint16_t sig; };
static void spanReset(Span &s) { s.x0 = 1; s.x1 = 0; s.sig = 0; }
static uint16_t sigOf(const char *s, uint16_t col) {
  uint16_t h = 0x811C ^ col;
  while (*s) { h ^= (uint8_t)*s++; h *= 0x0101; h += 7; }
  return h ? h : 1;
}

static void bxBegin(int16_t x, int16_t y, int16_t w, int16_t h, uint16_t bg) {
  bxX0 = max(x, (int16_t)0); bxY0 = max(y, (int16_t)0);
  bxX1 = min((int16_t)(x + w - 1), (int16_t)(W - 1)); bxY1 = min((int16_t)(y + h - 1), (int16_t)(H - 1));
  bxN = 0; bxNP = 1; bxPal[0] = bg;
}
static uint8_t bxColor(uint16_t c) {
  for (uint8_t i = 0; i < bxNP; i++) if (bxPal[i] == c) return i;
  if (bxNP < BX_COLS) bxPal[bxNP] = c;
  return bxNP < BX_COLS ? bxNP++ : BX_COLS - 1;
}
static void bxRect(int16_t x, int16_t y, int16_t w, int16_t h, uint16_t c) {
  if (bxN >= BX_LAYERS || w <= 0 || h <= 0) return;
  Layer &L = bxL[bxN++];
  L.kind = 0; L.col = bxColor(c); L.e = {x, (int16_t)(x + w - 1), y, (int16_t)(y + h - 1)};
}
// same placement rules as put(); f == nullptr = classic 5x7 font with base = top of the cell
static void bxText(const GFXfont *f, uint16_t c, int16_t x, int16_t base, const char *s, uint8_t align = 0) {
  if (bxN >= BX_LAYERS || !*s) return;
  if (align) { uint16_t w = textW(f, s); x -= align == 1 ? w / 2 : w; }
  Layer &L = bxL[bxN++];
  L.kind = f ? 1 : 2; L.col = bxColor(c); L.f = f; L.s = s; L.x = x; L.y = base;
  L.e = textExt(f, s, x, base);
}

static void bxGlyphRow(const Layer &L, int16_t yy, int16_t x0, int16_t w) {
  const GFXfont *f = L.f;
  uint8_t first = pgm_read_byte(&f->first), last = pgm_read_byte(&f->last), col = L.col;
  const GFXglyph *gt = (const GFXglyph *)pgm_read_word(&f->glyph);
  const uint8_t *bm = (const uint8_t *)pgm_read_word(&f->bitmap);
  int16_t cx = L.x - x0;
  for (const char *p = L.s; *p; p++) {
    uint8_t c = *p;
    if (c < first || c > last) continue;
    const GFXglyph *g = gt + (c - first);
    uint8_t gw = pgm_read_byte(&g->width), gh = pgm_read_byte(&g->height);
    int16_t r = yy - (L.y + (int8_t)pgm_read_byte(&g->yOffset));
    if (gw && r >= 0 && r < gh) {
      uint16_t bit = (uint16_t)r * gw;
      const uint8_t *bp = bm + pgm_read_word(&g->bitmapOffset) + (bit >> 3);
      uint8_t mask = 0x80 >> (bit & 7), bits = pgm_read_byte(bp);
      int16_t px = cx + (int8_t)pgm_read_byte(&g->xOffset);
      for (uint8_t k = 0; k < gw; k++, px++) {
        if ((bits & mask) && px >= 0 && px < w) lineIdx[px] = col;
        if (!(mask >>= 1)) { mask = 0x80; bits = pgm_read_byte(++bp); }
      }
    }
    cx += pgm_read_byte(&g->xAdvance);
  }
}

static void bxClassicRow(const Layer &L, int16_t yy, int16_t x0, int16_t w) {
  uint8_t r = yy - L.y, col = L.col;
  if (r > 6) return;                       // row 7 of the 5x7 font is always empty
  int16_t cx = L.x - x0;
  for (const char *p = L.s; *p; p++, cx += 6) {
    uint8_t c = *p;
    if (c >= 176) c++;                     // Adafruit_GFX's default (non-cp437) mapping
    for (uint8_t k = 0; k < 5; k++)
      if ((pgm_read_byte(&font[c * 5 + k]) >> r) & 1) { int16_t px = cx + k; if (px >= 0 && px < w) lineIdx[px] = col; }
  }
}

// Streams columns x0..x1 of the box (all rows) to the screen.
static void bxPaint(int16_t x0, int16_t x1) {
  if (x0 > x1 || bxY0 > bxY1) return;
  int16_t w = x1 - x0 + 1;
#ifdef FAST_LCD
  Bus ph[BX_COLS], pl[BX_COLS];
  bool eq[BX_COLS];
  if (tft.fast) {
    lcdWin(x0, bxY0, x1, bxY1);
    for (uint8_t i = 0; i < bxNP; i++) { ph[i] = busFor(bxPal[i] >> 8); pl[i] = busFor(bxPal[i]); eq[i] = (bxPal[i] >> 8) == (bxPal[i] & 0xFF); }
  } else
#endif
    tft.setAddrWindow(x0, bxY0, x1, bxY1);
  bool first = true;
  for (int16_t yy = bxY0; yy <= bxY1; yy++) {
#ifdef FAST_LCD
    if (tft.fast) {                        // rows with nothing on them: just the background
      bool any = false;
      for (uint8_t i = 0; i < bxN && !any; i++) any = yy >= bxL[i].e.y0 && yy <= bxL[i].e.y1 && bxL[i].e.x1 >= x0 && bxL[i].e.x0 <= x1;
      if (!any) {
        uint16_t n = w;
        if (eq[0]) { busSet(ph[0]); do { WR_PULSE(); WR_PULSE(); } while (--n); }
        else { Bus a = ph[0], b = pl[0]; do { busPut(a); busPut(b); } while (--n); }
        continue;
      }
    }
#endif
    memset(lineIdx, 0, w);
    for (uint8_t i = 0; i < bxN; i++) {
      const Layer &L = bxL[i];
      if (yy < L.e.y0 || yy > L.e.y1) continue;
      if (L.kind == 0) {
        int16_t a = max(L.e.x0, x0), b = min(L.e.x1, x1);
        if (a <= b) memset(lineIdx + (a - x0), L.col, b - a + 1);
      } else if (L.e.x1 >= x0 && L.e.x0 <= x1) {
        if (L.kind == 1) bxGlyphRow(L, yy, x0, w); else bxClassicRow(L, yy, x0, w);
      }
    }
#ifdef FAST_LCD
    if (tft.fast) {                        // stream the row as runs of one colour
      const uint8_t *p = lineIdx, *e = lineIdx + w;
      while (p < e) {
        uint8_t c = *p++;
        uint16_t n = 1;
        while (p < e && *p == c) { p++; n++; }
        if (eq[c]) { busSet(ph[c]); do { WR_PULSE(); WR_PULSE(); } while (--n); }
        else { Bus a = ph[c], b = pl[c]; do { busPut(a); busPut(b); } while (--n); }
      }
      pump();
      continue;
    }
#endif
    for (int16_t i = 0; i < w;) {          // generic path through the library
      uint16_t tmp[24];
      uint8_t k = min(24, w - i);
      for (uint8_t j = 0; j < k; j++) tmp[j] = bxPal[lineIdx[i + j]];
      tft.pushColors(tmp, k, first); first = false; i += k;
    }
    pump();
  }
#ifdef FAST_LCD
  if (tft.fast) lcdDone();
#endif
}

// Paints the box. With a span, only the columns of the old text plus the new text are painted (and remembered).
static void bxEnd(Span *sp = nullptr) {
  int16_t x0 = bxX0, x1 = bxX1;
  if (sp) {
    int16_t n0 = 32767, n1 = -32768;
    for (uint8_t i = 0; i < bxN; i++) { n0 = min(n0, bxL[i].e.x0); n1 = max(n1, bxL[i].e.x1); }
    n0 = max(n0, bxX0); n1 = min(n1, bxX1);
    x0 = n0; x1 = n1;
    if (sp->x0 <= sp->x1) { x0 = min(x0, sp->x0); x1 = max(x1, sp->x1); }
    sp->x0 = n0; sp->x1 = n1;
    x0 = max(x0, bxX0); x1 = min(x1, bxX1);
  }
  bxPaint(x0, x1);
}

// Cells and page rows remember what each of their layers looked like (content signature + columns), so a
// redraw repaints only the columns where a layer changed: a new value next to a static label leaves the label alone.
struct LSig { uint16_t sig; uint8_t a, b; };     // columns a*2 .. b*2+1
#define LS_ROWS 10
static LSig lsig[LS_ROWS][BX_LAYERS];
static uint8_t lsN[LS_ROWS];                     // 255: unknown, repaint the whole box
static uint16_t lsBox[LS_ROWS];                  // geometry of the box last painted there
static void lsReset() { memset(lsN, 255, sizeof lsN); }

static uint16_t layerSig(const Layer &L) {
  uint16_t h = 0x9E37 ^ L.kind ^ bxPal[L.col];
  h = h * 0x0101 + L.e.x0; h = h * 0x0101 + L.e.x1; h = h * 0x0101 + L.e.y0; h = h * 0x0101 + L.e.y1;
  if (L.kind) {
    h ^= (uint16_t)L.f;
    for (const char *p = L.s; *p; p++) h = (h ^ (uint8_t)*p) * 0x0101 + 7;
  }
  return h;
}

static bool hasSig(const LSig *v, uint8_t n, const LSig &x) {
  for (uint8_t i = 0; i < n; i++) if (v[i].sig == x.sig && v[i].a == x.a && v[i].b == x.b) return true;
  return false;
}
static void markCols(uint8_t *d, uint8_t a, uint8_t b) { for (uint8_t c = a; c <= b && c < 240; c++) d[c >> 3] |= 1 << (c & 7); }

// like bxEnd(), for cell / row `row`: paints only the columns whose layers differ from the last call
static void bxEndDiff(uint8_t row) {
  LSig ns[BX_LAYERS];
  uint8_t nn = 0;
  for (uint8_t i = 0; i < bxN; i++) {
    const Layer &L = bxL[i];
    int16_t a = max(L.e.x0, bxX0), b = min(L.e.x1, bxX1);
    if (a > b || L.e.y1 < bxY0 || L.e.y0 > bxY1) continue;
    ns[nn++] = {layerSig(L), (uint8_t)(a >> 1), (uint8_t)(b >> 1)};
  }
  LSig *os = lsig[row];
  uint8_t on = lsN[row];
  uint16_t box = (bxX0 << 7) ^ (bxX1 - bxX0) ^ ((bxY1 - bxY0) << 10) ^ bxPal[0];   // a new background repaints it all
  if (on == 255 || box != lsBox[row]) bxPaint(bxX0, bxX1);   // unknown or different box: all of it
  else {
    uint8_t d[30];
    memset(d, 0, sizeof d);
    for (uint8_t i = 0; i < on; i++) if (!hasSig(ns, nn, os[i])) markCols(d, os[i].a, os[i].b);
    for (uint8_t i = 0; i < nn; i++) if (!hasSig(os, on, ns[i])) markCols(d, ns[i].a, ns[i].b);
    int16_t runA = -1, runB = -100;      // dirty runs; gaps up to 16 px are painted through (fewer windows)
    for (int16_t c = 0; c < 240; c++) {
      if (!(d[c >> 3] & (1 << (c & 7)))) continue;
      if (runA >= 0 && c - runB <= 8) { runB = c; continue; }
      if (runA >= 0) bxPaint(max(bxX0, (int16_t)(runA * 2)), min(bxX1, (int16_t)(runB * 2 + 1)));
      runA = runB = c;
    }
    if (runA >= 0) bxPaint(max(bxX0, (int16_t)(runA * 2)), min(bxX1, (int16_t)(runB * 2 + 1)));
  }
  memcpy(os, ns, nn * sizeof(LSig));
  lsN[row] = nn; lsBox[row] = box;
}

// ---- latest stats ----
struct Stats {
  uint8_t cpu, ram, swap, cpuT, gpu, gpuT, vram;
  uint32_t down, up;          // network rates, kbit/s
  uint32_t uptime;
  uint16_t load[3];            // load averages x100
  uint8_t ncpu;
  uint16_t gpuW, gpuLim;
  char clock[8];
} st;

#define HIST 87
static uint32_t lastRx;
static bool online = false;

static uint16_t swapColor(uint8_t v) { return v >= 60 ? RED : v >= 25 ? ORANGE : SWAP_OK; }
static uint16_t tempColor(uint8_t t, uint8_t warn) { return t >= 80 ? RED : t >= warn ? ORANGE : TEMP_OK; }
static uint16_t levelColor(uint8_t v, uint8_t warn = 60, uint8_t crit = 85) {
  return v >= crit ? RED : v >= warn ? YELLOW : GREEN;
}

// ---- arc gauge: 270 degrees, N segments, only changed segments get redrawn ----
#define SEGS 30
struct Gauge { int cx, cy, ro, ri; const char *label; uint8_t lit; uint16_t col; uint8_t val; uint8_t warn, crit; uint16_t cOk, cWarn; Span sp; };
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

// Scanline spans of every segment, precomputed by tools/gauge_segments.py from the float maths and the two
// Adafruit_GFX fillTriangle calls the gauges used at run time (same pixels; that cost ~2.5 ms per segment).
// generated by tools/gauge_segments.py: segment spans (dy, dx, width) relative to the gauge centre
static const int8_t SEG_SPANS[2478] PROGMEM = {
  32,-45,1,33,-46,2,34,-47,4,35,-48,6,36,-49,8,37,-50,10,38,-50,11,39,-49,9,
  40,-48,7,41,-47,5,42,-46,3,43,-45,1,25,-49,1,26,-51,3,27,-53,6,28,-55,9,
  29,-55,9,30,-54,9,31,-54,10,32,-53,8,33,-53,6,34,-52,4,35,-51,1,17,-53,1,
  18,-56,4,19,-59,7,20,-59,8,21,-59,8,22,-58,8,23,-58,8,24,-58,9,25,-57,6,
  26,-57,4,27,-56,1,9,-55,1,10,-62,8,11,-62,8,12,-62,8,13,-62,9,14,-61,8,
  15,-61,8,16,-61,9,17,-61,6,18,-60,1,0,-62,8,1,-62,8,2,-62,8,3,-62,8,
  4,-62,8,5,-62,8,6,-62,8,7,-62,8,8,-62,1,-9,-62,1,-8,-62,8,-7,-62,8,
  -6,-62,8,-5,-62,8,-4,-62,8,-3,-62,8,-2,-62,8,-1,-62,8,-19,-60,1,-18,-60,4,
  -17,-60,8,-16,-60,8,-15,-61,9,-14,-61,9,-13,-61,8,-12,-61,8,-11,-62,9,-10,-55,1,
  -28,-56,1,-27,-56,3,-26,-56,5,-25,-57,8,-24,-57,8,-23,-57,8,-22,-58,8,-21,-58,8,
  -20,-59,8,-19,-56,5,-18,-53,1,-36,-51,1,-35,-51,2,-34,-52,5,-33,-52,6,-32,-53,9,
  -31,-53,9,-30,-54,9,-29,-55,9,-28,-53,7,-27,-51,4,-26,-49,1,-44,-45,1,-43,-45,2,
  -42,-46,4,-41,-47,6,-40,-48,8,-39,-49,10,-38,-50,11,-37,-49,9,-36,-48,7,-35,-47,5,
  -34,-46,3,-33,-45,1,-50,-38,1,-49,-39,3,-48,-40,5,-47,-41,7,-46,-42,9,-45,-44,12,
  -44,-43,10,-43,-42,8,-42,-41,6,-41,-40,4,-40,-39,1,-55,-29,1,-54,-30,2,-53,-32,5,
  -52,-34,7,-51,-36,10,-50,-36,10,-49,-35,10,-48,-34,8,-47,-34,6,-46,-33,4,-45,-32,1,
  -59,-20,1,-58,-22,3,-57,-25,6,-56,-28,10,-55,-28,10,-54,-27,9,-53,-27,10,-52,-26,7,
  -51,-26,5,-50,-25,1,-62,-11,1,-61,-15,5,-60,-19,9,-59,-19,9,-58,-19,9,-57,-19,9,
  -56,-18,8,-55,-18,9,-54,-18,6,-53,-17,1,-62,-9,9,-61,-9,9,-60,-9,9,-59,-9,9,
  -58,-9,9,-57,-9,9,-56,-9,9,-55,-8,8,-62,0,9,-61,0,9,-60,0,9,-59,0,9,
  -58,0,9,-57,0,9,-56,0,9,-55,0,8,-62,10,1,-61,10,5,-60,10,9,-59,10,9,
  -58,10,9,-57,10,9,-56,10,8,-55,9,9,-54,12,6,-53,16,1,-59,19,1,-58,19,3,
  -57,19,6,-56,18,10,-55,18,10,-54,18,9,-53,17,10,-52,19,7,-51,21,5,-50,24,1,
  -55,28,1,-54,28,2,-53,27,5,-52,27,7,-51,26,10,-50,26,10,-49,25,10,-48,26,8,
  -47,28,6,-46,29,4,-45,31,1,-50,37,1,-49,36,3,-48,35,5,-47,34,7,-46,33,9,
  -45,32,12,-44,33,10,-43,34,8,-42,35,6,-41,36,4,-40,38,1,-44,44,1,-43,43,2,
  -42,42,4,-41,41,6,-40,40,8,-39,39,10,-38,39,11,-37,40,9,-36,41,7,-35,42,5,
  -34,43,3,-33,44,1,-36,50,1,-35,49,2,-34,47,5,-33,46,6,-32,44,9,-31,44,9,
  -30,45,9,-29,46,9,-28,46,7,-27,47,4,-26,48,1,-28,55,1,-27,53,3,-26,51,5,
  -25,49,8,-24,49,8,-23,49,8,-22,50,8,-21,50,8,-20,51,8,-19,51,5,-18,52,1,
  -19,59,1,-18,56,4,-17,52,8,-16,52,8,-15,52,9,-14,52,9,-13,53,8,-12,53,8,
  -11,53,9,-10,54,1,-9,61,1,-8,54,8,-7,54,8,-6,54,8,-5,54,8,-4,54,8,
  -3,54,8,-2,54,8,-1,54,8,0,54,8,1,54,8,2,54,8,3,54,8,4,54,8,
  5,54,8,6,54,8,7,54,8,8,61,1,9,54,1,10,54,8,11,54,8,12,54,8,
  13,53,9,14,53,8,15,53,8,16,52,9,17,55,6,18,59,1,17,52,1,18,52,4,
  19,52,7,20,51,8,21,51,8,22,50,8,23,50,8,24,49,9,25,51,6,26,53,4,
  27,55,1,25,48,1,26,48,3,27,47,6,28,46,9,29,46,9,30,45,9,31,44,10,
  32,45,8,33,47,6,34,48,4,35,50,1,32,44,1,33,44,2,34,43,4,35,42,6,
  36,41,8,37,40,10,38,39,11,39,40,9,40,41,7,41,42,5,42,43,3,43,44,1,
  28,-38,1,29,-39,3,30,-41,6,31,-43,9,32,-42,9,33,-41,7,34,-40,5,35,-39,3,
  36,-38,1,21,-42,1,22,-43,2,23,-45,5,24,-47,7,25,-47,8,26,-46,7,27,-46,8,
  28,-45,6,29,-45,4,30,-44,1,15,-45,1,16,-48,4,17,-51,7,18,-51,8,19,-50,7,
  20,-50,8,21,-49,6,22,-49,4,23,-48,1,7,-47,1,8,-50,4,9,-53,7,10,-53,8,
  11,-53,8,12,-52,7,13,-52,8,14,-52,5,15,-51,1,0,-53,7,1,-53,7,2,-53,7,
  3,-53,7,4,-53,7,5,-53,7,6,-53,7,7,-53,1,-8,-53,1,-7,-53,7,-6,-53,7,
  -5,-53,7,-4,-53,7,-3,-53,7,-2,-53,7,-1,-53,7,-16,-51,1,-15,-51,4,-14,-51,7,
  -13,-52,8,-12,-52,8,-11,-52,7,-10,-53,8,-9,-50,5,-8,-47,1,-24,-48,1,-23,-48,2,
  -22,-49,5,-21,-49,7,-20,-50,8,-19,-50,8,-18,-51,8,-17,-48,5,-16,-45,1,-31,-44,1,
  -30,-44,2,-29,-45,5,-28,-45,7,-27,-46,8,-26,-46,7,-25,-47,8,-24,-46,6,-23,-44,4,
  -22,-42,1,-37,-38,1,-36,-39,3,-35,-40,5,-34,-41,7,-33,-42,9,-32,-43,9,-31,-42,7,
  -30,-40,4,-29,-38,1,-43,-32,1,-42,-33,2,-41,-34,4,-40,-35,5,-39,-36,7,-38,-37,9,
  -37,-36,7,-36,-35,5,-35,-34,3,-34,-33,1,-47,-25,1,-46,-27,3,-45,-29,6,-44,-31,8,
  -43,-31,9,-42,-30,9,-41,-30,7,-40,-29,4,-39,-28,1,-51,-18,1,-50,-20,3,-49,-22,5,
  -48,-24,8,-47,-24,8,-46,-23,7,-45,-23,8,-44,-22,5,-43,-21,1,-53,-10,1,-52,-13,4,
  -51,-16,7,-50,-16,8,-49,-16,8,-48,-15,7,-47,-15,8,-46,-15,5,-45,-14,1,-53,-8,8,
  -52,-8,8,-51,-8,8,-50,-8,8,-49,-8,8,-48,-8,8,-47,-7,7,-53,0,8,-52,0,8,
  -51,0,8,-50,0,8,-49,0,8,-48,0,8,-47,0,7,-53,9,1,-52,9,4,-51,9,7,
  -50,8,8,-49,8,8,-48,8,7,-47,7,8,-46,10,5,-45,13,1,-51,17,1,-50,17,3,
  -49,17,5,-48,16,8,-47,16,8,-46,16,7,-45,15,8,-44,17,5,-43,20,1,-47,24,1,
  -46,24,3,-45,23,6,-44,23,8,-43,22,9,-42,21,9,-41,23,7,-40,25,4,-39,27,1,
  -43,31,1,-42,31,2,-41,30,4,-40,30,5,-39,29,7,-38,28,9,-37,29,7,-36,30,5,
  -35,31,3,-34,32,1,-37,37,1,-36,36,3,-35,35,5,-34,34,7,-33,33,9,-32,34,9,
  -31,35,7,-30,36,4,-29,37,1,-31,43,1,-30,42,2,-29,40,5,-28,38,7,-27,38,8,
  -26,39,7,-25,39,8,-24,40,6,-23,40,4,-22,41,1,-24,47,1,-23,46,2,-22,44,5,
  -21,42,7,-20,42,8,-19,42,8,-18,43,8,-17,43,5,-16,44,1,-16,50,1,-15,47,4,
  -14,44,7,-13,44,8,-12,44,8,-11,45,7,-10,45,8,-9,45,5,-8,46,1,-8,52,1,
  -7,46,7,-6,46,7,-5,46,7,-4,46,7,-3,46,7,-2,46,7,-1,46,7,0,46,7,
  1,46,7,2,46,7,3,46,7,4,46,7,5,46,7,6,46,7,7,52,1,7,46,1,
  8,46,4,9,46,7,10,45,8,11,45,8,12,45,7,13,44,8,14,47,5,15,50,1,
  15,44,1,16,44,4,17,44,7,18,43,8,19,43,7,20,42,8,21,43,6,22,45,4,
  23,47,1,21,41,1,22,41,2,23,40,5,24,40,7,25,39,8,26,39,7,27,38,8,
  28,39,6,29,41,4,30,43,1,28,37,1,29,36,3,30,35,6,31,34,9,32,33,9,
  33,34,7,34,35,5,35,36,3,36,37,1,23,-32,1,24,-33,3,25,-35,6,26,-37,9,
  27,-37,10,28,-36,8,29,-35,6,30,-34,4,31,-33,1,18,-35,1,19,-37,3,20,-40,7,
  21,-40,8,22,-39,8,23,-39,7,24,-38,4,25,-37,1,12,-37,1,13,-40,4,14,-43,7,
  15,-43,8,16,-43,8,17,-42,8,18,-42,5,19,-41,1,6,-39,1,7,-45,7,8,-45,7,
  9,-45,7,10,-44,6,11,-44,7,12,-44,5,13,-43,1,0,-45,7,1,-45,7,2,-45,7,
  3,-45,7,4,-45,7,5,-45,7,6,-45,1,-7,-45,1,-6,-45,7,-5,-45,7,-4,-45,7,
  -3,-45,7,-2,-45,7,-1,-45,7,-14,-43,1,-13,-43,3,-12,-43,6,-11,-44,7,-10,-44,7,
  -9,-44,7,-8,-45,8,-7,-39,1,-20,-41,1,-19,-41,4,-18,-41,7,-17,-42,8,-16,-42,8,
  -15,-43,8,-14,-40,5,-13,-37,1,-26,-37,1,-25,-37,2,-24,-38,5,-23,-38,7,-22,-39,8,
  -21,-40,8,-20,-38,5,-19,-35,1,-32,-33,1,-31,-33,2,-30,-34,4,-29,-35,6,-28,-36,9,
  -27,-37,9,-26,-36,7,-25,-34,4,-24,-32,1,-37,-27,1,-36,-28,2,-35,-29,4,-34,-30,5,
  -33,-32,8,-32,-32,9,-31,-31,7,-30,-30,5,-29,-29,3,-28,-28,1,-40,-21,1,-39,-22,2,
  -38,-24,4,-37,-26,7,-36,-26,7,-35,-25,7,-34,-25,6,-33,-24,4,-32,-23,1,-43,-15,1,
  -42,-17,3,-41,-20,6,-40,-20,7,-39,-20,7,-38,-19,6,-37,-19,7,-36,-19,5,-35,-18,1,
  -45,-8,1,-44,-11,4,-43,-14,7,-42,-14,7,-41,-14,7,-40,-13,6,-39,-13,7,-38,-12,1,
  -45,-7,7,-44,-7,7,-43,-7,7,-42,-7,7,-41,-7,7,-40,-7,7,-39,-6,6,-45,0,7,
  -44,0,7,-43,0,7,-42,0,7,-41,0,7,-40,0,7,-39,0,6,-45,7,1,-44,7,4,
  -43,7,7,-42,7,7,-41,7,7,-40,7,6,-39,6,7,-38,11,1,-43,14,1,-42,14,3,
  -41,14,6,-40,13,7,-39,13,7,-38,13,6,-37,12,7,-36,14,5,-35,17,1,-40,20,1,
  -39,20,2,-38,20,4,-37,19,7,-36,19,7,-35,18,7,-34,19,6,-33,20,4,-32,22,1,
  -37,26,1,-36,26,2,-35,25,4,-34,25,5,-33,24,8,-32,23,9,-31,24,7,-30,25,5,
  -29,26,3,-28,27,1,-32,32,1,-31,31,2,-30,30,4,-29,29,6,-28,27,9,-27,28,9,
  -26,29,7,-25,30,4,-24,31,1,-26,36,1,-25,35,2,-24,33,5,-23,31,7,-22,31,8,
  -21,32,8,-20,33,5,-19,34,1,-20,40,1,-19,37,4,-18,34,7,-17,34,8,-16,34,8,
  -15,35,8,-14,35,5,-13,36,1,-14,42,1,-13,40,3,-12,37,6,-11,37,7,-10,37,7,
  -9,37,7,-8,37,8,-7,38,1,-7,44,1,-6,38,7,-5,38,7,-4,38,7,-3,38,7,
  -2,38,7,-1,38,7,0,38,7,1,38,7,2,38,7,3,38,7,4,38,7,5,38,7,
  6,44,1,6,38,1,7,38,7,8,38,7,9,38,7,10,38,6,11,37,7,12,39,5,
  13,42,1,12,36,1,13,36,4,14,36,7,15,35,8,16,35,8,17,34,8,18,37,5,
  19,40,1,18,34,1,19,34,3,20,33,7,21,32,8,22,31,8,23,32,7,24,34,4,
  25,36,1,23,31,1,24,30,3,25,29,6,26,28,9,27,27,10,28,28,8,29,29,6,
  30,30,4,31,32,1,
};
static const uint16_t SEG_IDX[3][31] PROGMEM = {
  {0,36,69,102,132,159,186,216,249,282,318,351,384,414,444,468,492,522,552,585,618,654,687,720,750,777,804,834,867,900,936},   // r 62..55
  {936,963,993,1020,1047,1071,1095,1122,1149,1179,1206,1236,1263,1290,1317,1338,1359,1386,1413,1440,1470,1497,1527,1554,1581,1605,1629,1656,1683,1713,1740},   // r 53..47
  {1740,1767,1791,1815,1839,1860,1881,1905,1929,1953,1980,2010,2037,2064,2088,2109,2130,2154,2181,2208,2238,2265,2289,2313,2337,2358,2379,2403,2427,2451,2478},   // r 45..39
};

static void segment(const Gauge &g, int i, uint16_t c) {
  uint8_t k = g.ro == 62 ? 0 : g.ro == 53 ? 1 : 2;     // ring type: 62/55, 53/47, 45/39
  const int8_t *p = SEG_SPANS + pgm_read_word(&SEG_IDX[k][i]), *e = SEG_SPANS + pgm_read_word(&SEG_IDX[k][i + 1]);
  for (; p < e; p += 3)
    tft.fillRect(g.cx + (int8_t)pgm_read_byte(p + 1), g.cy + (int8_t)pgm_read_byte(p), (int8_t)pgm_read_byte(p + 2), 1, c);
  pump();
}

static uint16_t gaugeColor(const Gauge &g, uint8_t v) { return v >= g.crit ? RED : v >= g.warn ? g.cWarn : g.cOk; }

static void gaugeFrame(Gauge &g) {
  for (int i = 0; i < SEGS; i++) segment(g, i, DARK);
  if (g.label) {
    tft.fillRect(g.cx - 30, g.cy + 48, 60, 20, BLACK);
    put(&FreeSansBold9pt7b, GREY, g.cx, g.cy + 63, g.label, 1);
  }
  g.lit = 0; g.col = 0; g.val = 255;
  spanReset(g.sp);
}

// v = fill percent, cv = value that decides the colour (same as v for load gauges, the temperature for rings)
static void gaugeUpdate(Gauge &g, uint8_t v, uint8_t cv) {
  if (cv == g.val) return;
  g.val = cv;
  uint8_t lit = (uint16_t)v * SEGS / 100;
  uint16_t col = gaugeColor(g, cv);
  if (col != g.col) {
    for (int i = 0; i < lit; i++) segment(g, i, col);
    for (int i = lit; i < g.lit; i++) segment(g, i, DARK);
  } else if (lit > g.lit) {
    for (int i = g.lit; i < lit; i++) segment(g, i, col);
  } else {
    for (int i = lit; i < g.lit; i++) segment(g, i, DARK);
  }
  g.lit = lit; g.col = col;
}

// the big percentage in the middle of a load gauge: only the columns of the old and new number are repainted
static void gaugeLabel(Gauge &g, uint8_t v) {
  char b[8]; snprintf_P(b, sizeof b, PSTR("%d"), v);
  uint16_t col = gaugeColor(g, v);
  uint16_t sig = sigOf(b, col);
  if (sig == g.sp.sig) return;
  g.sp.sig = sig;
  int w1 = textW(&FreeSansBold18pt7b, b), w2 = textW(&FreeSansBold12pt7b, "%");
  int x = g.cx - (w1 + 2 + w2) / 2;
  bxBegin(g.cx - 44, g.cy - 14, 88, 28, BLACK);
  bxText(&FreeSansBold18pt7b, col, x, g.cy + 12, b);
  bxText(&FreeSansBold12pt7b, col, x + w1 + 2, g.cy + 12, "%");
  bxEnd(&g.sp);
}

static uint8_t tempPct(uint8_t t) { return t <= 20 ? 0 : t >= 100 ? 100 : (t - 20) * 100 / 80; }

// ---- helpers ----
static void panelTitle(int x, int y, const __FlashStringHelper *s) {
  tft.setTextSize(1); tft.setTextColor(GREY);
  tft.setCursor(x, y); tft.print(s);
}
// the i-th string of a PROGMEM list of NUL-separated strings
static PGM_P nthP(PGM_P s, uint8_t i) { while (i--) s += strlen_P(s) + 1; return s; }
#define FS(p) ((const __FlashStringHelper *)(p))

// ---- info panel (bottom band): the PC lays out cells and pages, we only draw ----
#define PX 28
#define PY 260        // band: two 20 px rows, y 260..299; the menu strip owns y 300..319
#define MENU_Y 300
// 9 and 10 are the accent of the notice page and its dimmed twin (set by the PC with frame 12)
static uint16_t PAL[11] = {WHITE, GREY, GREEN, YELLOW, ORANGE, RED, CYAN, TEMP_OK, MAGENTA, CYAN, DARK};
static uint16_t cellHash[6];                 // skip redraws of identical cells
static uint8_t bIdx = 255, bCnt = 255, bSev = 255, bBorder = 255;
static uint8_t bandDirty;                    // slots still showing the previous band page (bit per slot)
static uint32_t bandDirtyT;

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
  if (wide) { cellHash[slot + 1] = 0; lsN[slot + 1] = 255; }
  char txt[73];
  uint8_t tl = min((int)len - 3, 72);
  memcpy(txt, p + 3, tl); txt[tl] = 0;
  char *val = strchr(txt, '\t');
  if (val) *val++ = 0;
  bandDirty &= ~(wide ? 3 << slot : 1 << slot);
  int x0 = PX + (slot & 1) * 224, y0 = PY + (slot >> 1) * 20, w = wide ? 446 : 222;
  uint16_t col = PAL[min(style & 15, 10)];
  int base = big ? y0 + 17 : y0 + 15;
  // one pass over the cell box, so the old cell is overwritten, never blanked. For the right column the box also
  // takes the 2 px gap: a wide cell of the previous page may have drawn there, and the band is not cleared as a whole.
  int gx = (slot & 1) ? 2 : 0;
  bxBegin(x0 - gx, y0, w + gx, 20, BLACK);
  bxText(big ? &FreeSansBold12pt7b : &FreeSans9pt7b, (bright || big) ? col : bar ? WHITE : GREY, x0, base, txt);
  if (val) {
    if (bar) {
      int bx = x0 + (wide ? 96 : 70), bw = wide ? 230 : 100, fill = (long)(bw - 4) * min((int)pct, 100) / 100;
      bxRect(bx, y0 + 6, bw, 9, DARK);                       // quiet track, no outline
      if (fill > 0) bxRect(bx + 2, y0 + 8, fill, 5, col);
      bxText(&FreeSansBold9pt7b, col, bx + bw + 6, y0 + 15, val);
    } else {
      bxText(big ? &FreeSansBold12pt7b : &FreeSansBold9pt7b, col, x0 + w, y0 + (big ? 17 : 15), val, 2);
    }
  }
  bxEndDiff(slot);
}

// ---- full-screen pages: the PC replaces the whole dashboard with a list of rows ----
// Row 0 is the title (big), rows 1..9 are data lines: [row | style | pct | "label\tvalue"].
static bool pageMode = false, pageScope = false, pageLogo = false, pageNet = false, pageNote = false;
static bool netVisible() { return pageMode && pageNet && online; }
static uint16_t lineHash[10];
#define LROW 31

static void drawLine(const uint8_t *p, uint8_t len) {
  if (len < 3 || p[0] > 9) return;
  uint8_t row = p[0], style = p[1], pct = p[2];
  uint16_t h = fnv(p, len);
  if (lineHash[row] == h) return;
  lineHash[row] = h;
  char txt[73];
  uint8_t tl = min((int)len - 3, 72);
  memcpy(txt, p + 3, tl); txt[tl] = 0;
  bool bar = style & 0x10, big = style & 0x40, bright = style & 0x80;
  bool sub = bar && (style & 0x20);        // bar row with a thin swap bar under it: text starts with the swap % as 3 digits
  uint8_t swp = 0;
  if (sub && tl >= 3) { swp = min(100, (txt[0] - '0') * 100 + (txt[1] - '0') * 10 + (txt[2] - '0')); memmove(txt, txt + 3, tl - 2); }
  else sub = false;
  char *val = strchr(txt, '\t');
  if (val) *val++ = 0;
  uint16_t col = PAL[min(style & 15, 10)];
  int y = 4 + row * LROW;
  char sw[6];
  // one pass over the whole row (the swap row is 2 px taller): label, title rule, bar, value
  // a notice: the header row is filled with the accent (dark text), the other rows leave room for the accent strip
  bool head = pageNote && row == 0;
  int x0 = pageNote && !head ? 8 : 0, lx = pageNote && !head ? 22 : 14;
  if (head) bxBegin(0, 0, W, LROW + 4, col); else bxBegin(x0, y, W - x0, LROW + (sub ? 2 : 0), BLACK);
  // label: title in bold 18, rows in regular 12 (cloud when the row has a bar, mist otherwise)
  int base = y + (head ? 24 : big ? 25 : 21);
  bxText(big ? &FreeSansBold18pt7b : &FreeSans12pt7b, head ? BLACK : big || bright ? col : bar ? WHITE : GREY, lx, base, txt);
  if (big && row == 0 && !pageNote) bxRect(14, y + LROW - 4, W - 28, 1, DARK);
  if (val) {
    if (bar) {
      int bx = 172, bw = 210, fill = (long)(bw - 4) * min((int)pct, 100) / 100;
      bxRect(bx, y + 7, bw, 12, DARK);                       // quiet track, no outline
      if (fill > 0) bxRect(bx + 2, y + 9, fill, 8, col);
      bxText(&FreeSansBold12pt7b, col, bx + bw + 10, base, val);
      if (sub) {                                             // thin swap bar under the RAM bar
        uint16_t sc = swapColor(swp);
        bxRect(bx, y + 26, bw, 4, DARK);
        if (swp) bxRect(bx + 1, y + 27, (long)(bw - 2) * swp / 100, 2, sc);
        bxText(nullptr, sc, 14, y + 24, "swap");
        snprintf_P(sw, sizeof sw, PSTR("%u%%"), swp);
        bxText(nullptr, sc, bx + bw + 10, y + 25, sw);
      }
    } else {
      bxText(&FreeSansBold12pt7b, head ? BLACK : col, W - 14, base, val, 2);
    }
  }
  bxEndDiff(row);
  if (row == 0 && pageLogo) claudeLogo(30, 19, 13);   // the Claude spark in the title row, redrawn whenever the row is
}

static void setScreen(uint8_t mode);   // defined after drawStatic()

static void drawBand(const uint8_t *p, uint8_t len) {   // page idx | page count | flags
  if (len < 3) return;
  uint8_t idx = p[0], cnt = min((int)p[1], 6), sev = p[2] & 3, border = (p[2] & 8) != 0;
  // new band page: the old cells stay until the new ones overwrite them; slots left unused are blanked later
  if (p[2] & 4) { bandDirty = 0x0F; bandDirtyT = millis(); memset(cellHash, 0, sizeof cellHash); lsReset(); }
  if (cnt != bCnt || idx != bIdx) {
    if (cnt != bCnt) tft.fillRect(14, 258, 12, 42, BLACK);
    for (int i = 0; i < cnt; i++) tft.fillRect(18, 262 + i * 7, 4, 4, i == idx ? CYAN : DARK);
    bCnt = cnt; bIdx = idx;
  }
  if (sev != bSev || border != bBorder) {
    uint16_t c = !sev || !border ? BLACK : sev == 3 ? RED : sev == 2 ? ORANGE : CYAN;
    tft.drawRect(27, 258, 451, 42, c);
    bSev = sev; bBorder = border;
  }
}

static void bandFlush() {   // blank the slots the new band page did not paint (the PC sends its cells within ~0.3 s)
  if (!bandDirty || pageMode || millis() - bandDirtyT < 600) return;
  for (uint8_t s = 0; s < 4; s++)
    if (bandDirty & (1 << s)) {
      tft.fillRect((s & 1) ? PX + 222 : PX, PY + (s >> 1) * 20, (s & 1) ? 224 : 222, 20, BLACK);
      lsN[s] = 255;
    }
  bandDirty = 0;
}

static void noteStrip() { tft.fillRect(0, LROW + 4, 6, MENU_Y - LROW - 4, PAL[9]); }

// a notice colour changed: on the notice page every row is drawn again in it (the PC resends them)
static void setAccent(uint8_t idx, uint16_t c) {
  if (idx < 9 || idx > 10 || PAL[idx] == c) return;
  PAL[idx] = c;
  if (!pageMode || !pageNote) return;
  memset(lineHash, 0, sizeof lineHash); lsReset();
  if (idx == 9) noteStrip();
}

static void drawPanelItem() {
  if (!qn) return;
  Item it = q[qh];
  qh = (qh + 1) % 6; qn--;
  if (it.type == 8) setScreen(it.d[0]);    // screen switch: 0 dashboard, 1 full page, 2 full page + clear
  else if (it.type == 9) { if (pageMode) drawLine(it.d, it.len); }   // full-screen row
  else if (it.type == 12) setAccent(it.d[0], it.d[1] | it.d[2] << 8);
  else if (pageMode) { /* band/cell frames are stale while a full page is up */ }
  else if (it.type == 5) drawCell(it.d, it.len);
  else drawBand(it.d, it.len);
}

// queued band / row frames, oldest first, until the queue is empty or the time budget is used up
static void drawPanelItems(uint16_t budgetMs) {
  uint32_t t0 = millis();
  while (qn && millis() - t0 < budgetMs) { drawPanelItem(); pump(); }
}

// ---- small text under the gauges (temperatures, VRAM) ----
static Span spNote[3], spLoad, spVram;


static void note(Span &sp, const Gauge &g, const char *b, uint16_t col) {   // small centred text under the %
  uint16_t sig = sigOf(b, col);
  if (sig == sp.sig) return;
  sp.sig = sig;
  bxBegin(g.cx - 40, g.cy + 15, 80, 15, BLACK);
  bxText(&FreeSans9pt7b, col, g.cx, g.cy + 28, b, 1);   // baseline below the % inside the ring
  bxEnd(&sp);
}

static void gaugeNotes() {
  char b[32];
  uint8_t wp = st.gpuLim ? min(100UL, (unsigned long)st.gpuW * 100 / st.gpuLim) : 0;
  gaugeUpdate(T[0], tempPct(st.cpuT), st.cpuT);         // rings first: the text below is drawn over them
  gaugeUpdate(T[1], tempPct(st.gpuT), st.gpuT);
  gaugeUpdate(T[2], st.swap, st.swap);
  gaugeUpdate(T[3], wp, wp);
  snprintf_P(b, sizeof b, PSTR("%dC"), st.cpuT);
  note(spNote[0], G[0], b, tempColor(st.cpuT, 60));
  snprintf_P(b, sizeof b, PSTR("%dC"), st.gpuT);
  note(spNote[1], G[2], b, tempColor(st.gpuT, 65));
  snprintf_P(b, sizeof b, PSTR("sw %d%%"), st.swap);
  note(spNote[2], G[1], b, swapColor(st.swap));
  pump();
  {                                                    // 1 and 5 min load inside the CPU gauge, above the %
    uint32_t per = (uint32_t)st.load[0] * 100 / st.ncpu;   // 1-min load per core, percent
    uint16_t col = per >= 100 ? RED : per >= 60 ? ORANGE : GREY;
    char ip[2][6], dp[2][4];
    for (int i = 0; i < 2; i++) {
      uint16_t t = (st.load[i] + 5) / 10;                 // tenths, rounded
      snprintf_P(ip[i], sizeof ip[i], PSTR("%u"), t / 10);
      snprintf_P(dp[i], sizeof dp[i], t < 100 ? PSTR(".%u") : PSTR(""), t % 10);   // decimal only while there is room
    }
    snprintf_P(b, sizeof b, PSTR("%s%s %s%s"), ip[0], dp[0], ip[1], dp[1]);
    uint16_t sig = sigOf(b, col);
    if (sig != spLoad.sig) {
      spLoad.sig = sig;
      int cx = G[0].cx, base = G[0].cy - 19;
      int wi[2], wd[2], w = 10;                           // 10 px gap between the two values
      for (int i = 0; i < 2; i++) {
        wi[i] = textW(&FreeSansBold12pt7b, ip[i]); wd[i] = textW(&FreeSans9pt7b, dp[i]);
        w += wi[i] + wd[i];
      }
      int x = cx - w / 2;
      bxBegin(cx - 46, G[0].cy - 36, 92, 18, BLACK);
      for (int i = 0; i < 2; i++) {
        bxText(&FreeSansBold12pt7b, col, x, base, ip[i]); x += wi[i];
        bxText(&FreeSans9pt7b, col, x, base, dp[i]); x += wd[i] + 10;
      }
      bxEnd(&spLoad);
    }
  }
  pump();
  {
    char v[12], wt[8];
    snprintf_P(v, sizeof v, PSTR("VRAM %d%%"), st.vram);
    snprintf_P(wt, sizeof wt, PSTR("%uW"), st.gpuW);
    uint16_t cv = levelColor(st.vram, 70, 90), cw = wp >= 98 ? RED : wp >= 85 ? ORANGE : POWER_OK;
    uint16_t sig = sigOf(v, cv) ^ sigOf(wt, cw);
    if (sig != spVram.sig) {
      spVram.sig = sig;
      int vw = textW(&FreeSans9pt7b, v), total = vw + 8 + textW(&FreeSans9pt7b, wt);
      int x0 = G[2].cx - total / 2 + 8;                       // nudged right: keep clear of the divider at x=318
      bxBegin(G[2].cx - 70, G[2].cy + 66, 160, 14, BLACK);    // stops above the "Network" title
      bxText(&FreeSans9pt7b, cv, x0, G[2].cy + 78, v);
      bxText(&FreeSans9pt7b, cw, x0 + vw + 8, G[2].cy + 78, wt);
      bxEnd(&spVram);
    }
  }
}

extern uint32_t shownD, shownU;   // 3 s averages of the network rates (see netTick)
// Network rates are bits per second in SI units, like fast.com and speed tests. Same thresholds as fmtBits() in net.go.
static void fmtRate(char *out, size_t n, uint32_t kbps) {
  if (kbps < 1000) snprintf_P(out, n, PSTR("%u Kbps"), (unsigned)kbps);
  else if (kbps < 99950UL) { uint32_t t = (kbps + 50) / 100; snprintf_P(out, n, PSTR("%u.%u Mbps"), (unsigned)(t / 10), (unsigned)(t % 10)); }
  else if (kbps < 999500UL) snprintf_P(out, n, PSTR("%u Mbps"), (unsigned)((kbps + 500) / 1000));
  else { uint32_t g = (kbps + 5000) / 10000; snprintf_P(out, n, PSTR("%u.%02u Gbps"), (unsigned)(g / 100), (unsigned)(g % 100)); }
}

static Span spUptime, spClock, spDocker, spClaude, spRate[2];

// header text on the PANEL strip: only the columns of the old and new text are repainted
static void hdrText(Span &sp, int x, int w, const GFXfont *f, uint16_t col, int tx, const char *s, uint8_t align = 0) {
  uint16_t sig = sigOf(s, col);
  if (sig == sp.sig) return;
  sp.sig = sig;
  bxBegin(x, 0, w, 22, PANEL);
  bxText(f, col, tx, 16, s, align);
  bxEnd(&sp);
}

static void drawHeader() {  // full redraw (on state change)
  tft.fillRect(0, 0, W, 22, PANEL);
  if (!online) {
    put(&FreeSansBold9pt7b, RED, 28, 16, F("No data - start tft-monitor"));
  }
  spanReset(spUptime); spanReset(spClock); spanReset(spDocker); spanReset(spClaude);
}

static void updateHeader() {
  if (!online) return;
  uint32_t u = st.uptime;
  char b[16]; snprintf_P(b, sizeof b, PSTR("up %lud %02luh"), u / 86400, (u % 86400) / 3600);
  hdrText(spUptime, 150, 140, &FreeSans9pt7b, GREY, 150, b);
  memcpy(b, st.clock, sizeof st.clock);              // a copy: frames are read while drawing
  hdrText(spClock, W - 80, 80, &FreeSansBold9pt7b, WHITE, W - 12, b, 2);
}

static void netRates() {
  char b[16];
  for (uint8_t i = 0; i < 2; i++) {
    fmtRate(b, sizeof b, i ? shownU : shownD);
    uint16_t sig = sigOf(b, WHITE);
    if (sig == spRate[i].sig) continue;
    spRate[i].sig = sig;
    bxBegin(346, 182 + i * 30, 130, 26, BLACK);
    bxText(&FreeSansBold12pt7b, WHITE, 348, 202 + i * 30, b);
    bxEnd(&spRate[i]);
  }
}

// ---- oscilloscope-style load history ----
// scope geometry: compact on the dashboard, full-width and taller on the CPU-hogs page
static int GX = 42, GY = 184, GSTEP = 3, GH = 66;
#define GW ((HIST - 1) * GSTEP)
static void graphGeom(bool wide) {
  if (wide) { GX = 26; GY = 173; GSTEP = 5; GH = 75; }
  else      { GX = 42; GY = 184; GSTEP = 3; GH = 66; }
}
#define SC_FRAME 0x29CA
#define SC_GRID  0x10E5
#define SC_AXIS  0x3A8E
// trace colours: head (bright), normal, fading 1, fading 2
static const uint16_t CPU_C[4] = {0xDFFF, CYAN, 0x2C96, 0x1A4C};
static const uint16_t GPU_C[4] = {0xFEFF, MAGENTA, 0x7AF7, 0x398C};
#define AGE1 25
#define AGE2 55

static uint8_t hC[HIST], hG[HIST];
static uint8_t gPos;
static uint16_t gTotal;   // samples drawn so far, saturating: only ever compared with ages below HIST
static inline void countUp(uint16_t &n) { if (n < 0xFFFF) n++; }

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

static void graphFrame(bool labels = true) {
  tft.drawRect(GX - 1, GY - 1, GW + 3, GH + 2, SC_FRAME);
  graphGrid(GX, GW + 1);
  if (!labels) return;
  tft.setTextSize(1); tft.setTextColor(SC_AXIS, BLACK);
  tft.setCursor(GX - 20, GY); tft.print(F("100"));
  tft.setCursor(GX - 14, GY + GH / 2 - 3); tft.print(F("50"));
  tft.setCursor(GX - 8, GY + GH - 8); tft.print(F("0"));
}

static inline int gy(uint8_t v) { return GY + GH - 3 - v * (GH - 4) / 100; }

static void drawSeg(const uint8_t *h, int j, uint16_t col) {
  int x1 = GX + j * GSTEP, x0 = x1 - GSTEP;
  int y0 = gy(h[j - 1]), y1 = gy(h[j]);
  tft.drawLine(x0, y0, x1, y1, col);
  tft.drawLine(x0, y0 + 1, x1, y1 + 1, col);
}

// network scope history (Net page), see netTick()
static uint32_t nRawD[HIST], nRawU[HIST];      // kbit/s
static uint8_t nD[HIST], nU[HIST];             // the same, scaled 0..100 for the current full-scale step
static uint8_t nPos, nScale;
static uint16_t nTotal;                        // saturating, like gTotal
static const uint32_t NSTEP[11] = {100, 300, 1000, 3000, 10000, 30000, 100000, 300000, 1000000, 3000000, 10000000};   // kbit/s at the top of the chart
static const uint16_t ND_C[4] = {0xCFFE, GREEN, 0x1CD0, 0x1289};     // download: teal, head to tail
static const uint16_t NU_C[4] = {0xFF16, ORANGE, 0xBB23, 0x5982};    // upload: orange

// Both scopes are two series over a ring of HIST samples, drawn as a beam: the newest segment bright, older ones
// fading at AGE1 / AGE2 samples. Load (net = false): GPU under CPU. Network (net = true): upload under download.
static void traceSeg(bool net, int j, uint8_t level) {   // level: head, normal, fading 1, fading 2
  if (net) { drawSeg(nU, j, NU_C[level]); drawSeg(nD, j, ND_C[level]); }
  else     { drawSeg(hG, j, GPU_C[level]); drawSeg(hC, j, CPU_C[level]); }
}

static void traceFade(bool net, uint8_t pos, uint16_t total, int age, uint8_t level) {   // the segment `age` samples back
  if ((int)total <= age) return;
  int j = ((int)pos - age + HIST) % HIST;
  if (j >= 1) traceSeg(net, j, level);
}

// the sample at pos is stored but not yet counted: erase the gap ahead of the beam, draw the head, fade older segments
static void traceHead(bool net, uint8_t pos, uint16_t total) {
  graphGrid(GX + pos * GSTEP + (pos ? 1 : 0), GSTEP * 3);
  if (pos >= 1 && total >= 1) traceSeg(net, pos, 0);
  traceFade(net, pos, total, 1, 1);
  traceFade(net, pos, total, AGE1, 2);
  traceFade(net, pos, total, AGE2, 3);
}

// repaint the whole trace from the ring buffer (coming back from another screen keeps the history)
static void traceRepaint(bool net, uint8_t pos, uint16_t total) {
  for (int j = 1; j < HIST; j++) {
    if (total < HIST && j >= pos) break;                            // not written yet
    if (total >= HIST && j >= pos && j < pos + 3) continue;         // the gap ahead of the beam
    int age = ((int)pos - 1 - j + 2 * HIST) % HIST;                 // 0 = newest sample
    traceSeg(net, j, age == 0 ? 0 : age < AGE1 ? 1 : age < AGE2 ? 2 : 3);
    pump();
  }
}

static void graphPush(uint8_t cpu, uint8_t gpu) {
  hC[gPos] = cpu; hG[gPos] = gpu;
  traceHead(false, gPos, gTotal);
  countUp(gTotal);   // a plain ++ wrapped after ~4.5 h of samples and stopped the trace fading for a while
  if (++gPos >= HIST) gPos = 0;
}

static void graphRepaint() { graphFrame(); traceRepaint(false, gPos, gTotal); }

// ---- network scope (Net page): download and upload rates, one sample per stats frame, autoscaled in steps ----
static uint8_t netScaled(uint32_t raw) { uint32_t v = raw * 100 / NSTEP[nScale]; return v > 100 ? 100 : v; }

static void netCaption() {
  tft.fillRect(26, 160, 440, 11, BLACK);
  tft.setTextSize(1);
  tft.setTextColor(GREY); tft.setCursor(26, 163); tft.print(F("Traffic"));
  tft.setTextColor(GREEN); tft.setCursor(80, 163); tft.print(F("down"));
  tft.setTextColor(ORANGE); tft.setCursor(112, 163); tft.print(F("up"));
  char b[16];                                   // "max 100 Kbps" .. "max 10 Gbps", right-aligned to the chart
  uint32_t v = NSTEP[nScale];
  if (v < 1000) snprintf_P(b, sizeof b, PSTR("max %lu Kbps"), v);
  else if (v < 1000000UL) snprintf_P(b, sizeof b, PSTR("max %lu Mbps"), v / 1000);
  else snprintf_P(b, sizeof b, PSTR("max %lu Gbps"), v / 1000000UL);
  tft.setTextColor(GREY); tft.setCursor(GX + GW - strlen(b) * 6, 163); tft.print(b);
}

static void netRepaint() { graphFrame(false); traceRepaint(true, nPos, nTotal); }

// the numbers next to the chart are a 3 s average with a dead band, so they only change when the traffic really does
static uint32_t rateD[12], rateU[12];
static uint8_t rateI;
uint32_t shownD, shownU;                        // what the text readouts show (kbit/s)
static void rateAvg() {
  rateD[rateI] = st.down; rateU[rateI] = st.up;
  if (++rateI >= 12) rateI = 0;
  uint32_t sd = 0, su = 0;
  for (uint8_t i = 0; i < 12; i++) { sd += rateD[i]; su += rateU[i]; }
  sd /= 12; su /= 12;
  if ((sd > shownD ? sd - shownD : shownD - sd) * 12 > shownD + 24) shownD = sd;    // moved by more than ~8 % (or a couple of kbit/s)
  if ((su > shownU ? su - shownU : shownU - su) * 12 > shownU + 24) shownU = su;
}

// called on every stats frame: keeps the real-time history even while another screen is up
static void netTick() {
  rateAvg();
  nRawD[nPos] = st.down; nRawU[nPos] = st.up;   // one sample per frame: the chart is live
  uint32_t mx = 1;                              // full scale = smallest step above the window's peak
  for (uint16_t i = 0; i < HIST; i++) { if (nRawD[i] > mx) mx = nRawD[i]; if (nRawU[i] > mx) mx = nRawU[i]; }
  uint8_t sc = 0;
  while (sc < 10 && mx > NSTEP[sc]) sc++;
  if (sc < nScale && mx * 10 > NSTEP[sc] * 7) sc = nScale;   // scale down only once the peak is well below the lower step
  bool rescaled = sc != nScale;
  nScale = sc;
  if (rescaled) for (uint8_t i = 0; i < HIST; i++) { nD[i] = netScaled(nRawD[i]); nU[i] = netScaled(nRawU[i]); }
  else { nD[nPos] = netScaled(nRawD[nPos]); nU[nPos] = netScaled(nRawU[nPos]); }
  bool vis = netVisible();
  if (vis && !rescaled) traceHead(true, nPos, nTotal);
  countUp(nTotal);
  if (++nPos >= HIST) nPos = 0;
  if (vis && rescaled) { netRepaint(); netCaption(); }
}

// ---- Claude session usage: two vertical bars (context fill, 5h limit) ----
struct Claude { int8_t ctx, s5h, wk, docker, busy; int16_t reset; } cl = {-1, -1, -1, -1, 0, -1}, clDrawn;
static bool clValid = false;
struct VBar { int8_t h; uint16_t col; } vb[2];
// Screen-edge bar (x0-8): top half = weekly usage fills downward, bottom half = 5h session fills upward, both toward the centre
#define EB_X    3                        // margin from the screen edge
#define EB_Y    28
#define EB_H    266
#define EB_LEN  119
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

static Span spCtx;

static void drawClaude() {
  if (clValid && !memcmp(&cl, &clDrawn, sizeof cl)) return;
  if (!clValid || cl.busy != clDrawn.busy) tft.drawRect(EB_X, EB_Y, 9, EB_H, cl.busy ? CYAN : GREY);
  vbar(0, cl.wk); vbar(1, cl.s5h);
  char b[24];
  if (cl.s5h >= 0) snprintf_P(b, sizeof b, PSTR("ctx %d%%  5h %d%%  %d:%02d   "), cl.ctx, cl.s5h, cl.reset / 60, cl.reset % 60);
  else snprintf_P(b, sizeof b, PSTR("ctx %d%%  5h --          "), cl.ctx);
  uint16_t col = levelColor(max((int)cl.ctx, (int)cl.s5h), 60, 80), sig = sigOf(b, col);
  if (sig != spCtx.sig) {
    spCtx.sig = sig;
    bxBegin(328, 242, W - 328, 8, BLACK);
    bxText(nullptr, col, 328, 242, b);
    bxEnd(&spCtx);
  }
  if (cl.docker < 0) snprintf_P(b, sizeof b, PSTR("docker --"));
  else snprintf_P(b, sizeof b, PSTR("docker %d"), cl.docker);
  hdrText(spDocker, 28, 118, &FreeSans9pt7b, CYAN, 28, b);   // never reaches the "up" text at x=150
  uint16_t cc = cl.busy ? YELLOW : GREY;
  if (sigOf("claude", cc) != spClaude.sig) {               // whole box: it also clears the working dot
    spClaude.sig = sigOf("claude", cc);
    bxBegin(300, 0, 90, 22, PANEL);
    bxText(&FreeSans9pt7b, cc, 300, 16, "claude");
    bxEnd();
    if (cl.busy) tft.fillCircle(366, 11, 4, YELLOW);          // working dot
  }
  clDrawn = cl; clValid = true;
}

static void drawStatic() {
  pageMode = pageScope = pageLogo = pageNet = false;
  tft.fillScreen(BLACK);
  for (int i = 0; i < 3; i++) gaugeFrame(G[i]);
  for (int i = 0; i < 4; i++) gaugeFrame(T[i]);
  panelTitle(28, 168, F("Load history"));
  tft.setTextColor(CYAN); tft.setCursor(108, 168); tft.print(F("CPU"));
  tft.setTextColor(MAGENTA); tft.setCursor(132, 168); tft.print(F("GPU"));
  panelTitle(328, 168, F("Network"));
  tft.fillTriangle(328, 187, 340, 187, 334, 198, GREEN);     // download
  tft.fillTriangle(328, 226, 340, 226, 334, 215, ORANGE);    // upload
  tft.drawFastVLine(318, 166, 86, DARK);
  tft.drawFastHLine(0, 258, W, DARK);
  graphGeom(false);
  graphRepaint();
  vbarFrame(); clValid = false;
  memset(cellHash, 0, sizeof cellHash); bIdx = bCnt = bSev = bBorder = 255; bandDirty = 0; lsReset();
  for (uint8_t i = 0; i < 3; i++) spanReset(spNote[i]);
  spanReset(spLoad); spanReset(spVram); spanReset(spRate[0]); spanReset(spRate[1]); spanReset(spCtx);
  menuBar();
}

// the Claude spark: twelve tapered rays of uneven length round a small hub, in terracotta
static void claudeLogo(int cx, int cy, int r) {
  static const uint8_t LEN[12] = {100, 74, 92, 66, 100, 80, 94, 70, 98, 76, 88, 68};   // % of r
  const uint16_t C = 0xDBAA;
  for (uint8_t i = 0; i < 12; i++) {
    float a = (i * 30 + 8) * DEG_TO_RAD;
    float ca = cos(a), sa = sin(a), L = r * LEN[i] / 100.0f, h = r * 0.13f, b0 = r * 0.16f;
    int tx = cx + ca * L, ty = cy + sa * L;
    int lx = cx + ca * b0 - sa * h, ly = cy + sa * b0 + ca * h;
    int rx = cx + ca * b0 + sa * h, ry = cy + sa * b0 - ca * h;
    tft.fillTriangle(lx, ly, rx, ry, tx, ty, C);
  }
  tft.fillCircle(cx, cy, r * 0.22f, C);
}

static void setScreen(uint8_t mode) {   // low bits: 0 dashboard, 1 full page, 2 full page cleared first; 0x10 = load scope on the page
  uint8_t m = mode & 3;
  bool full = m != 0, force = mode & 0x40;   // 0x40: repaint the current screen even if it is already up
  if (full && pageMode && m != 2 && !force) return;
  if (!full && !pageMode && !force) return;
  if (full) {
    pageMode = true;
    pageScope = mode & 0x10;
    pageLogo = mode & 0x20;
    pageNet = mode & 0x08;
    pageNote = mode & 0x04;
    tft.fillScreen(BLACK);
    memset(lineHash, 0, sizeof lineHash); lsReset();
    if (pageScope) {
      graphGeom(true);
      panelTitle(26, 163, F("Load history"));
      tft.setTextColor(CYAN); tft.setCursor(106, 163); tft.print(F("CPU"));
      tft.setTextColor(MAGENTA); tft.setCursor(130, 163); tft.print(F("GPU"));
      graphRepaint();
    }
    if (pageNet) { graphGeom(true); netCaption(); netRepaint(); }
    if (pageNote) noteStrip();
    menuBar();
  } else {
    drawStatic(); drawHeader();        // repaints everything; the stats loop refills the gauges
  }
}

// A stats frame is painted in two parts, with the queued band / page frames in between:
// first the three big percentages (number, then arc), last the slow or minor things (rings, small text, scope).
static void redrawFirst() {
  if (!online || pageMode) return;
  uint8_t v[3] = {st.cpu, st.ram, st.gpu};
  for (uint8_t i = 0; i < 3; i++) gaugeLabel(G[i], v[i]);
  pump();
  for (uint8_t i = 0; i < 3; i++) gaugeUpdate(G[i], v[i], v[i]);
}

static void redrawRest() {
  if (!online) { drawHeader(); return; }
  if (pageMode) {                     // a full-screen page owns the display
    if (pageScope) graphPush(st.cpu, st.gpu);
    return;
  }
  gaugeNotes();
  netRates(); pump();
  updateHeader(); pump();
  graphPush(st.cpu, st.gpu);
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
static void ctext(int y, uint8_t sz, uint16_t col, const __FlashStringHelper *s) {
  tft.setTextSize(sz); tft.setTextColor(col, BLACK);
  tft.setCursor(240 - (int)strlen_P((PGM_P)s) * 3 * sz, y); tft.print(s);
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
  if (wh >= 1000) snprintf_P(o, n, PSTR("%lu.%02lu kWh"), wh / 1000, (wh % 1000) / 10);
  else snprintf_P(o, n, PSTR("%lu Wh"), wh);
}
static void fmtMB(char *o, size_t n, uint32_t mb) {
  if (mb >= 100000) snprintf_P(o, n, PSTR("%lu GB"), mb / 1000);                        // decimal, three significant digits at most
  else if (mb >= 1000) snprintf_P(o, n, PSTR("%lu.%lu GB"), mb / 1000, (mb % 1000) / 100);
  else snprintf_P(o, n, PSTR("%lu MB"), mb);
}
static void fmtHMS(char *o, size_t n, uint32_t s) { snprintf_P(o, n, PSTR("%02lu:%02lu:%02lu"), (s / 3600) % 100, (s / 60) % 60, s % 60); }
static const __FlashStringHelper *ssTitle() {
  switch (byeReason) { case 1: return F("POWERED OFF"); case 2: return F("REBOOTING"); case 3: return F("MONITOR STOPPED"); }
  return F("LINK LOST");
}
static uint16_t ssTitleCol() { return byeReason == 1 ? ORANGE : byeReason == 2 ? YELLOW : byeReason == 3 ? GREY : CYAN; }
static uint16_t grey565(uint8_t g) { return ((g >> 3) << 11) | ((g >> 2) << 5) | (g >> 3); }
static uint16_t peakColor(uint8_t v, bool temp) { return temp ? tempColor(v, 60) : levelColor(v); }
static void ssClock(char *o, size_t n, uint32_t elapsedS) {   // wall clock estimate: time of the signal + elapsed
  uint32_t m = (offH * 60UL + offM + elapsedS / 60) % 1440;
  snprintf_P(o, n, PSTR("%02lu:%02lu"), m / 60, m % 60);
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
      ctext(100, 2, GREY, byeReason ? F("OFF FOR") : F("NO DATA FOR"));
      break;
    case 1: {
      ctext(10, 3, CYAN, F("LAST SESSION"));
      static const char L[] PROGMEM = "UPTIME\0ENERGY ~\0DOWNLOAD\0UPLOAD\0FULLEST DISK";
      tft.setTextSize(2); tft.setTextColor(GREY, BLACK);
      for (uint8_t i = 0; i < 5; i++) { tft.setCursor(30, 66 + i * 48); tft.print(FS(nthP(L, i))); }
      break; }
    case 2: {
      ctext(10, 3, CYAN, F("PEAKS"));
      static const char L[] PROGMEM = "CPU\0GPU\0CPU T\0GPU T";
      tft.setTextSize(2); tft.setTextColor(GREY, BLACK);
      for (uint8_t i = 0; i < 4; i++) { PGM_P l = nthP(L, i); tft.setCursor(50 + i * 110 + (60 - (int)strlen_P(l) * 12) / 2, 280); tft.print(FS(l)); }
      tft.drawFastHLine(30, 270, 420, DARK);
      break; }
    case 3: {
      ctext(10, 3, CYAN, F("ENERGY"));
      uint32_t ch = sum.wh10 / 10 / 12;
      if (ch) snprintf_P(b, sizeof b, PSTR("~ %lu phone charges"), ch); else snprintf_P(b, sizeof b, PSTR("less than 1 phone charge"));
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
        snprintf_P(b, sizeof b, PSTR("since %s  now %s"), c, n); ctext(204, 2, GREY, b);
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
        if (i == 0) { col = CYAN; if (v >= 1440) snprintf_P(b, sizeof b, PSTR("%lud %02luh %02lum"), v / 1440, (v / 60) % 24, v % 60); else snprintf_P(b, sizeof b, PSTR("%luh %02lum"), v / 60, v % 60); }
        else if (i == 1) { col = YELLOW; fmtWh(b, sizeof b, v); }
        else if (i == 2) { col = GREEN; fmtMB(b, sizeof b, v); }
        else if (i == 3) { col = ORANGE; fmtMB(b, sizeof b, v); }
        else { col = peakColor(sum.disk, false); snprintf_P(b, sizeof b, PSTR("%ld%%"), v); }
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
          snprintf_P(b, sizeof b, i < 2 ? PSTR("%u%%") : PSTR("%uC"), val[i]);
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
        ssClock(c, sizeof c, el); snprintf_P(b, sizeof b, PSTR("now %s"), c); ctext(268, 2, GREY, b);
      }
      break; }
  }
}

// ---- binary protocol: 0xAA 0x55 | type | len | payload | crc8(type,len,payload) ----
enum : uint8_t { F_STATS = 1, F_CLAUDE = 2, F_BAND = 4, F_CELL = 5, F_SUM = 6, F_BYE = 7, F_SCREEN = 8, F_LINE = 9, F_PAGE = 10, F_CMD = 11, F_COLOR = 12 };
static bool menuDirty = false;    // the highlighted menu entry (or its pin) changed
static bool menuPinned = false;   // the PC says the current page is pinned
static uint8_t menuSlot = 255;    // which menu entry the PC says is showing (255 = none)
static bool doCal = false;        // the PC asked for a touch calibration

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
    snprintf_P(st.clock, sizeof st.clock, PSTR("%02d:%02d"), min((int)p[18], 23), min((int)p[19], 59));
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
  } else if (type == F_PAGE && (len == 1 || len == 2)) {
    bool pin = len == 2 && p[1];
    if (menuSlot != p[0] || menuPinned != pin) { menuSlot = p[0]; menuPinned = pin; menuDirty = true; }
  } else if (type == F_CMD && len == 1) {
    if (p[0] == 1) doCal = true;
  } else if (type == F_BAND || type == F_CELL || type == F_LINE || (type == F_SCREEN && len == 1) || (type == F_COLOR && len == 3)) {
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
template <typename T>   // detail: a RAM buffer or an F() string
static void bootLine(const __FlashStringHelper *name, T detail, bool ok = true) {
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
  snprintf_P(b, sizeof b, PSTR("ILI%X %dx%d"), id, W, H); bootLine(F("DISPLAY"), b);
  bootLine(F("SERIAL"), F("115200 8N1")); 
  snprintf_P(b, sizeof b, PSTR("v%d CRC-8 frames"), PROTO); bootLine(F("PROTOCOL"), b);
  bootLine(F("GAUGES"), F("CPU GPU RAM"));
  bootLine(F("RINGS"), F("TEMP SWAP"));
  bootLine(F("SCOPE"), F("load history"));
  bootLine(F("INFO BAND"), F("alerts+pages"));
  snprintf_P(b, sizeof b, PSTR("%d B free"), freeRam()); bootLine(F("MEMORY"), b);
  bootLine(F("HOST LINK"), F("waiting..."), false);
}

static bool booted;
// ---- touch: 4-wire resistive panel on the shield pins (XP=8, XM=A2, YP=A3, YM=9) ----
// Raw readings are mapped to screen pixels with a 3-point calibration kept in EEPROM. Gestures are
// reported to the PC as text lines: "T t x y" tap, "T l x y" long press, "T s L|R x y" swipe; the PC decides what they do.
#define TS_XP 8
#define TS_XM A2
#define TS_YP A3
#define TS_YM 9
static TouchScreen ts(TS_XP, TS_YP, TS_XM, TS_YM, 300);
struct TCal { uint16_t magic; uint8_t swap; int16_t x0, x1, y0, y1; } tcal;
#define TCAL_MAGIC 0x7C42
static bool touchOk = false;

static bool rawTouch(int16_t &rx, int16_t &ry) {
  TSPoint p = ts.getPoint();
  pinMode(TS_XM, OUTPUT); pinMode(TS_YP, OUTPUT); pinMode(TS_XP, OUTPUT); pinMode(TS_YM, OUTPUT);   // hand the shared pins back to the LCD
  digitalWrite(TS_YP, HIGH);                                                                       // A3 is the LCD chip select: idle
  if (p.z < 80 || p.z > 1000) return false;
  rx = p.x; ry = p.y;
  return true;
}

static int16_t clampI(long v, int lo, int hi) { return v < lo ? lo : v > hi ? hi : v; }

static bool readTouch(int16_t &x, int16_t &y) {
  int16_t a, b;
  if (!touchOk || !rawTouch(a, b)) return false;
  int16_t hx = tcal.swap ? b : a, vy = tcal.swap ? a : b;
  x = clampI((long)(hx - tcal.x0) * (W - 1) / (tcal.x1 - tcal.x0), 0, W - 1);
  y = clampI((long)(vy - tcal.y0) * (H - 1) / (tcal.y1 - tcal.y0), 0, H - 1);
  return true;
}

static void crossAt(int x, int y, uint16_t c) {
  tft.drawFastHLine(x - 14, y, 29, c); tft.drawFastVLine(x, y - 14, 29, c); tft.drawCircle(x, y, 8, c);
}

static void calPoint(int sx, int sy, int n, int16_t &ax, int16_t &ay) {
  tft.fillScreen(BLACK);
  tft.setFont(); tft.setTextSize(2); tft.setTextColor(WHITE);
  tft.setCursor(70, 130); tft.print(F("Touch the cross"));
  tft.setCursor(70, 156); tft.print(F("to calibrate  ")); tft.print(n); tft.print(F("/3"));
  crossAt(sx, sy, CYAN);
  int16_t a, b;
  while (rawTouch(a, b)) delay(10);                       // wait for the previous finger to leave
  long sa = 0, sb = 0; uint8_t k = 0;
  while (k < 12) {                                         // average a steady touch
    if (rawTouch(a, b)) { sa += a; sb += b; k++; } else { sa = sb = 0; k = 0; }
    delay(10);
  }
  ax = sa / 12; ay = sb / 12;
  crossAt(sx, sy, GREEN);
  while (rawTouch(a, b)) delay(10);
}

static void calibrate() {
  const int M = 24;                                        // target distance from the screen edge
  int16_t a[3], b[3];
  calPoint(M, M, 1, a[0], b[0]);                           // top-left
  calPoint(W - 1 - M, M, 2, a[1], b[1]);                   // top-right
  calPoint(M, H - 1 - M, 3, a[2], b[2]);                   // bottom-left
  tcal.swap = abs(b[1] - b[0]) > abs(a[1] - a[0]);         // which raw axis runs along the screen's x
  int16_t h0 = tcal.swap ? b[0] : a[0], h1 = tcal.swap ? b[1] : a[1];
  int16_t v0 = tcal.swap ? a[0] : b[0], v2 = tcal.swap ? a[2] : b[2];
  long span = W - 1 - 2 * M, hspan = H - 1 - 2 * M;
  tcal.x0 = h0 - (long)(h1 - h0) * M / span;
  tcal.x1 = h0 + (long)(h1 - h0) * (W - 1 - M) / span;
  tcal.y0 = v0 - (long)(v2 - v0) * M / hspan;
  tcal.y1 = v0 + (long)(v2 - v0) * (H - 1 - M) / hspan;
  tcal.magic = TCAL_MAGIC;
  touchOk = tcal.x1 != tcal.x0 && tcal.y1 != tcal.y0;
  if (touchOk) EEPROM.put(0, tcal);
  tft.fillScreen(BLACK);
  tft.setTextColor(touchOk ? GREEN : RED); tft.setTextSize(2);
  tft.setCursor(70, 140); tft.print(touchOk ? F("Calibration saved") : F("Calibration failed"));
  delay(900);
}

static void touchInit() {
  EEPROM.get(0, tcal);
  touchOk = tcal.magic == TCAL_MAGIC && tcal.x1 != tcal.x0 && tcal.y1 != tcal.y0;
}

// ---- bottom menu: a thin strip on every screen (y 300..319); a tap sends the choice to the PC ----
#define MENU_N 7
static const char MENU[] PROGMEM = "Home\0CPU\0RAM\0Net\0Claude\0System\0Setup";   // MENU_N entries
static void menuBar() {
  menuDirty = false;
  int w = W / MENU_N;
  tft.fillRect(0, MENU_Y, W, H - MENU_Y, PANEL);
  tft.drawFastHLine(0, MENU_Y, W, DARK);
  for (uint8_t i = 0; i < MENU_N; i++) {
    int x0 = i * w;
    bool on = i == menuSlot;
    if (on) { tft.fillRect(x0 + 3, MENU_Y + 3, w - 6, H - MENU_Y - 5, DARK); tft.fillRect(x0 + 10, H - 3, w - 20, 2, GREEN); }
    char label[8];
    strcpy_P(label, nthP(MENU, i));
    put(&FreeSans9pt7b, on ? WHITE : GREY, x0 + w / 2 + (on && menuPinned ? 5 : 0), MENU_Y + 15, label, 1);
    if (on && menuPinned) {                                // a pin: round head, needle down-left
      tft.fillCircle(x0 + 9, MENU_Y + 8, 3, ORANGE);
      tft.drawLine(x0 + 9, MENU_Y + 10, x0 + 6, MENU_Y + 16, ORANGE);
    }
  }
}

// repaint whatever screen is up (after a calibration covered it); the PC resends the contents
static void repaintCurrent() {
  if (pageMode) setScreen(0x42 | (pageScope ? 0x10 : 0) | (pageLogo ? 0x20 : 0) | (pageNet ? 0x08 : 0) | (pageNote ? 0x04 : 0)); else setScreen(0x40);
  qn = 0;
}

static void menuClose() {
  repaintCurrent();
  Serial.println(F("T r"));
}

// gesture state machine, polled every ~25 ms
static void touchPoll() {
  static uint32_t lastPoll, t0;
  static bool down, long_sent;
  static int16_t x0, y0, x, y;
  static uint8_t miss;
  if (!touchOk || millis() - lastPoll < 25) return;
  lastPoll = millis();
  int16_t nx, ny;
  if (readTouch(nx, ny)) {
    miss = 0;
    if (!down) { down = true; long_sent = false; t0 = millis(); x0 = x = nx; y0 = y = ny; return; }
    x = nx; y = ny;
    uint32_t held = millis() - t0;
    if (abs(x - x0) < 30 && abs(y - y0) < 30) {
      if (held >= 700 && !long_sent && y0 < MENU_Y) { long_sent = true; Serial.print(F("T l ")); Serial.print(x); Serial.print(' '); Serial.println(y); }
      if (held >= 6000) { down = false; calibrate(); menuClose(); }                 // hold 6 s: recalibrate
    }
    return;
  }
  if (!down || ++miss < 3) return;                         // released (3 empty reads in a row)
  down = false;
  uint32_t held = millis() - t0;
  int dx = x - x0, dy = y - y0;
  if (long_sent) return;
  bool swipe = held < 900 && abs(dx) >= 90 && abs(dx) > 2 * abs(dy);
  bool tap = abs(dx) < 30 && abs(dy) < 30 && held >= 40;
  if (y0 >= MENU_Y) {                                      // the menu strip: only taps count
    if (tap) { Serial.print(F("T m ")); Serial.println(min(x0 / (W / MENU_N), MENU_N - 1)); }
    return;
  }
  if (swipe) {
    Serial.print(F("T s ")); Serial.print(dx < 0 ? 'L' : 'R'); Serial.print(' '); Serial.print(x0); Serial.print(' '); Serial.println(y0);
  } else if (tap) {
    Serial.print(F("T t ")); Serial.print(x0); Serial.print(' '); Serial.println(y0);
  }
}

void setup() {
  Serial.begin(115200);
  hello();
  uint16_t id = tft.readID();
  Serial.print(F("TFT ID: 0x")); Serial.println(id, HEX);
  if (id == 0xD3D3) id = 0x9486;
  tft.begin(id);
  tft.setRotation(3);   // landscape, flipped 180 (use 1 for the original orientation)
  tft.fast = id == 0x9486;   // direct bus writes (see FastTFT); anything else goes through the library
#ifdef FAST_LCD
  winX0 = -1; winY0 = -1;    // setRotation reset the window behind our back
#endif
  W = tft.width(); H = tft.height();
  touchInit();
  if (!touchOk) calibrate();    // first run: three taps, then it is remembered in EEPROM
  bootScreen(id);
}

static void pump() { while (Serial.available()) feed(Serial.read()); }

void loop() {
  pump();   // drain first, then draw only the newest data once (drawing keeps draining between steps)
  touchPoll();
  if (gotS) netTick();
  if (doCal) { doCal = false; calibrate(); menuClose(); }
  if (gotS) {
    gotS = false;
    if (!online) {
      online = true; saver = false;
      if (!booted) {   // finish the boot log, then swap to the dashboard
        booted = true;
        bootY -= 22; tft.fillRect(0, bootY, 480, 22, BLACK);
        bootLine(F("HOST LINK"), F("connected"));
        delay(350);
      }
      drawStatic(); drawHeader(); qn = 0;
    }
    lastRx = millis();
    redrawFirst();
    if (online) drawPanelItems(40);  // alerts / band changes before the minor readouts
    redrawRest();
    if (!clValid && !pageMode) drawClaude();
  }
  if (gotC) { gotC = false; if (online && !pageMode) drawClaude(); }
  if (byeGot) {
    byeGot = false;
    if (sumValid && !saver) startSaver(byeReason, offH, offM, millis());
  }
  if (saver) ssTick();
  if (online) { drawPanelItems(40); bandFlush(); }
  if (menuDirty && online && !saver) menuBar();
  static uint32_t lastStat;
  if (millis() - lastStat > 5000) {
    lastStat = millis();
    Serial.print(F("ok=")); Serial.print(framesOk); Serial.print(F(" bad=")); Serial.println(framesBad);
    hello();
  }
  if (online && millis() - lastRx > 5000) {
    online = false;
    if (sumValid) {
      uint8_t h = (st.clock[0] - '0') * 10 + st.clock[1] - '0', m = (st.clock[3] - '0') * 10 + st.clock[4] - '0';
      startSaver(0, min((int)h, 23), min((int)m, 59), lastRx);
    } else {
      drawStatic();
      drawHeader(); qn = 0;
    }
  }
}
