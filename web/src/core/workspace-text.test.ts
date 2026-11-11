import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "./api";
import { Drafts, MemoryDraftBackend } from "./drafts";
import { bomChar, NotTextError } from "./textfile";
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
    vi.mocked(api.readNote).mockRejectedValueOnce(new NotTextError("it isn't UTF-8 text"));
    await ws.open("logo.png");
    expect(ws.state.value.open).toBeUndefined();
    expect(ws.state.value.error).toBe("PostelMD can't open logo.png: it isn't UTF-8 text.");
  });

  it("a file with mixed line endings doesn't open: a save would change them", async () => {
    vi.mocked(api.readNote).mockResolvedValueOnce({ content: "a\r\nb\n", etag: '"e1"' });
    await ws.open("mixed.conf");
    expect(ws.state.value.open).toBeUndefined();
    expect(ws.state.value.error).toBe("PostelMD can't open mixed.conf: it mixes CRLF and LF line endings, which the editor would change.");
  });

  it("taking the other version in a conflict takes its line endings too", async () => {
    await openAndAttach("app.yaml");
    vi.mocked(api.saveNote).mockResolvedValueOnce({ kind: "conflict", etag: '"e3"', content: "a: 9\n" });
    shown = "a: 1\n";
    ws.state.value.open?.doc.edit();
    await ws.saveOpen();
    expect(ws.state.value.open?.doc.state.value.status).toBe("conflict");

    shown = ws.takeTheirs() ?? "";
    expect(shown).toBe("a: 9\n");
    shown += "b: 1\n";
    ws.state.value.open?.doc.edit();
    await ws.saveOpen();
    expect(api.saveNote).toHaveBeenLastCalledWith("app.yaml", "a: 9\nb: 1\n", '"e3"');
  });

  it("an other version the editor can't hold isn't taken: the conflict and the edits stay", async () => {
    await openAndAttach("app.yaml");
    vi.mocked(api.saveNote).mockResolvedValueOnce({ kind: "conflict", etag: '"e3"', content: "a\r\nb\n" });
    shown = "a: 1\n";
    ws.state.value.open?.doc.edit();
    await ws.saveOpen();

    expect(ws.takeTheirs()).toBeUndefined();
    expect(ws.state.value.open?.doc.state.value.status).toBe("conflict");
    expect(ws.state.value.error).toBe("Couldn't use the other version: it mixes CRLF and LF line endings, which the editor would change.");
  });

  it("saving the text as a new file keeps its format", async () => {
    await openAndAttach("app.yaml");
    await ws.saveAsNew();
    expect(api.createNote).toHaveBeenCalledWith("app-recovered.yaml", `${bomChar}a: 1\r\nb: 2\r\n`);
  });
});
