import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "./api";
import { Workspace } from "./workspace";

vi.mock("./api", async (actual) => ({
  ...(await actual<typeof import("./api")>()),
  listFolder: vi.fn(async () => []),
  readNote: vi.fn(async (path: string) => ({ content: `# ${path}`, etag: '"e1"' })),
  saveNote: vi.fn(async () => ({ kind: "saved", etag: '"e2"' })),
  move: vi.fn(async () => {}),
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
