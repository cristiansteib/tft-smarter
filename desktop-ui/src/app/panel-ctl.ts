import { Component, inject } from '@angular/core';
import { Api } from './api';

const INFO: Record<string, string> = {
  dash: 'home screen: gauges + the four fullest disks', docker: 'full screen · only when a container is unhealthy',
  system: 'full screen · only when pressure / NVMe / ping is off', top: 'full screen · busiest processes (≥10% CPU)',
  rollcpu: 'full screen · CPU hogs + load scope (busy only)', rollmem: 'full screen · RAM hogs (busy only, rolls if long)',
};

@Component({
  selector: 'panel-view',
  template: `
    @if (api.settings(); as c) {
      <div class="grid">
        <section>
          <h2>Pages</h2>
          <p class="muted">Eligible pages rotate automatically. Pin one to hold it on screen (alerts still take over).</p>
          <ul class="pages">
            @for (p of api.state()?.panel?.pages ?? []; track p.name) {
              <li [class.current]="api.state()?.panel?.page === p.name">
                <label class="chk"><input type="checkbox" [checked]="!c.disabled[p.name]" (change)="toggle(p.name, $any($event.target).checked)" /></label>
                <span class="name">{{ p.name }}<small class="muted">{{ info[p.name] }}</small></span>
                <span class="tag" [class.on]="p.eligible">{{ p.eligible ? 'eligible' : 'idle' }}</span>
                <button (click)="api.pin(c.pin === p.name ? '' : p.name)" [class.primary]="c.pin === p.name">{{ c.pin === p.name ? 'Unpin' : 'Pin' }}</button>
              </li>
            }
          </ul>
          <div class="row"><button (click)="api.next()">Next page ▶</button><button (click)="api.pin('')" [disabled]="!c.pin">Release pin</button></div>
        </section>
        <section>
          <h2>Thresholds &amp; timing</h2>
          @for (f of fields; track f.key) {
            <label class="slider">
              <span>{{ f.label }} <b>{{ val(f.key) }}{{ f.unit }}</b></span>
              <input type="range" [min]="f.min" [max]="f.max" [step]="f.step" [value]="val(f.key)" (change)="set(f.key, +$any($event.target).value)" />
              <small class="muted">{{ f.hint }}</small>
            </label>
          }
        </section>
      </div>
    } @else { <p class="muted">Loading settings…</p> }
  `,
})
export class PanelView {
  api = inject(Api);
  info = INFO;
  fields = [
    { key: 'hogCpu', label: 'CPU hog threshold', unit: '% of a core', min: 10, max: 400, step: 5, hint: 'a process is listed in the CPU credits above this' },
    { key: 'hogMem', label: 'RAM hog threshold', unit: '% of RAM', min: 1, max: 50, step: 1, hint: 'a process is listed in the RAM credits above this' },
    { key: 'loadTrigger', label: 'Credits when load/core ≥', unit: '%', min: 20, max: 200, step: 5, hint: '1-minute load average per core' },
    { key: 'ramTrigger', label: 'RAM credits when used ≥', unit: '%', min: 50, max: 99, step: 1, hint: 'RAM in use' },
    { key: 'dwellSec', label: 'Seconds per page', unit: ' s', min: 3, max: 60, step: 1, hint: 'rotation speed' },
    { key: 'creditStepMs', label: 'Credits roll speed', unit: ' ms/row', min: 400, max: 3000, step: 100, hint: 'lower is faster' },
  ];
  val(k: string): number { return (this.api.settings() as any)[k]; }
  set(k: string, v: number) { this.api.saveSettings({ [k]: v } as any); }
  toggle(name: string, on: boolean) {
    this.api.saveSettings({ disabled: { ...this.api.settings()!.disabled, [name]: !on } });
  }
}
