import { Component, inject, signal } from '@angular/core';
import { Api } from './api';
import { LiveView } from './live';
import { NotifyView } from './notify';
import { PanelView } from './panel-ctl';
import { ServiceView } from './service-ctl';

type Tab = 'live' | 'notify' | 'panel' | 'service';

@Component({
  selector: 'app-root',
  imports: [LiveView, NotifyView, PanelView, ServiceView],
  template: `
    <header>
      <h1>TFT Smarter</h1>
      <nav>
        @for (t of tabs; track t[0]) {
          <button [class.active]="tab() === t[0]" (click)="tab.set(t[0])">{{ t[1] }}</button>
        }
      </nav>
      <span class="status" [class.bad]="!!api.error() || !api.state()?.connected">
        {{ api.error() ? 'daemon offline' : api.state()?.connected ? 'connected' : 'board offline' }}
      </span>
    </header>
    @if (api.error()) { <div class="banner">{{ api.error() }} — start the service in the Service tab.</div> }
    <main>
      @switch (tab()) {
        @case ('live') { <live-view /> }
        @case ('notify') { <notify-view /> }
        @case ('panel') { <panel-view /> }
        @case ('service') { <service-view /> }
      }
    </main>
  `,
})
export class App {
  api = inject(Api);
  tab = signal<Tab>('live');
  tabs: [Tab, string][] = [['live', 'Live'], ['notify', 'Notify'], ['panel', 'Panel'], ['service', 'Service']];
}
