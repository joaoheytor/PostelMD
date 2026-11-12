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
    expect(ws.state.value.error).toBe("Didn't open b.md: edits to a.md made while it loaded aren't saved.");
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
