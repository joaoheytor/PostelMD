import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "./api";
import { App, unsavedWhy } from "./app";
import { type Draft, type DraftBackend, Drafts, MemoryDraftBackend } from "./drafts";

vi.mock("./api", async (actual) => ({
  ...(await actual<typeof import("./api")>()),
  listFolder: vi.fn(async () => []),
  readNote: vi.fn(async () => ({ content: "# loaded", etag: '"e1"' })),
  saveNote: vi.fn(async () => ({ kind: "saved", etag: '"e2"' })),
  logout: vi.fn(async () => {}),
  sessionState: vi.fn(async () => "ready"),
}));

/** A draft store whose writes can be made to fail, as IndexedDB does when over quota. */
class FlakyBackend extends MemoryDraftBackend implements DraftBackend {
  failing = false;
  override put(d: Draft): Promise<void> {
    return this.failing ? Promise.reject(new DOMException("quota", "QuotaExceededError")) : super.put(d);
  }
}

const failedSave = { kind: "error", message: "the server is busy", retryAfterMs: 0, retry: true } as const;

describe("App.logout", () => {
  let backend: FlakyBackend;
  let app: App;

  beforeEach(async () => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    backend = new FlakyBackend();
    app = new App();
    await app.boot(() => Drafts.open(backend, "me", () => "moved"));
    await app.workspace.open("a.md");
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  const edit = (text: string) => {
    app.workspace.reader = () => text;
    app.workspace.state.value.open?.doc.edit();
  };
  /** The editor unmounts when the workspace is no longer shown. */
  const unmount = () => (app.workspace.reader = undefined);
  const texts = async () => (await backend.all()).map((d) => d.text);

  it("with every edit saved, closes the note and logs out", async () => {
    const doc = app.workspace.state.value.open?.doc;
    edit("# saved edit");
    await app.logout();
    expect(api.logout).toHaveBeenCalledTimes(1);
    expect(app.state.value).toEqual({ phase: "login", reauth: false });
    expect(app.workspace.state.value.open).toBeUndefined();

    // The closed session never saves again, even if something pokes it after the editor is gone.
    unmount();
    vi.mocked(api.saveNote).mockClear();
    doc?.edit();
    await vi.advanceTimersByTimeAsync(60_000);
    await doc?.flush();
    expect(api.saveNote).not.toHaveBeenCalled();
  });

  it("stops when the open note's save fails, and asks, keeping the note open and its edits", async () => {
    vi.mocked(api.saveNote).mockResolvedValue(failedSave);
    edit("# unsaved edit");
    await app.logout();
    expect(api.logout).not.toHaveBeenCalled();
    expect(app.state.value.phase).toBe("ready");
    expect(app.state.value.leaving).toEqual({ path: "a.md", why: "the server is busy", draftFailed: false });
    expect(app.workspace.state.value.open?.doc.dirty).toBe(true);

    app.stay();
    expect(app.state.value.leaving).toBeUndefined();
    expect(app.workspace.state.value.open?.doc.path).toBe("a.md");
  });

  it("says why for a conflict, too", async () => {
    vi.mocked(api.saveNote).mockResolvedValue({ kind: "conflict", etag: '"e9"', content: "theirs" });
    edit("# mine");
    await app.logout();
    expect(app.state.value.leaving?.why).toBe("the note was changed somewhere else too");
  });

  it("says why in a few words for an expired session or a stalled save", () => {
    const expired = "your session expired. Log in again; your edits are kept in this browser.";
    expect(unsavedWhy({ status: "error", message: expired })).toBe("your session expired");
    expect(unsavedWhy({ status: "stalled" })).toBe("saving is taking too long");
  });

  it("logging out anyway keeps the edits as a local draft, then logs out", async () => {
    vi.mocked(api.saveNote).mockResolvedValue(failedSave);
    edit("# unsaved edit");
    await app.logout();
    await app.logoutKeepingDraft();
    expect(await texts()).toEqual(["# unsaved edit"]);
    expect(api.logout).toHaveBeenCalledTimes(1);
    expect(app.state.value).toEqual({ phase: "login", reauth: false });
    expect(app.workspace.state.value.open).toBeUndefined();

    // Logged in again: the draft is offered, and nothing wrote the loaded text over it.
    unmount();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(await texts()).toEqual(["# unsaved edit"]);
    await app.workspace.refreshOffered();
    expect(app.workspace.offered.value.map((d) => d.text)).toEqual(["# unsaved edit"]);
  });

  it("doesn't log out when the draft can't be kept either, and says so", async () => {
    vi.mocked(api.saveNote).mockResolvedValue(failedSave);
    backend.failing = true;
    edit("# unsaved edit");
    await app.logout();
    await app.logoutKeepingDraft();
    expect(api.logout).not.toHaveBeenCalled();
    expect(app.state.value.phase).toBe("ready");
    expect(app.state.value.leaving?.draftFailed).toBe(true);
    expect(app.workspace.state.value.open?.doc.dirty).toBe(true);
  });
});

describe("Workspace with its editor gone", () => {
  let backend: MemoryDraftBackend;
  let app: App;

  beforeEach(async () => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    backend = new MemoryDraftBackend();
    app = new App();
    await app.boot(() => Drafts.open(backend, "me", () => "moved"));
    await app.workspace.open("a.md");
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("never saves or drafts the text the note was loaded with over the user's edits", async () => {
    vi.mocked(api.saveNote).mockResolvedValue(failedSave);
    const doc = app.workspace.state.value.open?.doc;
    app.workspace.reader = () => "# my edits";
    doc?.edit();
    await doc?.writeDraft();
    app.workspace.reader = () => "# more edits";
    doc?.edit(); // not in a draft yet
    app.workspace.reader = undefined; // the editor went away
    vi.mocked(api.saveNote).mockClear();
    await vi.advanceTimersByTimeAsync(60_000);
    await app.workspace.open("b.md"); // closing writes the pending draft, if it can read one
    await app.workspace.drafts?.idle();
    const sent = vi.mocked(api.saveNote).mock.calls.map((c) => c[1]);
    expect(sent).not.toContain("# loaded");
    expect((await backend.all()).map((d) => d.text)).toEqual(["# my edits"]);
  });
});
