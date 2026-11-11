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

/** IndexedDB, as far as the tests go: it outlives the page, and its writes can fail (quota). */
class FlakyBackend extends MemoryDraftBackend implements DraftBackend {
  override readonly persistent: boolean = true;
  failing = false;
  override put(d: Draft): Promise<void> {
    return this.failing ? Promise.reject(new DOMException("quota", "QuotaExceededError")) : super.put(d);
  }
}

const failedSave = { kind: "error", message: "the server is busy", next: "Retrying", retryAfterMs: 0, retry: true } as const;

describe("App.logout", () => {
  let backend: FlakyBackend;
  let app: App;
  let navigated: string[];

  const start = async (b: DraftBackend) => {
    navigated = [];
    app = new App((url) => navigated.push(url));
    await app.boot(() => Drafts.open(b, "me", () => "moved"));
    await app.workspace.open("a.md");
  };

  beforeEach(async () => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    backend = new FlakyBackend();
    await start(backend);
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

  it("with every edit saved, closes the note, logs out and reloads the page", async () => {
    const doc = app.workspace.state.value.open?.doc;
    edit("# saved edit");
    await app.logout();
    expect(api.logout).toHaveBeenCalledTimes(1);
    expect(app.state.value.phase).toBe("login");
    expect(app.workspace.state.value.open).toBeUndefined();
    // Nothing of the vault (tree, git state) stays in the page's memory.
    expect(navigated).toEqual(["/"]);

    // The closed session never saves again, even if something pokes it after the editor is gone.
    unmount();
    vi.mocked(api.saveNote).mockClear();
    doc?.edit();
    await vi.advanceTimersByTimeAsync(60_000);
    await doc?.flush();
    expect(api.saveNote).not.toHaveBeenCalled();
  });

  it("says at once that it is saving first, while a save is in flight", async () => {
    let land: (r: api.SaveResult) => void = () => {};
    vi.mocked(api.saveNote).mockImplementationOnce(() => new Promise((r) => (land = r)));
    edit("# slow");
    const out = app.logout();
    expect(app.state.value.loggingOut).toBe(true);
    land({ kind: "saved", etag: '"e2"' });
    await out;
    expect(app.state.value.loggingOut).toBeFalsy();
    expect(navigated).toEqual(["/"]);
  });

  it("when the server can't be reached, says so and stays, ready to try again", async () => {
    vi.mocked(api.logout).mockRejectedValueOnce(new api.NetworkError());
    await app.logout(); // never rejects: a click handler calls it
    expect(app.state.value).toMatchObject({ phase: "ready", loggingOut: false });
    expect(app.workspace.state.value.error).toBe("Couldn't log out: can't reach the server");
    expect(app.workspace.state.value.open?.doc.path).toBe("a.md");
    expect(navigated).toEqual([]);

    await app.logout();
    expect(navigated).toEqual(["/"]);
  });

  it("stops when the open note's save fails, and asks, keeping the note open and its edits", async () => {
    vi.mocked(api.saveNote).mockResolvedValue(failedSave);
    edit("# unsaved edit");
    await app.logout();
    expect(api.logout).not.toHaveBeenCalled();
    expect(app.state.value.phase).toBe("ready");
    expect(app.state.value.loggingOut).toBeFalsy();
    expect(app.state.value.leaving).toEqual({ path: "a.md", why: "The server is busy.", draftFailed: false, persistent: true });
    expect(app.workspace.state.value.open?.doc.dirty).toBe(true);

    app.stay();
    expect(app.state.value.leaving).toBeUndefined();
    expect(app.workspace.state.value.open?.doc.path).toBe("a.md");
  });

  it("names the stall when the save timed out", async () => {
    vi.mocked(api.saveNote).mockResolvedValue({ kind: "unknown" });
    edit("# stuck");
    await app.logout();
    expect(app.state.value.leaving?.why).toBe("The server hasn't answered for 15 seconds.");
  });

  it("says why in a sentence: a conflict, an expired session, a stall", () => {
    expect(unsavedWhy({ status: "conflict" })).toBe("The note was changed somewhere else too.");
    expect(unsavedWhy({ status: "error", message: "your session expired", next: "Log in again" })).toBe("Your session expired.");
    expect(unsavedWhy({ status: "stalled" })).toBe("The server hasn't answered for 15 seconds.");
  });

  it("logging out anyway keeps the edits as a local draft, then logs out", async () => {
    vi.mocked(api.saveNote).mockResolvedValue(failedSave);
    edit("# unsaved edit");
    await app.logout();
    await app.logoutKeepingDraft();
    expect(await texts()).toEqual(["# unsaved edit"]);
    expect(api.logout).toHaveBeenCalledTimes(1);
    expect(app.state.value.phase).toBe("login");
    expect(app.state.value.leaving).toBeUndefined();
    expect(app.workspace.state.value.open).toBeUndefined();
    expect(navigated).toEqual(["/"]);

    // Nothing writes the loaded text over the draft afterwards, and it is offered.
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

  it("with drafts only in memory (no IndexedDB), never offers to keep them past the logout", async () => {
    await start(new MemoryDraftBackend()); // the page reloads on logout: memory drafts are gone
    vi.mocked(api.saveNote).mockResolvedValue(failedSave);
    edit("# unsaved edit");
    await app.logout();
    expect(app.state.value.leaving?.persistent).toBe(false);
    await app.logoutKeepingDraft();
    expect(api.logout).not.toHaveBeenCalled();
    expect(app.state.value.phase).toBe("ready");
  });
});

describe("Workspace with its editor gone", () => {
  let backend: MemoryDraftBackend;
  let app: App;

  beforeEach(async () => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    backend = new MemoryDraftBackend();
    app = new App(() => {});
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
