import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { DiagramReply, DiagramRequest } from "../diagram/protocol";
import { Diagrams } from "./diagrams";

// DOMPurify needs a DOM, which these tests don't have; the sanitizer has its own e2e test
// (03-diagrams). The blob URL still goes through URL.createObjectURL, which the tests count.
vi.mock("./svg", () => ({ svgBlobURL: (svg: string) => URL.createObjectURL(new Blob([svg])) }));

/**
 * A stand-in for the renderer frame: when it loads, it takes the port it's handed and answers
 * every request with an SVG. One that never loads stands for a frame page that can't be fetched.
 */
class FakeFrame {
  readonly sandbox = { add: () => {} };
  className = "";
  title = "";
  tabIndex = 0;
  src = "";
  private onLoad: (() => void) | undefined;
  readonly contentWindow = {
    postMessage: (_msg: unknown, _origin: string, [port]: MessagePort[]) => {
      if (!port) return;
      port.onmessage = (e: MessageEvent<DiagramRequest>) => {
        const reply: DiagramReply = { id: e.data.id, ok: true, svg: "<svg/>", width: 1, height: 1, label: e.data.source };
        port.postMessage(reply);
      };
    },
  };
  setAttribute(): void {}
  addEventListener(_type: string, fn: () => void): void {
    this.onLoad = fn;
  }
  remove(): void {}
  load(): void {
    this.onLoad?.();
  }
}

let loads: boolean;
let frames: FakeFrame[];
let live: Set<string>;

beforeEach(() => {
  loads = true;
  frames = [];
  live = new Set();
  let n = 0;
  vi.stubGlobal("matchMedia", () => ({ matches: false }));
  vi.stubGlobal("document", {
    createElement: () => new FakeFrame(),
    body: {
      append: (f: FakeFrame) => {
        frames.push(f);
        if (loads) queueMicrotask(() => f.load());
      },
    },
  });
  vi.spyOn(URL, "createObjectURL").mockImplementation(() => {
    const url = `blob:test/${++n}`;
    live.add(url);
    return url;
  });
  vi.spyOn(URL, "revokeObjectURL").mockImplementation((url) => void live.delete(url));
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("Diagrams", () => {
  it("renders a diagram through the frame", async () => {
    const d = new Diagrams();
    expect(await d.render("graph TD; a-->b")).toMatchObject({ ok: true, label: "graph TD; a-->b" });
    d.dispose();
  });

  it("gives up after the deadline when the frame never loads", async () => {
    vi.useFakeTimers();
    loads = false;
    const d = new Diagrams();
    let result: unknown;
    void d.render("graph TD; a-->b").then((r) => (result = r));
    await vi.advanceTimersByTimeAsync(14_000);
    expect(result).toBeUndefined();
    await vi.advanceTimersByTimeAsync(1_000);
    expect(result).toEqual({ ok: false, error: "the diagram took too long to render" });
    // The stuck frame is dropped: the next diagram starts a new one.
    void d.render("graph TD; b-->c");
    expect(frames).toHaveLength(2);
    d.dispose();
  });

  it("keeps a bounded number of blob URLs alive while a diagram is edited", async () => {
    const d = new Diagrams();
    for (let i = 0; i < 100; i++) await d.render(`graph TD; a-->b${i}`);
    await Promise.resolve(); // the revocations of the last evictions
    expect(URL.createObjectURL).toHaveBeenCalledTimes(100);
    expect(live.size).toBeLessThanOrEqual(32);
    d.dispose();
    await Promise.resolve();
    expect(live.size).toBe(0);
  });

  it("never revokes the URL of a diagram still on screen", async () => {
    const shown = new Set<string>();
    const d = new Diagrams((url) => shown.has(url));
    const first = await d.render("graph TD; first");
    if (!first.ok) throw new Error(first.error);
    shown.add(first.url);
    for (let i = 0; i < 40; i++) await d.render(`graph TD; a-->b${i}`);
    await Promise.resolve();
    expect(live.has(first.url)).toBe(true);
    shown.delete(first.url); // replaced on screen: the pane's next sweep revokes it
    d.sweep();
    expect(live.has(first.url)).toBe(false);
    d.dispose();
  });

  it("renders nothing once disposed", async () => {
    const d = new Diagrams();
    d.dispose();
    expect(await d.render("graph TD; a-->b")).toEqual({ ok: false, error: "closed" });
    expect(frames).toHaveLength(0);
  });
});
