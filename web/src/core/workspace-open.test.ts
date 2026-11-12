import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "./api";
import { Drafts, MemoryDraftBackend } from "./drafts";
import { BinaryError, TooLargeError } from "./textfile";
import { Workspace } from "./workspace";

vi.mock("./api", async (actual) => ({
  ...(await actual<typeof import("./api")>()),
  listFolder: vi.fn(async () => []),
  readNote: vi.fn(async (path: string) => ({ content: `# ${path}\n`, etag: `"${path}"` })),
  saveNote: vi.fn(async () => ({ kind: "saved", etag: '"e2"' })),
}));

/** A read the test answers. */
function slowRead(): { resolve: (n: api.Note) => void; reject: (e: unknown) => void } {
  let out: { resolve: (n: api.Note) => void; reject: (e: unknown) => void } | undefined;
  const promise = new Promise<api.Note>((resolve, reject) => (out = { resolve, reject }));
  vi.mocked(api.readNote).mockImplementationOnce(() => promise);
  if (!out) throw new Error("the promise's executor runs at once");
  return out;
}

/** The next save, answered by the test. */
function slowSave(): { resolve: (r: api.SaveResult) => void } {
  let out: { resolve: (r: api.SaveResult) => void } | undefined;
  const promise = new Promise<api.SaveResult>((resolve) => (out = { resolve }));
  vi.mocked(api.saveNote).mockImplementationOnce(() => promise);
  if (!out) throw new Error("the promise's executor runs at once");
  return out;
}

