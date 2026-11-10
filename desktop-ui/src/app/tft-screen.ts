import { Component, computed, inject, input } from '@angular/core';
import { Api, Cell, State } from './api';

const PAL = ['#eaf0f8', '#8a9bb4', '#27d6b4', '#ffb020', '#ff8a3d', '#ff5560', '#4fe6ff', '#4aa8ff', '#b48cff'];
const DARK = '#1c2636';
const SEGS = 30;

interface Seg { d: string; on: boolean }

function arc(cx: number, cy: number, ro: number, ri: number, i: number): string {
  const a0 = ((135 + i * 9 + 0.8) * Math.PI) / 180;
  const a1 = ((135 + (i + 1) * 9 - 0.8) * Math.PI) / 180;
  const p = (r: number, a: number) => `${(cx + r * Math.cos(a)).toFixed(1)},${(cy + r * Math.sin(a)).toFixed(1)}`;
  return `M${p(ro, a0)} A${ro},${ro} 0 0 1 ${p(ro, a1)} L${p(ri, a1)} A${ri},${ri} 0 0 0 ${p(ri, a0)} Z`;
}

function segs(cx: number, cy: number, ro: number, ri: number, v: number): Seg[] {
  const lit = Math.floor((Math.min(Math.max(v, 0), 100) * SEGS) / 100);
  return Array.from({ length: SEGS }, (_, i) => ({ d: arc(cx, cy, ro, ri, i), on: i < lit }));
}

const level = (v: number, warn: number, crit: number, ok = PAL[2]) => (v >= crit ? PAL[5] : v >= warn ? PAL[3] : ok);
const tempPct = (t: number) => (t <= 20 ? 0 : t >= 100 ? 100 : ((t - 20) * 100) / 80);

