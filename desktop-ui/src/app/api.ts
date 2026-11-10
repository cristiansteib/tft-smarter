import { Injectable, signal } from '@angular/core';
import { invoke } from '@tauri-apps/api/core';

export interface Cell { slot: number; style: number; pct: number; text: string }
export interface Page { name: string; eligible: boolean; enabled: boolean }
export interface Alert { key: string; title: string; detail: string; sev: number }
export interface Proc { name: string; v: number }
export interface State {
  connected: boolean;
  updated: string;
  gauges: {
    cpu: number; ram: number; swap: number; cpuT: number; gpu: number; gpuT: number; vram: number;
    gpuW: number; gpuLimit: number; nvmeT: number; latency: number; down: number; up: number;
    uptime: number; load: [number, number, number]; ncpu: number;
  };
  disks: { name: string; pct: number }[];
  psi: [number, number, number];
  topCpu: Proc[];
  topMem: Proc[];
  panel: { page: string; sub: number; subs: number; pin: string; full: boolean; pages: Page[]; cells: Cell[] };
  alerts: Alert[];
  docker: Record<string, number>;
  hog: { cpu: boolean; mem: boolean };
}
export interface Settings {
  hogCpu: number; hogMem: number; loadTrigger: number; ramTrigger: number;
  creditStepMs: number; dwellSec: number;
  disabled: Record<string, boolean>; pin: string;
}

/** Talks to the tft-monitor daemon through the Rust side (unix control socket). */
@Injectable({ providedIn: 'root' })
export class Api {
  readonly state = signal<State | null>(null);
  readonly settings = signal<Settings | null>(null);
  readonly error = signal<string>('');
  /** last 87 CPU/GPU samples, like the scope on the TFT */
  readonly history = signal<{ cpu: number; gpu: number }[]>([]);

  constructor() {
    this.poll();
    setInterval(() => this.poll(), 500);
    this.loadSettings();
  }

  private async poll() {
    try {
      const st = await this.ctl<State>({ cmd: 'state' });
      this.state.set(st);
      if (st.connected) this.history.update((h) => [...h.slice(-86), { cpu: st.gauges.cpu, gpu: st.gauges.gpu }]);
      this.error.set('');
    } catch (e) {
      this.error.set(String(e));
    }
  }

  async loadSettings() {
    try { this.settings.set(await this.ctl<Settings>({ cmd: 'settings' })); } catch { /* shown by poll */ }
  }

  ctl<T = unknown>(req: Record<string, unknown>): Promise<T> {
    return invoke<T>('ctl', { req });
  }

  async saveSettings(patch: Partial<Settings>) {
    const next = { ...this.settings()!, ...patch };
    this.settings.set(next);
    this.settings.set(await this.ctl<Settings>({ cmd: 'set', settings: next }));
  }

  async pin(page: string) { this.settings.set(await this.ctl<Settings>({ cmd: 'pin', page })); }
  next() { return this.ctl({ cmd: 'next' }); }
  notify(sev: string, ttl: number, text: string) { return this.ctl({ cmd: 'notify', sev, ttl, text }); }
  service(action: string) { return invoke<string>('service', { action }); }
  logs(lines = 150) { return invoke<string>('logs', { lines }); }
}
