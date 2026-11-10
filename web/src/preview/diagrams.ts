import type { Disposable } from "../core/disposable";
import type { DiagramReply, DiagramRequest } from "../diagram/protocol";

/** A rendered diagram, ready for an img: SVG in an <img> runs no script and loads nothing. */
export type Diagram = { ok: true; url: string; width: number; height: number; label: string } | { ok: false; error: string };

/** A diagram that hasn't come back by then, frame load included, is given up on and the frame replaced. */
const timeoutMs = 15_000;

/** Diagrams kept for reuse, most recently used last: editing one renders a new one per pause. */
const cacheSize = 32;

/**
 * Renders Mermaid diagrams in the sandboxed renderer frame (ADR-0021). The frame is created on
 * the first diagram, and talks only over a MessagePort handed over on its first load: if the
 * frame ever navigates away, the port dies with it and a new frame takes over. The last
 * cacheSize results are cached by source and theme; an older one's blob URL is revoked once
 * it isn't on screen, and dispose revokes them all.
 */
export class Diagrams implements Disposable {
  private frame: HTMLIFrameElement | undefined;
  private port: Promise<MessagePort> | undefined;
  private seq = 0;
  private disposed = false;
  private readonly waiting = new Map<number, { done: (d: Diagram) => void; timer: ReturnType<typeof setTimeout> }>();
  private readonly cache = new Map<string, Promise<Diagram>>();
  /** URLs dropped from the cache while still on screen: revoked once they aren't. */
  private readonly retired = new Set<string>();

  /** shown tells whether a diagram's URL is still on screen, so it isn't revoked under it. */
  constructor(private readonly shown: (url: string) => boolean = () => false) {}

  render(source: string): Promise<Diagram> {
    const theme = matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "default";
    const key = `${theme}\n${source}`;
    let d = this.cache.get(key);
    if (d) this.cache.delete(key); // set again below: now the most recently used
    else d = this.request({ id: ++this.seq, source, theme });
    this.cache.set(key, d);
    for (const old of this.cache.keys()) {
      if (this.cache.size <= cacheSize) break;
      this.evict(old);
    }
    return d;
  }

  dispose(): void {
    this.disposed = true;
    this.reset("closed");
    this.evictAll();
    this.sweep();
  }

  /** One deadline for loading the frame and rendering: a frame that never loads fails too. */
  private request(req: DiagramRequest): Promise<Diagram> {
    const reply = new Promise<Diagram>((done) => {
      const timer = setTimeout(() => {
        this.evictAll(); // a source that hangs the renderer is retried on the next edit
        this.reset("the diagram took too long to render");
      }, timeoutMs);
      this.waiting.set(req.id, { done, timer });
    });
    void this.open().then((port) => {
      if (this.waiting.has(req.id)) port.postMessage(req);
    });
    return reply;
  }

  private open(): Promise<MessagePort> {
    this.port ??= new Promise((resolve) => {
      const frame = document.createElement("iframe");
      frame.sandbox.add("allow-scripts"); // before src: an opaque origin from the first load
      frame.className = "pm-diagram-frame";
      frame.title = "Diagram renderer";
      frame.tabIndex = -1;
      frame.setAttribute("aria-hidden", "true");
      let loads = 0;
      frame.addEventListener("load", () => {
        if (++loads > 1) {
          this.reset("the diagram renderer stopped"); // it navigated: never trust it again
          return;
        }
        const channel = new MessageChannel();
        channel.port1.onmessage = (e: MessageEvent<DiagramReply>) => this.answer(e.data);
        // An opaque origin can't be named, hence "*"; the port is what binds us to this load.
        frame.contentWindow?.postMessage({ type: "postelmd-diagram" }, "*", [channel.port2]);
        resolve(channel.port1);
      });
      frame.src = "/frames/diagram";
      document.body.append(frame);
      this.frame = frame;
    });
    return this.port;
  }

  private answer(r: DiagramReply): void {
    const w = this.waiting.get(r.id);
    if (!w) return;
    this.waiting.delete(r.id);
    clearTimeout(w.timer);
    if (!r.ok) {
      w.done({ ok: false, error: r.error });
      return;
    }
    const url = URL.createObjectURL(new Blob([r.svg], { type: "image/svg+xml" }));
    w.done({ ok: true, url, width: r.width, height: r.height, label: r.label });
  }

  /** Drops a result from the cache; its URL is revoked once it's off screen. */
  private evict(key: string): void {
    const d = this.cache.get(key);
    this.cache.delete(key);
    void d?.then((r) => {
      if (r.ok) this.retired.add(r.url);
      this.sweep();
    });
  }

  private evictAll(): void {
    for (const key of [...this.cache.keys()]) this.evict(key);
  }

  /** Revokes every retired URL no longer on screen; all of them once disposed. */
  private sweep(): void {
    for (const url of this.retired) {
      if (this.disposed || !this.shown(url)) {
        URL.revokeObjectURL(url);
        this.retired.delete(url);
      }
    }
  }

  /** Drops the frame and fails every diagram still waiting; the next one starts a new frame. */
  private reset(error: string): void {
    void this.port?.then((p) => p.close());
    this.port = undefined;
    this.frame?.remove();
    this.frame = undefined;
    for (const w of this.waiting.values()) {
      clearTimeout(w.timer);
      w.done({ ok: false, error });
    }
    this.waiting.clear();
  }
}