/** A 480x320 replica of what the Arduino draws, fed from the daemon's state. */
@Component({
  selector: 'tft-screen',
  template: `
    @if (s(); as st) {
      <svg viewBox="0 0 480 320" class="screen">
        <rect width="480" height="320" fill="#000" />
        @if (st.panel.full) {
          @for (r of rows(); track r.row) {
            <g [attr.transform]="'translate(0,' + (4 + r.row * 31) + ')'">
              <text x="14" [attr.y]="r.big ? 24 : 20" [attr.fill]="r.labelCol" [attr.font-size]="r.big ? 24 : 15">{{ r.label }}</text>
              @if (r.big) { <rect x="14" y="27" width="452" height="1" fill="#1c2636" /> }
              @if (r.bar) {
                <rect x="170" y="3" width="220" height="20" fill="none" stroke="#9aa3ad" />
                <rect x="171" y="4" [attr.width]="r.fill" height="18" [attr.fill]="r.col" />
                <text x="398" y="20" [attr.fill]="r.col" font-size="15">{{ r.val }}</text>
                @if (r.sub) {
                  <text x="14" y="32" fill="#9aa3ad" font-size="8">SWAP</text>
                  <rect x="170" y="26" width="220" height="5" fill="none" stroke="#9aa3ad" />
                  <rect x="171" y="27" [attr.width]="2.18 * r.swap" height="3" [attr.fill]="r.swapCol" />
                  <text x="398" y="32" [attr.fill]="r.swapCol" font-size="8">{{ r.swap }}%</text>
                }
              } @else if (r.val) {
                <text x="466" [attr.y]="r.big ? 24 : 20" text-anchor="end" [attr.fill]="r.col" [attr.font-size]="r.big ? 24 : 15">{{ r.val }}</text>
              }
            </g>
          }
          @if (st.panel.page === 'net') {
            <text x="26" y="170" fill="#8a9bb4" font-size="9">Traffic</text>
            <text x="80" y="170" fill="#27d6b4" font-size="9">down</text><text x="112" y="170" fill="#ff8a3d" font-size="9">up</text>
            <rect x="25" y="172" width="432" height="77" fill="none" stroke="#29384f" />
            <polyline [attr.points]="netTrace('up')" fill="none" stroke="#ff8a3d" stroke-width="2" />
            <polyline [attr.points]="netTrace('down')" fill="none" stroke="#27d6b4" stroke-width="2" />
          }
          @if (st.panel.page === 'rollcpu') {
            <text x="26" y="170" fill="#9aa3ad" font-size="9">LOAD HISTORY</text>
            <text x="98" y="170" fill="#4fe6ff" font-size="9">CPU</text><text x="122" y="170" fill="#b48cff" font-size="9">GPU</text>
            <rect x="25" y="172" width="432" height="96" fill="none" stroke="#29384f" />
            <line x1="26" y1="220" x2="456" y2="220" stroke="#29384f" />
            <polyline [attr.points]="trace('gpu')" fill="none" stroke="#b48cff" stroke-width="2" />
            <polyline [attr.points]="trace('cpu')" fill="none" stroke="#4fe6ff" stroke-width="2" />
          }
        } @else {
        <rect width="480" height="22" fill="#14181f" />
        <text x="28" y="16" fill="#9aa3ad" font-size="13">UP {{ up() }}</text>
        <text x="452" y="16" fill="#9aa3ad" font-size="13" text-anchor="end">{{ st.docker['up'] }}/{{ st.docker['total'] }} docker</text>

        @for (g of gauges(); track g.label) {
          @for (sg of g.outer; track $index) { <path [attr.d]="sg.d" [attr.fill]="sg.on ? g.col : '#1c2636'" /> }
          @for (sg of g.inner; track $index) { <path [attr.d]="sg.d" [attr.fill]="sg.on ? g.icol : '#1c2636'" /> }
          @if (g.label === 'CPU') {
            <text [attr.x]="g.cx" [attr.y]="g.cy - 18" text-anchor="middle" [attr.fill]="g.loadCol" font-size="15">{{ load() }}</text>
          }
          <text [attr.x]="g.cx" [attr.y]="g.cy + 10" text-anchor="middle" [attr.fill]="g.col" font-size="24" font-weight="700">{{ g.val }}%</text>
          <text [attr.x]="g.cx" [attr.y]="g.cy + 28" text-anchor="middle" [attr.fill]="g.icol" font-size="15">{{ g.sub }}</text>
          <text [attr.x]="g.cx" [attr.y]="g.cy + 62" text-anchor="middle" fill="#9aa3ad" font-size="15">{{ g.label }}</text>
        }

        <rect x="27" y="260" width="451" height="60" fill="none" [attr.stroke]="border()" />
        @for (c of cells(); track c.slot) {
          <g [attr.transform]="'translate(' + c.x + ',' + c.y + ')'">
            <text x="0" y="19" [attr.fill]="c.labelCol" [attr.font-size]="c.big ? 24 : 15">{{ c.label }}</text>
            @if (c.bar) {
              <rect [attr.x]="c.bx" y="4" [attr.width]="c.bw" height="19" fill="none" stroke="#9aa3ad" />
              <rect [attr.x]="c.bx + 1" y="5" [attr.width]="c.fill" height="17" [attr.fill]="c.col" />
              <text [attr.x]="c.bx + c.bw + 5" y="19" [attr.fill]="c.col" font-size="15">{{ c.val }}</text>
            } @else if (c.val) {
              <text [attr.x]="c.w" y="19" text-anchor="end" [attr.fill]="c.col" font-size="15">{{ c.val }}</text>
            }
          </g>
        }
        }
      </svg>
    } @else {
      <div class="screen empty">waiting for the daemon…</div>
    }
  `,
  styles: [`.screen { width: 100%; max-width: 720px; border-radius: 10px; border: 6px solid #2a2f38; background: #000; font-family: system-ui, sans-serif; display:block }
    .empty { aspect-ratio: 3/2; display:grid; place-items:center; color:#9aa3ad }`],
})
export class TftScreen {
  s = input.required<State | null>();
  private api = inject(Api);

  /** download / upload trace, scaled to the window's peak like the board does (steps of 4x from 16 KB/s) */
  netTrace(k: 'down' | 'up') {
    const h = this.api.history();
    const peak = Math.max(1, ...h.map((x) => Math.max(x.down, x.up)));
    let step = 16;
    while (step < peak && step < 262144) step *= 4;
    return h.map((x, i) => `${26 + i * 5},${244 - (x[k] * 70) / step}`).join(' ');
  }

