import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "./api";
import { Drafts, MemoryDraftBackend } from "./drafts";
import { openSession } from "./open-file";
import { Workspace } from "./workspace";

vi.mock("./api", async (actual) => ({
  ...(await actual<typeof import("./api")>()),
  listFolder: vi.fn(async () => []),
  // Mixed line endings: it opens only converted to LF, which is an unsaved edit at once.
  readNote: vi.fn(async (path: string) => ({ content: path.endsWith(".txt") ? "a\r\nb\n" : `# ${path}\n`, etag: `"${path}"` })),
  saveNote: vi.fn(async () => ({ kind: "saved", etag: '"e2"' })),
  move: vi.fn(async () => {}),
  restore: vi.fn(async () => '"e3"'),
}));

/** A promise the test settles. */
function deferred(): { promise: Promise<void>; resolve: () => void } {
  let resolve = () => {};
  const promise = new Promise<void>((r) => (resolve = r));
  return { promise, resolve };
}

describe("Workspace: a session that isn't the open note never saves", () => {
  let ws: Workspace;
  let backend: MemoryDraftBackend;
  let drafts: Drafts;
  /** The paths whose draft keys were released (NoteDrafts.close). */
  let released: string[];

  beforeEach(async () => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    backend = new MemoryDraftBackend();
    drafts = await Drafts.open(backend, "me", () => "moved");
    released = [];
    const forNote = drafts.forNote.bind(drafts);
    vi.spyOn(drafts, "forNote").mockImplementation((path) => {
      const d = forNote(path);
      return { ...d, close: () => (released.push(path), d.close()) };
    });
    ws = new Workspace();
    ws.useDrafts(drafts);
    ws.reader = undefined; // no editor mounted yet
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("a converted open whose draft listing fails leaves no session saving behind", async () => {
    // open() reads path's drafts (one getAll), then refreshes the whole list (a second): it fails.
    const all = backend.all.bind(backend);
    vi.spyOn(backend, "all")
      .mockImplementationOnce(all)
      .mockRejectedValueOnce(new Error("getAll failed"));
    const sessions: NonNullable<Workspace["state"]["value"]["open"]>[] = [];
    ws.state.subscribe((s) => s.open && sessions.push(s.open));

    await ws.open("a.txt", true); // "Convert to LF and open": dirty at once
    const first = sessions[0];
    if (!first) throw new Error("a.txt's session was never published");
    expect(first.doc.dirty).toBe(true);
    // The note stays open and owned; the error names no IndexedDB detail.
    expect(ws.state.value.open).toBe(first);
    expect(ws.state.value.error).toBe(
      "Couldn't check this browser for unsaved drafts of your notes. This note is open and saves as usual; reload the page to check again.",
    );
    const dispose = vi.spyOn(first.doc, "dispose");

    await ws.open("b.md");
    // Opening b.md disposed a.txt's session and released its draft key, at once.
    expect(dispose).toHaveBeenCalled();
    expect(released).toContain("a.txt");
    expect(ws.state.value.open?.doc.path).toBe("b.md");

    let shown = ws.state.value.open?.shown ?? "";
    ws.reader = () => shown; // b.md's editor mounted
    shown += "b's text\n";
    ws.state.value.open?.doc.edit();
    await vi.advanceTimersByTimeAsync(120_000);

    const paths = vi.mocked(api.saveNote).mock.calls.map(([p]) => p);
    expect(paths).not.toContain("a.txt");
    expect(paths).toContain("b.md");
    expect((await backend.all()).filter((d) => d.path === "a.txt" && d.text.includes("b's text"))).toEqual([]);
  });

  it("a file refused for its line endings leaves no draft key live", async () => {
    await ws.open("a.txt"); // not converted: refused
    expect(ws.state.value.convert).toBe("a.txt");
    // Refused before any session starts (#134): no draft key was even taken.
    expect(drafts.forNote).not.toHaveBeenCalled();
    expect(released).toEqual([]);
  });

  it("a session that isn't the current one never reads the editor, and its timer stops", async () => {
    const reader = vi.fn(() => "another note's text");
    const note = { content: "a\r\nb\n", etag: '"a"' };
    const opened = openSession("a.txt", note, true, {
      reader,
      current: () => false, // the workspace shows another file
      save: api.saveNote,
      draft: drafts.forNote("a.txt"),
    });
    expect(opened.doc.dirty).toBe(true);
    await vi.advanceTimersByTimeAsync(120_000);
    expect(reader).not.toHaveBeenCalled();
    expect(api.saveNote).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0); // no save rescheduling forever (rule 8)
    expect(released).toContain("a.txt"); // it disposed itself: its draft is offered again
  });
});

describe("Workspace: a note opened during a move or a restore stays open", () => {
  let ws: Workspace;

  beforeEach(async () => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    ws = new Workspace();
    await ws.open("a.md");
    ws.reader = () => "edited";
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("a move that finishes after another note opened doesn't reopen the moved one", async () => {
    const slow = deferred();
    vi.mocked(api.move).mockImplementationOnce(() => slow.promise);
    const moving = ws.move("a.md", "c.md");
    await vi.advanceTimersByTimeAsync(0);
    expect(api.move).toHaveBeenCalled();

    await ws.open("b.md");
    const b = ws.state.value.open;
    const dispose = b && vi.spyOn(b.doc, "dispose");
    slow.resolve();
    expect(await moving).toBe(true);

    expect(ws.state.value.open).toBe(b);
    expect(dispose).not.toHaveBeenCalled();
    expect(api.readNote).not.toHaveBeenCalledWith("c.md");
  });

  it("a failed move doesn't reopen the old note over the one opened meanwhile", async () => {
    const slow = deferred();
    vi.mocked(api.move).mockImplementationOnce(() => slow.promise.then(() => Promise.reject(new Error("boom"))));
    const moving = ws.move("a.md", "c.md");
    await vi.advanceTimersByTimeAsync(0);
    expect(api.move).toHaveBeenCalled();

    await ws.open("b.md");
    const b = ws.state.value.open;
    vi.mocked(api.readNote).mockClear();
    slow.resolve();
    expect(await moving).toBe(false);
    expect(ws.state.value.open).toBe(b);
    expect(api.readNote).not.toHaveBeenCalled();
  });

  it("a restore that finishes after another note opened doesn't reopen the restored one", async () => {
    let finish = () => {};
    vi.mocked(api.restore).mockImplementationOnce(() => new Promise((r) => (finish = () => r('"e3"'))));
    const restoring = ws.restore("abc123");
    await vi.advanceTimersByTimeAsync(0);
    expect(api.restore).toHaveBeenCalled();

    await ws.open("b.md");
    const b = ws.state.value.open;
    vi.mocked(api.readNote).mockClear();
    finish();
    await restoring;
    expect(ws.state.value.open).toBe(b);
    expect(api.readNote).not.toHaveBeenCalled();
  });
});
