import type { Disposable } from "../core/disposable";
import type { DiagramReply, DiagramRequest } from "../diagram/protocol";

/** A rendered diagram, ready for an img: SVG in an <img> runs no script and loads nothing. */
export type Diagram = { ok: true; url: string; width: number; height: number; label: string } | { ok: false; error: string };

/** A diagram that hasn't come back by then is given up on, and the frame replaced. */
const timeoutMs = 15_000;

/**
 * Renders Mermaid diagrams in the sandboxed renderer frame (ADR-0021). The frame is created on
 * the first diagram, and talks only over a MessagePort handed over on its first load: if the
 * frame ever navigates away, the port dies with it and a new frame takes over. Results are
 * cached by source and theme while this lives; dispose revokes their blob URLs.
 */
export class Diagrams implements Disposable {
  private frame: HTMLIFrameElement | undefined;
  private port: Promise<MessagePort> | undefined;
  private seq = 0;
  private readonly waiting = new Map<number, { done: (d: Diagram) => void; timer: ReturnType<typeof setTimeout> }>();
  private readonly cache = new Map<string, Promise<Diagram>>();
  private readonly urls: string[] = [];

  render(source: string): Promise<Diagram> {
    const theme = matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "default";
    const key = `${theme}\n${source}`;
    let d = this.cache.get(key);
    if (!d) {
      d = this.request({ id: ++this.seq, source, theme });
      this.cache.set(key, d);
    }
    return d;
  }

  dispose(): void {
    this.reset("closed");
    this.cache.clear();
    for (const url of this.urls.splice(0)) URL.revokeObjectURL(url);
  }

  private async request(req: DiagramRequest): Promise<Diagram> {
    const port = await this.open();
    const reply = new Promise<Diagram>((done) => {
      const timer = setTimeout(() => {
        this.cache.clear(); // a source that hangs the renderer is retried on the next edit
        this.reset("the diagram took too long to render");
      }, timeoutMs);
      this.waiting.set(req.id, { done, timer });
    });
    port.postMessage(req);
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
    this.urls.push(url);
    w.done({ ok: true, url, width: r.width, height: r.height, label: r.label });
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