  trace(k: 'cpu' | 'gpu') {
    return this.api.history().map((h, i) => `${26 + i * 5},${264 - (h[k] * 88) / 100}`).join(' ');
  }

  up = computed(() => {
    const u = this.s()?.gauges.uptime ?? 0;
    return `${Math.floor(u / 86400)}d ${String(Math.floor((u % 86400) / 3600)).padStart(2, '0')}h`;
  });

  load = computed(() => {
    const l = this.s()?.gauges.load ?? [0, 0, 0];
    return `${l[0].toFixed(1)}  ${l[1].toFixed(1)}`;
  });

  border = computed(() => {
    const a = this.s()?.alerts ?? [];
    const m = Math.max(0, ...a.map((x) => x.sev));
    return m >= 3 ? PAL[5] : m === 2 ? PAL[4] : m === 1 ? PAL[6] : '#000';
  });

  gauges = computed(() => {
    const st = this.s();
    if (!st) return [];
    const g = st.gauges;
    const per = (g.load[0] * 100) / Math.max(g.ncpu, 1);
    return [
      { label: 'CPU', cx: 90, cy: 88, val: g.cpu, col: level(g.cpu, 60, 85), outer: segs(90, 88, 62, 55, g.cpu),
        inner: segs(90, 88, 53, 47, tempPct(g.cpuT)), icol: g.cpuT >= 80 ? PAL[5] : g.cpuT >= 60 ? PAL[4] : PAL[7],
        sub: `${g.cpuT}C`, loadCol: per >= 100 ? PAL[5] : per >= 60 ? PAL[4] : PAL[1] },
      { label: 'RAM', cx: 240, cy: 88, val: g.ram, col: level(g.ram, 60, 85), outer: segs(240, 88, 62, 55, g.ram),
        inner: segs(240, 88, 53, 47, g.swap), icol: g.swap >= 60 ? PAL[5] : g.swap >= 25 ? PAL[4] : PAL[2],
        sub: `S${g.swap}%`, loadCol: PAL[1] },
      { label: 'GPU', cx: 390, cy: 88, val: g.gpu, col: level(g.gpu, 60, 85), outer: segs(390, 88, 62, 55, g.gpu),
        inner: segs(390, 88, 53, 47, tempPct(g.gpuT)), icol: g.gpuT >= 80 ? PAL[5] : g.gpuT >= 65 ? PAL[4] : PAL[7],
        sub: `${g.gpuT}C`, loadCol: PAL[1] },
    ];
  });

  rows = computed(() => {
    const st = this.s();
    if (!st) return [];
    return st.panel.cells.map((c: Cell) => {
      const bar = !!(c.style & 0x10), big = !!(c.style & 0x40), bright = !!(c.style & 0x80);
      const col = PAL[Math.min(c.style & 15, 8)];
      let [label, val] = c.text.split('\t');
      const sub = bar && !!(c.style & 0x20);
      const swap = sub ? Math.min(100, parseInt(label.slice(0, 3), 10) || 0) : 0;
      if (sub) label = label.slice(3);
      return { row: c.slot, label, val: val ?? '', bar, big, sub, swap, swapCol: swap >= 60 ? PAL[5] : swap >= 25 ? PAL[4] : '#b4a0f0', col, labelCol: bright || big ? col : bar ? '#fff' : PAL[1], fill: (218 * Math.min(c.pct, 100)) / 100 };
    });
  });

  cells = computed(() => {
    const st = this.s();
    if (!st) return [];
    return st.panel.cells.map((c: Cell) => {
      const wide = !!(c.style & 0x20), bar = !!(c.style & 0x10), big = !!(c.style & 0x40), bright = !!(c.style & 0x80);
      const col = PAL[Math.min(c.style & 15, 8)];
      const [label, val] = c.text.split('\t');
      const w = wide ? 446 : 222;
      const bw = wide ? 230 : 68;
      return {
        slot: c.slot, x: 28 + (c.slot & 1) * 224, y: 262 + (c.slot >> 1) * 28, w, label, val: val ?? '',
        big, bar, col, labelCol: bright || big ? col : bar ? '#fff' : PAL[1],
        bx: wide ? 96 : 76, bw, fill: ((bw - 2) * Math.min(c.pct, 100)) / 100,
      };
    });
  });
}
