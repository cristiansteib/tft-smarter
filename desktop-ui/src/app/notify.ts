import { Component, inject, signal } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { Api, Level, Message } from './api';

const LEVELS: Level[] = ['info', 'success', 'warn', 'error', 'action'];

@Component({
  selector: 'notify-view',
  imports: [FormsModule],
  template: `
    <div class="grid">
      <section>
        <h2>Send a notice to the TFT</h2>
        <label>Title <input [(ngModel)]="m.title" maxlength="120" placeholder="Build finished" (keyup.enter)="send()" /></label>
        <label>Body <small class="muted">(optional)</small> <input [(ngModel)]="m.body" maxlength="600" /></label>
        <label>Action <small class="muted">(what to do, optional)</small> <input [(ngModel)]="m.action" maxlength="120" /></label>
        <div class="row">
          <label>Level
            <select [(ngModel)]="m.level">@for (l of levels; track l) { <option [value]="l">{{ l }}</option> }</select>
          </label>
          <label>View
            <select [(ngModel)]="m.view"><option value="auto">auto</option><option value="simple">simple</option><option value="detail">detail</option></select>
          </label>
          <label>Show for (s, 0 = until tapped, empty = level default)
            <input type="number" min="0" max="86400" [(ngModel)]="ttl" />
          </label>
        </div>
        <button class="primary" (click)="send()" [disabled]="!m.title.trim()">Send</button>
        <p class="muted">{{ status() }} · from a terminal or an agent: <code>tft-notify --help</code></p>
        <h3>Quick</h3>
        <div class="row">
          @for (q of quick; track q.title) { <button (click)="go(q)">{{ q.title }}</button> }
        </div>
      </section>
      <section>
        <h2>On screen</h2>
        @if (api.state()?.notices?.length) {
          <ul>
            @for (n of api.state()!.notices; track n.id) {
              <li><span class="sev lv-{{ n.level }}">{{ n.level }}</span><span>{{ n.title }}<small class="muted"> {{ n.source }}</small></span>
                <button (click)="api.dismiss(n.id)">Dismiss</button></li>
            }
          </ul>
          <button (click)="api.dismissAll()">Dismiss all</button>
        } @else { <p class="muted">No notices.</p> }
        <h2>System alerts</h2>
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
  levels = LEVELS;
  m: Message & { title: string } = { title: '', level: 'info', view: 'auto' };
  ttl: number | null = null;
  status = signal('');
  quick: Message[] = [
    { title: 'Time for a break', level: 'info', ttl: 60 },
    { title: 'Deploy running', level: 'warn', ttl: 120 },
    { title: 'Meeting in 5 min', level: 'action', action: 'Wrap up', ttl: 300 },
  ];

  send() {
    if (!this.m.title.trim()) return;
    const msg: Message = { ...this.m, title: this.m.title.trim() };
    if (this.ttl !== null && this.ttl >= 0) msg.ttl = this.ttl;
    this.go(msg).then((ok) => ok && (this.m = { title: '', level: this.m.level, view: this.m.view }));
  }

  async go(msg: Message): Promise<boolean> {
    try {
      await this.api.notify(msg);
      this.status.set(`sent “${msg.title}”`);
      return true;
    } catch (e) {
      this.status.set('failed: ' + e);
      return false;
    }
  }
}
