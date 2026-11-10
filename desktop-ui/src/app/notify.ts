import { Component, inject, signal } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { Api } from './api';

@Component({
  selector: 'notify-view',
  imports: [FormsModule],
  template: `
    <div class="grid">
      <section>
        <h2>Send a notification to the TFT</h2>
        <label>Message <small>({{ text.length }}/60)</small>
          <input [(ngModel)]="text" maxlength="60" placeholder="Build finished" (keyup.enter)="send()" />
        </label>
        <div class="row">
          <label>Severity
            <select [(ngModel)]="sev"><option value="info">info</option><option value="warn">warn</option><option value="crit">crit</option></select>
          </label>
          <label>Show for (s)
            <input type="number" min="5" max="3600" [(ngModel)]="ttl" />
          </label>
        </div>
        <button class="primary" (click)="send()" [disabled]="!text.trim()">Send</button>
        <p class="muted">{{ status() }}</p>
        <h3>Quick</h3>
        <div class="row">
          @for (q of quick; track q.text) { <button (click)="go(q.sev, q.ttl, q.text)">{{ q.text }}</button> }
        </div>
      </section>
      <section>
        <h2>Active alerts</h2>
        @if (api.state()?.alerts?.length) {
          <ul>
            @for (a of api.state()!.alerts; track a.key) {
              <li><span class="sev s{{ a.sev }}">{{ ['ok','info','warn','crit'][a.sev] }}</span><span>{{ a.title }}<small class="muted"> {{ a.detail }}</small></span></li>
            }
          </ul>
        } @else { <p class="muted">Nothing is firing.</p> }
      </section>
    </div>
  `,
})
export class NotifyView {
  api = inject(Api);
  text = '';
  sev = 'info';
  ttl = 30;
  status = signal('');
  quick = [
    { text: 'Time for a break', sev: 'info', ttl: 60 },
    { text: 'Deploy running', sev: 'warn', ttl: 120 },
    { text: 'Meeting in 5 min', sev: 'info', ttl: 300 },
  ];

  send() { if (this.text.trim()) this.go(this.sev, this.ttl, this.text.trim()); }

  async go(sev: string, ttl: number, text: string) {
    try {
      await this.api.notify(sev, ttl, text);
      this.status.set(`sent “${text}”`);
      if (text === this.text.trim()) this.text = '';
    } catch (e) { this.status.set('failed: ' + e); }
  }
}
