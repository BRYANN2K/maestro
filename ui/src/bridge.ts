import { connect, type Socket } from "node:net";
import { StringDecoder } from "node:string_decoder";
import type { Client, UIEvent } from "./types";
export class Bridge implements Client {
  private socket: Socket;
  private next = 0;
  private listeners = new Set<(event: UIEvent) => void>();
  private pending = new Map<
    number,
    { resolve: (value: any) => void; reject: (error: Error) => void }
  >();
  private closed = false;
  private ready: Promise<void>;
  constructor(address: string, token: string) {
    const [host, port] = address.split(":");
    if (
      host !== "127.0.0.1" ||
      !/^\d+$/.test(port || "") ||
      token.length !== 64
    )
      throw Error("Launch this interface with maestro.");
    this.socket = connect({ host, port: Number(port) });
    this.socket.setNoDelay(true);
    this.ready = new Promise((resolve, reject) => {
      this.socket.once("connect", () => {
        this.socket.write(JSON.stringify({ op: "connect", token }) + "\n");
        resolve();
      });
      this.socket.once("error", reject);
    });
    let buffer = "";
    const decoder = new StringDecoder("utf8");
    this.socket.on("data", (chunk) => {
      buffer += decoder.write(chunk);
      if (Buffer.byteLength(buffer) > 4 * 1024 * 1024) {
        this.close();
        return;
      }
      let at: number;
      while ((at = buffer.indexOf("\n")) >= 0) {
        const line = buffer.slice(0, at);
        buffer = buffer.slice(at + 1);
        try {
          const value = JSON.parse(line);
          if (value.event) {
            for (const listener of this.listeners) listener(value);
          } else {
            const pending = this.pending.get(value.id);
            if (!pending) throw Error("Unexpected host reply");
            this.pending.delete(value.id);
            value.error
              ? pending.reject(Error(value.error))
              : pending.resolve(value.data);
          }
        } catch {
          this.close();
          return;
        }
      }
    });
    this.socket.on("error", () => this.close());
    this.socket.on("close", () => this.close());
  }
  async connected() {
    await this.ready;
  }
  async request<T = any>(
    op: string,
    args: Record<string, unknown> = {},
  ): Promise<T> {
    await this.ready;
    if (this.closed) throw Error("Maestro disconnected");
    if (this.pending.size >= 8)
      throw Error("Too many pending interface requests");
    const id = ++this.next;
    const line = JSON.stringify({ id, op, args }) + "\n";
    if (Buffer.byteLength(line) > 1024 * 1024) throw Error("Request too large");
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.socket.write(line, (error) => {
        if (error) {
          this.pending.delete(id);
          reject(error);
        }
      });
    });
  }
  subscribe(listener: (event: UIEvent) => void) {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }
  close() {
    if (this.closed) return;
    this.closed = true;
    this.socket.destroy();
    for (const p of this.pending.values())
      p.reject(Error("Maestro disconnected"));
    this.pending.clear();
    for (const l of this.listeners) l({ event: "disconnect", data: null });
  }
}