// #134: a file that can't be opened leaves the open note as it was: open, editable and saving.
describe("Workspace: a file that can't be opened keeps the open note", () => {
  let ws: Workspace;
  let backend: MemoryDraftBackend;
  /** The editor's text: the test types by changing it. */
  let text = "";

  beforeEach(async () => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    backend = new MemoryDraftBackend();
    ws = new Workspace();
    ws.useDrafts(await Drafts.open(backend, "me", () => "moved"));
    await ws.open("a.md");
    text = ws.state.value.open?.shown ?? "";
    ws.reader = () => text;
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  function type(more: string): void {
    text += more;
    ws.state.value.open?.doc.edit();
  }

  const refusals: { name: string; refuse: () => void; error: string; convert?: string }[] = [
    {
      name: "a binary file",
      refuse: () => vi.mocked(api.readNote).mockRejectedValueOnce(new BinaryError()),
      error: "PostelMD opens only UTF-8 text files, and logo.png isn't one.",
    },
    {
      name: "a file over 5 MiB",
      refuse: () => vi.mocked(api.readNote).mockRejectedValueOnce(new TooLargeError()),
      error: "PostelMD can't open logo.png: it's larger than 5 MiB, the most PostelMD edits.",
    },
    {
      name: "a file with mixed line endings",
      refuse: () => vi.mocked(api.readNote).mockResolvedValueOnce({ content: "a\r\nb\n", etag: '"m"' }),
      error:
        "PostelMD can't open logo.png as it is: it mixes Windows (CRLF) and Unix (LF) line endings. Convert them to LF to edit it here. This saves the file with LF line endings.",
      convert: "logo.png",
    },
    {
      name: "a file the server can't read",
      refuse: () => vi.mocked(api.readNote).mockRejectedValueOnce(new Error("can't reach the server")),
      error: "Couldn't open logo.png: can't reach the server",
    },
  ];

  for (const r of refusals) {
    it(`${r.name}: the open note stays open, unsaved edit included, and keeps saving`, async () => {
      const before = ws.state.value.open;
      const dispose = before && vi.spyOn(before.doc, "dispose");
      type("unsaved\n");
      r.refuse();
      await ws.open("logo.png");

      expect(ws.state.value.open).toBe(before); // the same session: nothing remounts
      expect(dispose).not.toHaveBeenCalled();
      expect(ws.state.value.error).toBe(r.error);
      expect(ws.state.value.convert).toBe(r.convert);

      type("more\n");
      await vi.advanceTimersByTimeAsync(1000);
      expect(api.saveNote).toHaveBeenLastCalledWith("a.md", "# a.md\nunsaved\nmore\n", '"a.md"');
      expect(ws.state.value.open?.doc.dirty).toBe(false);
    });
  }

  it("edits typed while the next file loads are saved before it opens", async () => {
    const read = slowRead();
    const opening = ws.open("b.md");
    await vi.advanceTimersByTimeAsync(0);
    type("typed meanwhile\n");
    read.resolve({ content: "# b.md\n", etag: '"b.md"' });
    await opening;

    expect(api.saveNote).toHaveBeenCalledWith("a.md", "# a.md\ntyped meanwhile\n", '"a.md"');
    expect(ws.state.value.open?.doc.path).toBe("b.md");
  });

  it("edits typed while the next file loads that can't be saved keep the open note, and say why", async () => {
    const before = ws.state.value.open;
    const read = slowRead();
    const opening = ws.open("b.md");
    await vi.advanceTimersByTimeAsync(0);
    type("typed meanwhile\n");
    vi.mocked(api.saveNote).mockResolvedValueOnce({ kind: "conflict", etag: '"x"', content: "theirs" });
    read.resolve({ content: "# b.md\n", etag: '"b.md"' });
    await opening;

    expect(ws.state.value.open).toBe(before);
    expect(before?.doc.state.value.status).toBe("conflict");
    expect(ws.state.value.error).toBe(
      "Couldn't open b.md: your latest edits to a.md aren't saved yet, so it stays open. Open b.md again once they save.",
    );
  });

  it("typing again while those edits save still opens the next file, with every edit saved", async () => {
    const save = slowSave();
    const read = slowRead();
    const opening = ws.open("b.md");
    await vi.advanceTimersByTimeAsync(0);
    type("one\n");
    read.resolve({ content: "# b.md\n", etag: '"b.md"' });
    await vi.advanceTimersByTimeAsync(0); // the flush's first save is in flight
    type("two\n");
    save.resolve({ kind: "saved", etag: '"e1"' });
    await opening;

    expect(api.saveNote).toHaveBeenLastCalledWith("a.md", "# a.md\none\ntwo\n", '"e1"');
    expect(ws.state.value.open?.doc.path).toBe("b.md");
    expect(ws.state.value.error).toBeUndefined();
  });

  it("a note closed while its edits save, before the next file opens, stays closed", async () => {
    const save = slowSave();
    const read = slowRead();
    const opening = ws.open("b.md");
    await vi.advanceTimersByTimeAsync(0);
    type("one\n");
    read.resolve({ content: "# b.md\n", etag: '"b.md"' });
    await vi.advanceTimersByTimeAsync(0);
    ws.close(); // a logout, or a trash of the note
    save.resolve({ kind: "saved", etag: '"e1"' });
    await opening;
    expect(ws.state.value).toEqual({}); // no b.md, and no error about a note no longer open
  });

  it("another note opened while those edits save stays open", async () => {
    const save = slowSave();
    const read = slowRead();
    const opening = ws.open("b.md");
    await vi.advanceTimersByTimeAsync(0);
    type("one\n");
    read.resolve({ content: "# b.md\n", etag: '"b.md"' });
    await vi.advanceTimersByTimeAsync(0);
    await ws.open("c.md");
    const c = ws.state.value.open;
    save.resolve({ kind: "saved", etag: '"e1"' });
    await opening;
    expect(c?.doc.path).toBe("c.md");
    expect(ws.state.value).toEqual({ open: c }); // and no error from b.md's open, cancelled
  });

  it("opening the open note again keeps its session: an edit typed meanwhile stays, and saves over its own last save", async () => {
    const before = ws.state.value.open;
    type("one\n");
    await ws.saveOpen(); // saved as "e2"
    const opening = ws.open("a.md");
    type("two\n");
    await opening;
    expect(ws.state.value.open).toBe(before);
    expect(api.readNote).toHaveBeenCalledTimes(1); // a.md's first open only: a read now would be stale
    await vi.advanceTimersByTimeAsync(1000);
    expect(api.saveNote).toHaveBeenLastCalledWith("a.md", "# a.md\none\ntwo\n", '"e2"');
  });

  it("converting the open note reads it again if a save landed while it loaded", async () => {
    const read = slowRead();
    const opening = ws.open("a.md", true);
    await vi.advanceTimersByTimeAsync(0);
    type("one\n");
    read.resolve({ content: "# a.md\n", etag: '"a.md"' }); // read before the save below
    vi.mocked(api.readNote).mockResolvedValueOnce({ content: "# a.md\none\n", etag: '"e2"' });
    await opening;
    expect(api.saveNote).toHaveBeenCalledWith("a.md", "# a.md\none\n", '"a.md"');
    expect(ws.state.value.open?.doc.etag).toBe('"e2"');
    expect(ws.state.value.open?.shown).toBe("# a.md\none\n");
  });

  it("a file refused again is announced again: the error goes while it loads", async () => {
    vi.mocked(api.readNote).mockRejectedValueOnce(new BinaryError());
    await ws.open("logo.png");
    const first = ws.state.value.error;
    expect(first).toBeDefined();
    const read = slowRead();
    const again = ws.open("logo.png");
    await vi.advanceTimersByTimeAsync(0);
    expect(ws.state.value.error).toBeUndefined();
    expect(ws.state.value.open?.doc.path).toBe("a.md");
    read.reject(new BinaryError());
    await again;
    expect(ws.state.value.error).toBe(first);
  });

  it("a file refused for its line endings, then a binary one: the offer to convert goes", async () => {
    vi.mocked(api.readNote).mockResolvedValueOnce({ content: "a\r\nb\n", etag: '"m"' });
    await ws.open("mixed.conf");
    expect(ws.state.value.convert).toBe("mixed.conf");
    vi.mocked(api.readNote).mockRejectedValueOnce(new BinaryError());
    await ws.open("logo.png");
    expect(ws.state.value.convert).toBeUndefined();
    expect(ws.state.value.error).toBe("PostelMD opens only UTF-8 text files, and logo.png isn't one.");
  });

  it("a refused file that answers after another note opened changes nothing", async () => {
    const read = slowRead();
    const refused = ws.open("logo.png");
    await ws.open("b.md");
    const b = ws.state.value.open;
    read.reject(new BinaryError());
    await refused;
    expect(ws.state.value.open).toBe(b);
    expect(ws.state.value.error).toBeUndefined();
  });

  it("a note left with edits it couldn't save still opens the next file: the user agreed to leave them", async () => {
    vi.mocked(api.saveNote).mockResolvedValueOnce({ kind: "conflict", etag: '"x"', content: "theirs" });
    type("mine\n");
    await ws.saveOpen();
    expect(ws.state.value.open?.doc.state.value.status).toBe("conflict");
    await ws.open("b.md");
    expect(ws.state.value.open?.doc.path).toBe("b.md");
  });
});
