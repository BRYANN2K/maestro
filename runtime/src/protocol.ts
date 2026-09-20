import { createInterface } from 'node:readline';

// JSONL is private IPC. Secrets travel only over stdin/stdout pipes, never argv.
export class Protocol {
  private next = 0;
  private pending = new Map<number, {resolve: (v: any) => void; reject: (e: Error) => void}>();
  private first!: (v: any) => void;
  readonly initial = new Promise<any>(resolve => { this.first = resolve; });
  private started = false;
  private closed = false;
  constructor() {
    const input = createInterface({ input: process.stdin });
    input.on('line', line => {
      if (Buffer.byteLength(line) > 16 * 1024 * 1024) { this.fail(new Error('IPC frame exceeds limit')); return; }
      try {
        const value = JSON.parse(line);
        if (!this.started) { this.started = true; this.first(value); return; }
        const waiter = this.pending.get(value.id);
        if (!waiter) throw new Error('unexpected IPC reply');
        this.pending.delete(value.id);
        if (value.error) waiter.reject(new Error(value.error)); else waiter.resolve(value.result);
      } catch (error) { this.fail(error as Error); }
    });
    input.on('close', () => this.fail(new Error('Maestro host disconnected')));
  }
  send(value: any) { process.stdout.write(JSON.stringify(value) + '\n'); }
  request(method: string, params: any = {}) {
    if(this.closed) return Promise.reject(new Error('Maestro host disconnected'));
    const id = ++this.next;
    return new Promise<any>((resolve,reject) => {
      this.pending.set(id, {resolve,reject}); this.send({id,method,params});
    });
  }
  fail(error: Error) {
    this.closed = true;
    for (const waiter of this.pending.values()) waiter.reject(error);
    this.pending.clear();
  }
}
