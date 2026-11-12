import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "./api";
import { destination, folderChoices, folderOf, intoFolder, moveInto } from "./move-into";
import { Tree } from "./tree";
import { Workspace } from "./workspace";

vi.mock("./api", async (actual) => ({
  ...(await actual<typeof import("./api")>()),
  // The vault: ops (with db in it), archive (with ops in it), and a note at the root.
  listFolder: vi.fn(async (path: string) => {
    const folders: Record<string, string[]> = { "": ["ops", "archive"], ops: ["db"], archive: ["ops"] };
    const entries = (folders[path] ?? []).map((name) => ({ name, kind: "folder" as const }));
    return path === "" ? [...entries, { name: "a.md", kind: "file" as const }] : entries;
  }),
  readNote: vi.fn(async (path: string) => ({ content: `# ${path}`, etag: '"e1"' })),
  saveNote: vi.fn(async () => ({ kind: "saved", etag: '"e2"' })),
  move: vi.fn(async () => {}),
}));

describe("destination", () => {
  it("puts the item into the folder under its own name", () => {
    expect(destination("a.md", "file", "ops")).toEqual({ to: "ops/a.md" });
    expect(destination("ops/db/x.md", "file", "")).toEqual({ to: "x.md" });
    expect(destination("ops/db", "folder", "archive")).toEqual({ to: "archive/db" });
    expect(destination("ops", "folder", "ops-old")).toEqual({ to: "ops-old/ops" }); // a prefix isn't a subfolder
  });

  it("does nothing for the item's own folder", () => {
    expect(destination("ops/a.md", "file", "ops")).toBeUndefined();
    expect(destination("a.md", "file", "")).toBeUndefined();
    expect(destination("ops/db", "folder", "ops")).toBeUndefined();
  });

  it("refuses a folder into itself or one of its subfolders", () => {
    expect(destination("ops", "folder", "ops")).toEqual({ refused: "Can't move the folder ops into itself." });
    expect(destination("ops", "folder", "ops/db/deep")).toEqual({ refused: "Can't move the folder ops into ops/db/deep, which is inside it." });
  });
});

describe("folderChoices", () => {
  it("lists the folders the tree knows that the item can be in, its own included, the vault first", async () => {
    const tree = new Tree(api.listFolder);
    await tree.load("");
    await tree.load("ops");
    const s = tree.state.value;
    expect(folderChoices(s, "a.md", "file")).toEqual(["", "archive", "ops", "ops/db"]);
    expect(folderChoices(s, "ops/db/x.md", "file")).toEqual(["", "archive", "ops", "ops/db"]);
    expect(folderChoices(s, "ops", "folder")).toEqual(["", "archive"]); // not itself, not ops/db
    expect(folderChoices(s, "deep/er/x.md", "file")).toContain("deep/er"); // its own, even if not loaded
  });
});

describe("folderOf", () => {
  it("is the folder the typed path puts the item in, as the move would read it", () => {
    expect(folderOf("a.md", "ops/x.md", "file")).toBe("ops");
    expect(folderOf("ops/a.md", " /archive/old/b ", "file")).toBe("archive/old"); // trimmed, .md added
    expect(folderOf("ops/a.md", "b.md", "file")).toBe("");
    expect(folderOf("ops", "archive/ops/", "folder")).toBe("archive");
    expect(folderOf("ops/a.md", "", "file")).toBe("");
  });
});

describe("intoFolder", () => {
  it("keeps the typed name and puts it in the chosen folder", () => {
    expect(intoFolder("ops/a.md", "archive")).toBe("archive/a.md");
    expect(intoFolder(" ops/renamed.md/ ", "")).toBe("renamed.md");
    expect(intoFolder("a.md", "ops/db")).toBe("ops/db/a.md");
    expect(intoFolder("", "ops")).toBe("ops/");
  });
});

describe("moveInto", () => {
  let ws: Workspace;

  beforeEach(async () => {
    vi.clearAllMocks();
    ws = new Workspace();
    await ws.tree.load("");
  });

  it("moves through the workspace's move and announces it in the tree's status", async () => {
    expect(await moveInto(ws, "a.md", "file", "ops")).toBe("ops/a.md");
    expect(api.move).toHaveBeenCalledWith("a.md", "ops/a.md");
    expect(ws.tree.state.value.status).toBe("Moved a.md to ops/a.md.");
  });

  it("keeps the open note open, with its unsaved edit, at its new path", async () => {
    await ws.open("a.md");
    ws.reader = () => "edited";
    ws.state.value.open?.doc.edit();
    expect(await moveInto(ws, "a.md", "file", "ops")).toBe("ops/a.md");
    expect(api.saveNote).toHaveBeenCalledWith("a.md", "edited", expect.anything()); // saved before the move
    expect(ws.state.value.open?.doc.path).toBe("ops/a.md");
  });

  it("does nothing, and says nothing, for the item's own folder", async () => {
    ws.tree.announce("old news");
    expect(await moveInto(ws, "ops/db", "folder", "ops")).toBeUndefined();
    expect(api.move).not.toHaveBeenCalled();
    expect(ws.tree.state.value.status).toBe("");
    expect(ws.state.value.error).toBeUndefined();
  });

  it("refuses a folder into its subfolder without asking the server", async () => {
    expect(await moveInto(ws, "ops", "folder", "ops/db")).toBeUndefined();
    expect(api.move).not.toHaveBeenCalled();
    expect(ws.tree.state.value.status).toBe("Can't move the folder ops into ops/db, which is inside it.");
  });

  it("passes the server's refusal through as the workspace error, and announces nothing", async () => {
    vi.mocked(api.move).mockRejectedValueOnce(new api.ApiError(409, "already exists"));
    expect(await moveInto(ws, "ops", "folder", "archive")).toBeUndefined();
    expect(ws.state.value.error).toBe("Couldn't move ops: archive/ops already exists");
    expect(ws.tree.state.value.status).toBe("");
  });
});
