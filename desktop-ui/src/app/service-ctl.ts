import { Component, inject, OnDestroy, signal } from '@angular/core';
import { Api } from './api';

@Component({
  selector: 'service-view',
  template: `
    <div class="grid">
      <section>
        <h2>tft-monitor service</h2>
        <div class="kv">
          @for (k of keys(); track k[0]) { <span>{{ k[0] }}</span><b class="wrap">{{ k[1] }}</b> }
        </div>
        <div class="row">
          <button (click)="act('start')">Start</button>
          <button (click)="act('restart')" class="primary">Restart</button>
          <button (click)="act('stop')">Stop</button>
        </div>
        <p class="muted">{{ msg() }}</p>
        <p class="muted">The daemon owns the serial port, so stopping it blanks the TFT updates until it is started again.</p>
      </section>
      <section class="wide">
        <h2>Logs</h2>
        <pre>{{ log() }}</pre>
      </section>
    </div>
  `,
})
export class ServiceView implements OnDestroy {
  api = inject(Api);
  keys = signal<[string, string][]>([]);
  log = signal('');
  msg = signal('');
  private t = setInterval(() => this.refresh(), 3000);

  constructor() { this.refresh(); }
  ngOnDestroy() { clearInterval(this.t); }

  async refresh() {
    try {
      const s = await this.api.service('status');
      this.keys.set(s.split('\n').map((l) => { const i = l.indexOf('='); return [l.slice(0, i), l.slice(i + 1)] as [string, string]; }));
      this.log.set(await this.api.logs());
    } catch (e) { this.msg.set(String(e)); }
  }

  async act(a: string) {
    this.msg.set(a + '…');
    try { this.msg.set((await this.api.service(a)) || a + ' ok'); } catch (e) { this.msg.set(String(e)); }
    setTimeout(() => this.refresh(), 800);
  }
}
