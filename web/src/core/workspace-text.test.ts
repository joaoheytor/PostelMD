import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "./api";
import { Drafts, MemoryDraftBackend } from "./drafts";
import { BinaryError, bomChar } from "./textfile";
import { Workspace } from "./workspace";

vi.mock("./api", async (actual) => ({
  ...(await actual<typeof import("./api")>()),
  listFolder: vi.fn(async () => []),
  // A byte order mark (U+FEFF) and CRLF lines.
  readNote: vi.fn(async () => ({ content: `${String.fromCharCode(0xfeff)}a: 1\r\nb: 2\r\n`, etag: '"e1"' })),
  saveNote: vi.fn(async () => ({ kind: "saved", etag: '"e2"' })),
  createNote: vi.fn(async () => '"e5"'),
}));

/** What the editor shows: the test types by changing it. */
let shown = "";

describe("Workspace: files that aren't Markdown", () => {
  let ws: Workspace;
  let backend: MemoryDraftBackend;

  beforeEach(async () => {
    vi.clearAllMocks();
    backend = new MemoryDraftBackend();
    ws = new Workspace();
    ws.useDrafts(await Drafts.open(backend, "me", () => "moved"));
  });

  async function openAndAttach(path: string): Promise<void> {
    await ws.open(path);
    shown = ws.state.value.open?.shown ?? "";
    ws.reader = () => shown;
  }

  it("the editor shows LF lines without the byte order mark, and a save writes both back", async () => {
    await openAndAttach("k8s/app.yaml");
    expect(shown).toBe("a: 1\nb: 2\n");
    shown += "c: 3\n";
    ws.state.value.open?.doc.edit();
    expect(await ws.saveOpen()).toBe(true);
    expect(api.saveNote).toHaveBeenCalledWith("k8s/app.yaml", `${bomChar}a: 1\r\nb: 2\r\nc: 3\r\n`, '"e1"');
  });

  it("a draft holds the file's text, and restoring it shows the editor's", async () => {
    await openAndAttach("k8s/app.yaml");
    shown += "c: 3\n";
    ws.state.value.open?.doc.edit();
    await ws.state.value.open?.doc.writeDraft();
    const [draft] = await backend.all();
    expect(draft?.text).toBe(`${bomChar}a: 1\r\nb: 2\r\nc: 3\r\n`);
    if (!draft) return;

    const put = vi.fn();
    await ws.recoverDraft({ ...draft, esid: "other-tab" }, put);
    expect(put).toHaveBeenCalledWith("a: 1\nb: 2\nc: 3\n");
  });

  it("a binary file doesn't open, and says why", async () => {
    vi.mocked(api.readNote).mockRejectedValueOnce(new BinaryError());
    await ws.open("logo.png");
    expect(ws.state.value.open).toBeUndefined();
    expect(ws.state.value.error).toBe("PostelMD opens only UTF-8 text files, and logo.png isn't one.");
    expect(ws.state.value.convert).toBeUndefined(); // nothing to convert
  });

  it("a file with mixed line endings doesn't open as it is, and offers to convert them", async () => {
    vi.mocked(api.readNote).mockResolvedValueOnce({ content: "a\r\nb\n", etag: '"e1"' });
    await ws.open("mixed.conf");
    expect(ws.state.value.open).toBeUndefined();
    expect(ws.state.value.error).toBe(
      "PostelMD can't open mixed.conf as it is: it mixes Windows (CRLF) and Unix (LF) line endings. Convert them to LF to edit it here. This saves the file with LF line endings.",
    );
    expect(ws.state.value.convert).toBe("mixed.conf");
    expect(api.saveNote).not.toHaveBeenCalled(); // never converted without the user's click
  });

  it("a lone CR gets the same offer", async () => {
    vi.mocked(api.readNote).mockResolvedValueOnce({ content: "a\rb", etag: '"e1"' });
    await ws.open("old.txt");
    expect(ws.state.value.error).toBe(
      "PostelMD can't open old.txt as it is: it has old Mac (CR) line endings. Convert them to LF to edit it here. This saves the file with LF line endings.",
    );
    expect(ws.state.value.convert).toBe("old.txt");
  });

  describe("converting line endings, at the user's click", () => {
    const mixed = `${String.fromCharCode(0xfeff)}a\r\nb\nc\rd`;

    const usual = vi.mocked(api.readNote).getMockImplementation();
    beforeEach(() => {
      vi.mocked(api.readNote).mockImplementation(async () => ({ content: mixed, etag: '"e1"' }));
    });
    afterEach(() => {
      if (usual) vi.mocked(api.readNote).mockImplementation(usual);
    });

    it("opens the file with LF lines as an unsaved edit, saved with If-Match against the version read", async () => {
      await ws.open("mixed.conf", true);
      expect(ws.state.value.error).toBeUndefined();
      expect(ws.state.value.convert).toBeUndefined();
      shown = ws.state.value.open?.shown ?? "";
      ws.reader = () => shown;
      expect(shown).toBe("a\nb\nc\nd");
      expect(ws.state.value.open?.doc.dirty).toBe(true);
      expect(await ws.saveOpen()).toBe(true);
      // The byte order mark is no line ending: it stays as it was.
      expect(api.saveNote).toHaveBeenCalledWith("mixed.conf", `${bomChar}a\nb\nc\nd`, '"e1"');
    });

    it("saves on its own even when the editor mounts after the first save's tick", async () => {
      vi.useFakeTimers();
      try {
        ws.reader = undefined; // no editor yet
        await ws.open("mixed.conf", true);
        await vi.advanceTimersByTimeAsync(1500); // the debounced save finds no text
        expect(api.saveNote).not.toHaveBeenCalled();
        ws.reader = () => ws.state.value.open?.shown ?? ""; // the editor mounted
        await vi.advanceTimersByTimeAsync(1500);
        expect(api.saveNote).toHaveBeenCalledWith("mixed.conf", `${bomChar}a\nb\nc\nd`, '"e1"');
      } finally {
        vi.useRealTimers();
      }
    });

    it("a file changed since it was read gets the conflict view, not an overwrite", async () => {
      vi.mocked(api.saveNote).mockResolvedValueOnce({ kind: "conflict", etag: '"e9"', content: "other\n" });
      await ws.open("mixed.conf", true);
      ws.reader = () => ws.state.value.open?.shown ?? "";
      expect(await ws.saveOpen()).toBe(false);
      expect(ws.state.value.open?.doc.state.value.status).toBe("conflict");
    });

    it("a draft of the converted file restores after converting again", async () => {
      await ws.open("mixed.conf", true);
      shown = `${ws.state.value.open?.shown ?? ""}\ne`;
      ws.reader = () => shown;
      ws.state.value.open?.doc.edit();
      await ws.state.value.open?.doc.writeDraft();
      const [draft] = await backend.all();
      if (!draft) throw new Error("no draft written");

      await ws.open("mixed.conf", true);
      const put = vi.fn((t: string) => (shown = t));
      await ws.recoverDraft({ ...draft, esid: "other-tab" }, put);
      expect(put).toHaveBeenCalledWith("a\nb\nc\nd\ne");
      expect(await ws.saveOpen()).toBe(true);
      expect(api.saveNote).toHaveBeenLastCalledWith("mixed.conf", `${bomChar}a\nb\nc\nd\ne`, expect.any(String));
    });
  });

  /** Opens app.yaml, edits it, and has its save answered with conflict. */
  async function conflictWith(conflict: api.SaveResult & { kind: "conflict" }): Promise<void> {
    await openAndAttach("app.yaml");
    vi.mocked(api.saveNote).mockResolvedValueOnce(conflict);
    shown = "a: 1\n";
    ws.state.value.open?.doc.edit();
    await ws.saveOpen();
    expect(ws.state.value.open?.doc.state.value.status).toBe("conflict");
  }

  it("taking the other version in a conflict takes its line endings too", async () => {
    await conflictWith({ kind: "conflict", etag: '"e3"', content: "a: 9\n" });
    expect(ws.takeTheirs((t) => (shown = t))).toBe(true);
    expect(shown).toBe("a: 9\n");
    shown += "b: 1\n";
    ws.state.value.open?.doc.edit();
    await ws.saveOpen();
    expect(api.saveNote).toHaveBeenLastCalledWith("app.yaml", "a: 9\nb: 1\n", '"e3"');
  });

  it("an other version with mixed line endings isn't taken as it is: the conflict and the edits stay", async () => {
    await conflictWith({ kind: "conflict", etag: '"e3"', content: "a\r\nb\n" });
    const put = vi.fn();
    expect(ws.takeTheirs(put)).toBe(false);
    expect(put).not.toHaveBeenCalled();
    expect(ws.state.value.open?.doc.state.value.status).toBe("conflict");
  });

  it("…and taken converted to LF at the user's word, as a save over the other version's ETag", async () => {
    await conflictWith({ kind: "conflict", etag: '"e3"', content: "a\r\nb\n" });
    expect(ws.takeTheirs((t) => (shown = t), true)).toBe(true);
    expect(shown).toBe("a\nb\n");
    expect(ws.state.value.open?.doc.dirty).toBe(true); // the conversion is an edit
    expect(await ws.saveOpen()).toBe(true);
    expect(api.saveNote).toHaveBeenLastCalledWith("app.yaml", "a\nb\n", '"e3"');
  });

  it("an other version that isn't text is never taken; my text saves as a new file, the other stays", async () => {
    await conflictWith({ kind: "conflict", etag: '"e3"', content: "", binary: true });
    expect(ws.takeTheirs(vi.fn(), true)).toBe(false);
    vi.mocked(api.saveNote).mockClear();
    await ws.saveAsNew();
    expect(api.createNote).toHaveBeenCalledWith("app-recovered.yaml", `${bomChar}a: 1\r\n`);
    expect(api.saveNote).not.toHaveBeenCalled(); // the other version, on disk, is untouched
    expect(ws.state.value.open?.doc.path).toBe("app-recovered.yaml");
  });

  it("saving the text as a new file keeps its format", async () => {
    await openAndAttach("app.yaml");
    await ws.saveAsNew();
    expect(api.createNote).toHaveBeenCalledWith("app-recovered.yaml", `${bomChar}a: 1\r\nb: 2\r\n`);
  });
});
