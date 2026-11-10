import { Component, computed, inject } from '@angular/core';
import { Api } from './api';
import { TftScreen } from './tft-screen';

@Component({
  selector: 'live-view',
  imports: [TftScreen],
  template: `
    <div class="grid">
      <section>
        <h2>TFT mirror</h2>
        <tft-screen [s]="api.state()" />
        @if (st(); as s) {
          <p class="muted">
            page <b>{{ s.panel.page }}</b> ({{ s.panel.sub + 1 }}/{{ s.panel.subs }})
            @if (s.panel.pin) { · pinned to <b>{{ s.panel.pin }}</b> }
            · {{ s.connected ? 'board connected' : 'board disconnected' }}
          </p>
        }
      </section>
      @if (st(); as s) {
        <section>
          <h2>Machine</h2>
          <div class="kv">
            <span>Load 1/5/15</span><b>{{ s.gauges.load[0].toFixed(2) }} / {{ s.gauges.load[1].toFixed(2) }} / {{ s.gauges.load[2].toFixed(2) }}</b>
            <span>Per core</span><b>{{ perCore() }}% of {{ s.gauges.ncpu }} threads</b>
            <span>Net</span><b>↓ {{ bits(s.gauges.down) }} · ↑ {{ bits(s.gauges.up) }}</b>
            <span>GPU power</span><b>{{ s.gauges.gpuW }} / {{ s.gauges.gpuLimit }} W</b>
            <span>NVMe</span><b>{{ s.gauges.nvmeT }} °C</b>
            <span>Ping</span><b>{{ s.gauges.latency < 0 ? 'FAIL' : s.gauges.latency + ' ms' }}</b>
            <span>Pressure</span><b>cpu {{ s.psi[0].toFixed(0) }}% · mem {{ s.psi[1].toFixed(0) }}% · io {{ s.psi[2].toFixed(0) }}%</b>
          </div>
          <h2>Top processes</h2>
          <div class="two">
            <ul>
              @for (p of s.topCpu; track p.name) { <li><span>{{ p.name }}</span><b>{{ p.v.toFixed(0) }}%</b></li> }
            </ul>
            <ul>
              @for (p of s.topMem; track p.name) { <li><span>{{ p.name }}</span><b>{{ gb(p.v) }}</b></li> }
            </ul>
          </div>
          <h2>Disks</h2>
          <ul>
            @for (d of s.disks; track d.name) {
              <li><span>{{ d.name }}</span><div class="bar"><i [style.width.%]="d.pct" [class.hot]="d.pct >= 90"></i></div><b>{{ d.pct }}%</b></li>
            }
          </ul>
        </section>
      }
    </div>
  `,
})
export class LiveView {
  api = inject(Api);
  st = this.api.state;
  perCore = computed(() => {
    const g = this.st()?.gauges;
    return g ? Math.round((g.load[0] * 100) / Math.max(g.ncpu, 1)) : 0;
  });
  /** kbit/s -> Kbps / Mbps / Gbps with at most three significant digits (same rule as the board) */
  bits(k: number): string {
    if (k < 1000) return `${k} Kbps`;
    if (k < 99950) { const t = Math.floor((k + 50) / 100); return `${Math.floor(t / 10)}.${t % 10} Mbps`; }
    if (k < 999500) return `${Math.floor((k + 500) / 1000)} Mbps`;
    const g = Math.floor((k + 5000) / 10000);
    return `${Math.floor(g / 100)}.${String(g % 100).padStart(2, '0')} Gbps`;
  }
  gb(b: number) { return b >= 2 ** 30 ? (b / 2 ** 30).toFixed(1) + ' G' : (b / 2 ** 20).toFixed(0) + ' M'; }
}
