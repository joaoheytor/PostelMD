import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "./api";
import { localDate } from "./templates";
import { Workspace } from "./workspace";

vi.mock("./api", async (actual) => ({
  ...(await actual<typeof import("./api")>()),
  listFolder: vi.fn(async () => []),
  readNote: vi.fn(async (path: string) => ({ content: `# ${path}`, etag: '"e1"' })),
  saveNote: vi.fn(async () => ({ kind: "saved", etag: '"e2"' })),
  move: vi.fn(async () => {}),
  restore: vi.fn(async () => '"e3"'),
  createNote: vi.fn(async () => '"e4"'),
}));

/** Every request in order, so a test can assert that the save came before the move. */
const log: string[] = [];

describe("Workspace.move", () => {
  let ws: Workspace;

  beforeEach(async () => {
    vi.clearAllMocks();
    log.length = 0;
    vi.mocked(api.saveNote).mockImplementation(async (path) => {
      log.push(`save ${path}`);
      return { kind: "saved", etag: '"e2"' };
    });
    vi.mocked(api.move).mockImplementation(async (from, to) => {
      log.push(`move ${from} ${to}`);
    });
    ws = new Workspace();
    await ws.open("ops/a.md");
  });

  it("saves the open note first, then reopens it at its new path", async () => {
    ws.state.value.open?.doc.edit();
    expect(await ws.move("ops", "archive/ops")).toBe(true);
    expect(log).toEqual(["save ops/a.md", "move ops archive/ops"]);
    expect(ws.state.value.open?.doc.path).toBe("archive/ops/a.md");
  });

  it("doesn't move anything while the open note can't be saved", async () => {
    vi.mocked(api.saveNote).mockResolvedValue({ kind: "gone" });
    ws.state.value.open?.doc.edit();
    expect(await ws.move("ops/a.md", "b.md")).toBe(false);
    expect(api.move).not.toHaveBeenCalled();
    expect(ws.state.value.open?.doc.path).toBe("ops/a.md");
    expect(ws.state.value.error).toContain("Save or resolve ops/a.md");
  });

  it("reports an existing destination and keeps the note open where it was", async () => {
    vi.mocked(api.move).mockRejectedValue(new api.ApiError(409, "already exists"));
    expect(await ws.move("ops/a.md", "b.md")).toBe(false);
    expect(ws.state.value.open?.doc.path).toBe("ops/a.md");
    expect(ws.state.value.error).toBe("Couldn't move ops/a.md: b.md already exists");
  });

  it("leaves an open note outside the moved folder alone", async () => {
    const doc = ws.state.value.open?.doc;
    expect(await ws.move("ops-old", "archive")).toBe(true);
    expect(ws.state.value.open?.doc).toBe(doc);
  });
});

describe("Workspace.restore", () => {
  let ws: Workspace;

  beforeEach(async () => {
    vi.clearAllMocks();
    log.length = 0;
    vi.mocked(api.saveNote).mockImplementation(async (path) => {
      log.push(`save ${path}`);
      return { kind: "saved", etag: '"e2"' };
    });
    vi.mocked(api.restore).mockImplementation(async (path, commit, base) => {
      log.push(`restore ${path} ${commit} ${base}`);
      return '"e3"';
    });
    ws = new Workspace();
    await ws.open("a.md");
  });

  it("saves first, restores over the saved version, and reopens the note", async () => {
    ws.state.value.open?.doc.edit();
    vi.mocked(api.readNote).mockResolvedValueOnce({ content: "old text", etag: '"e3"' });
    expect(await ws.restore("c1")).toBe(true);
    expect(log).toEqual(["save a.md", 'restore a.md c1 "e2"']);
    expect(ws.state.value.open?.initial).toBe("old text");
  });

  it("doesn't restore while the open note can't be saved", async () => {
    vi.mocked(api.saveNote).mockResolvedValue({ kind: "error", message: "busy", retryAfterMs: 0, retry: false });
    ws.state.value.open?.doc.edit();
    expect(await ws.restore("c1")).toBe(false);
    expect(api.restore).not.toHaveBeenCalled();
    expect(ws.state.value.error).toContain("Save or resolve a.md");
  });

  it("says nothing was replaced when the note changed in the meantime", async () => {
    vi.mocked(api.restore).mockRejectedValue(new api.ApiError(412, "Precondition Failed"));
    expect(await ws.restore("c1")).toBe(false);
    expect(ws.state.value.open?.doc.path).toBe("a.md");
    expect(ws.state.value.error).toBe("Couldn't restore a.md: it changed in the meantime, so nothing was replaced");
  });
});

describe("Workspace.createNote", () => {
  beforeEach(() => vi.clearAllMocks());

  it("starts a blank note with its name as the heading", async () => {
    await new Workspace().createNote("ops", "Disk full");
    expect(api.createNote).toHaveBeenCalledWith("ops/Disk full.md", "# Disk full\n\n");
  });

  it("starts from a template: the name, today's date and the values filled in", async () => {
    const template = { name: "T", body: "# {{title}}\n{{date}} {{owner}} {{service}}\n" };
    await new Workspace().createNote("", "db.md", { template, values: { owner: "sre", service: "", title: "ignored" } });
    expect(api.createNote).toHaveBeenCalledWith("db.md", `# db\n${localDate(new Date())} sre {{service}}\n`);
  });
});
