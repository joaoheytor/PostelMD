import { beforeEach, describe, expect, it, onTestFinished, vi } from "vitest";
import * as api from "./api";
import { type Draft, Drafts, MemoryDraftBackend } from "./drafts";
import { Workspace } from "./workspace";

vi.mock("./api", async (actual) => ({
  ...(await actual<typeof import("./api")>()),
  listFolder: vi.fn(async () => []),
  readNote: vi.fn(async (path: string) => ({ content: `# ${path}`, etag: '"e1"' })),
  saveNote: vi.fn(async () => ({ kind: "saved", etag: '"e2"' })),
  createNote: vi.fn(async () => '"e5"'),
}));

const other = (over: Partial<Draft> = {}): Draft => ({ path: "a.md", esid: "closed-tab", text: "lost edit", base: '"e0"', gen: 4, savedAt: 1, ...over });

describe("Workspace drafts (ADR-0003 §7)", () => {
  let backend: MemoryDraftBackend;
  let ws: Workspace;

  beforeEach(async () => {
    vi.clearAllMocks();
    backend = new MemoryDraftBackend();
    await backend.put(other());
    await backend.put(other({ path: "gone.md", esid: "crashed" }));
    ws = new Workspace();
    ws.useDrafts(await Drafts.open(backend, "me", () => "moved"));
  });

  const rows = async () => (await backend.all()).map((d) => `${d.path} ${d.esid} ${d.text}`).sort();

  it("opening a note offers the drafts other tabs left for it", async () => {
    await ws.open("a.md");
    expect(ws.noteDrafts.value.map((d) => d.text)).toEqual(["lost edit"]);
    await ws.open("b.md");
    expect(ws.noteDrafts.value).toEqual([]);
  });

  it("a note closed while its save fails, then reopened in this tab, offers that draft and keeps it until the user decides (#105)", async () => {
    await ws.open("b.md");
    ws.reader = () => "unsaved edit";
    vi.mocked(api.saveNote).mockResolvedValueOnce({ kind: "error", message: "down", retryAfterMs: 0, retry: true });
    ws.state.value.open?.doc.edit();
    expect(await ws.saveOpen()).toBe(false);
    await ws.open("b.md"); // closing writes the draft: the edit's only other copy
    expect(ws.noteDrafts.value.map((d) => d.text)).toEqual(["unsaved edit"]);

    ws.reader = () => "typed after reopening";
    ws.state.value.open?.doc.edit();
    await ws.state.value.open?.doc.writeDraft();
    expect(await ws.saveOpen()).toBe(true);
    await ws.drafts?.idle();
    expect((await backend.all()).filter((d) => d.path === "b.md").map((d) => d.text)).toEqual(["unsaved edit"]);
    expect(ws.offered.value.map((d) => d.text)).toContain("unsaved edit"); // the vault-wide list too
  });

  it("restoring a draft based on an older version than the saved one asks the user (412) instead of saving over it", async () => {
    // The server answers like the real one: a save lands only if its If-Match is current.
    const server = { content: "# b.md", etag: '"e1"', n: 1 };
    const [read, save] = [vi.mocked(api.readNote).getMockImplementation(), vi.mocked(api.saveNote).getMockImplementation()];
    onTestFinished(() => {
      if (read) vi.mocked(api.readNote).mockImplementation(read);
      if (save) vi.mocked(api.saveNote).mockImplementation(save);
    });
    vi.mocked(api.readNote).mockImplementation(async () => ({ content: server.content, etag: server.etag }));
    vi.mocked(api.saveNote).mockImplementation(async (_p, content, base) => {
      if (base !== server.etag) return { kind: "conflict", content: server.content, etag: server.etag };
      Object.assign(server, { content, etag: `"e${++server.n}"` });
      return { kind: "saved", etag: server.etag };
    });
    await ws.open("b.md");
    ws.reader = () => "old edit";
    vi.mocked(api.saveNote).mockResolvedValueOnce({ kind: "error", message: "down", retryAfterMs: 0, retry: true });
    ws.state.value.open?.doc.edit();
    expect(await ws.saveOpen()).toBe(false);
    await ws.open("b.md");
    let editor = "newer text";
    ws.reader = () => editor;
    ws.state.value.open?.doc.edit();
    expect(await ws.saveOpen()).toBe(true); // the user ignored the draft and saved newer text
    const d = ws.noteDrafts.value.find((x) => x.text === "old edit");
    if (!d) throw new Error("no draft offered");

    await ws.recoverDraft(d, (t) => (editor = t));
    await ws.saveOpen();
    expect(server.content).toBe("newer text"); // not overwritten without the user choosing
    expect(ws.state.value.open?.doc.state.value).toMatchObject({ status: "conflict", conflict: { content: "newer text" } });
  });

  it("an edit of the open note goes to this tab's draft", async () => {
    await ws.open("b.md");
    ws.reader = () => "typed";
    ws.state.value.open?.doc.edit();
    await ws.state.value.open?.doc.writeDraft();
    await ws.drafts?.idle();
    expect(await rows()).toContain("b.md me.1 typed"); // the tab's esid and the note session
  });

  it("restoring a draft puts it in the editor as an edit, keeps it as this tab's draft, then removes the old one", async () => {
    await ws.open("a.md");
    let editor = "# a.md";
    ws.reader = () => editor;
    const d = ws.noteDrafts.value[0];
    if (!d) throw new Error("no draft offered");
    const open = ws.state.value.open;
    await ws.recoverDraft(d, (t) => (editor = t));
    expect(ws.state.value.open).toBe(open); // the same note: the editor isn't remounted
    expect(editor).toBe("lost edit");
    expect(ws.state.value.open?.doc.dirty).toBe(true); // it saves through the normal path
    expect(await rows()).toEqual(["a.md me.1 lost edit", "gone.md crashed lost edit"]);
    expect(ws.noteDrafts.value).toEqual([]);
  });

  it("restoring a draft keeps the old one when this tab's draft can't be written (quota)", async () => {
    await ws.open("a.md");
    let editor = "# a.md";
    ws.reader = () => editor;
    const d = ws.noteDrafts.value[0];
    if (!d) throw new Error("no draft offered");
    vi.spyOn(backend, "put").mockRejectedValue(new DOMException("quota", "QuotaExceededError"));
    await ws.recoverDraft(d, (t) => (editor = t));
    expect(editor).toBe("lost edit");
    expect(await rows()).toContain("a.md closed-tab lost edit"); // still the edit's only other copy
  });

  it("discards only the draft the user picked", async () => {
    await ws.open("a.md");
    const d = ws.noteDrafts.value[0];
    if (!d) throw new Error("no draft offered");
    await ws.discardDraft(d);
    expect(await rows()).toEqual(["gone.md crashed lost edit"]);
    expect(ws.noteDrafts.value).toEqual([]);
  });

  it("saves an orphaned draft as a new note, and only then removes it", async () => {
    await ws.refreshOffered();
    expect(ws.offered.value.map((x) => x.path)).toEqual(["a.md", "gone.md"]);
    const [d] = ws.offered.value.filter((x) => x.path === "gone.md");
    if (!d) throw new Error("no orphan");
    vi.mocked(api.createNote).mockRejectedValueOnce(new api.ApiError(409, "exists"));
    await ws.saveDraftAsNew(d);
    expect(ws.state.value.error).toContain("gone-recovered.md");
    expect(await rows()).toContain("gone.md crashed lost edit"); // kept: nothing was saved
    await ws.saveDraftAsNew(d);
    expect(api.createNote).toHaveBeenLastCalledWith("gone-recovered.md", "lost edit");
    expect(await rows()).not.toContain("gone.md crashed lost edit");
    expect(ws.state.value.open?.doc.path).toBe("gone-recovered.md");
    expect(ws.offered.value.map((x) => x.path)).toEqual(["a.md"]);
  });

  it("after saving a deleted note's text as a new note, its old draft goes", async () => {
    await ws.open("b.md");
    ws.reader = () => "typed";
    vi.mocked(api.saveNote).mockResolvedValueOnce({ kind: "gone" });
    ws.state.value.open?.doc.edit();
    await ws.state.value.open?.doc.flush();
    await ws.saveAsNew();
    await ws.drafts?.idle();
    expect((await rows()).filter((r) => r.startsWith("b.md "))).toEqual([]);
  });
});
