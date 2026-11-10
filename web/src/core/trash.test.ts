import { describe, expect, it, vi } from "vitest";
import { ApiError } from "./api";
import { restoreTarget, Trash, type TrashDeps } from "./trash";
import type { TrashItem } from "./trash-api";

const note: TrashItem = { id: "20261110T143005.000000000Z", name: "runbook.md", kind: "file", deletedAt: "2026-11-10T14:30:05Z", size: 4 };
const folder: TrashItem = { id: "20261110T143006.000000000Z", name: "ops", kind: "folder", deletedAt: "2026-11-10T14:30:06Z" };

function deps(over: Partial<TrashDeps> = {}): TrashDeps {
  return {
    list: async () => ({ items: [folder, note], truncated: false }),
    restore: async (_id, f, n) => (f === "" ? n : `${f}/${n}`),
    changed: vi.fn(async () => {}),
    reveal: vi.fn(async () => {}),
    ...over,
  };
}

describe("restoreTarget", () => {
  it.each([
    ["", "runbook.md", "file", { folder: "", name: "runbook.md" }],
    ["  ", "runbook.md", "file", { folder: "", name: "runbook.md" }],
    ["runbook.md", "runbook.md", "file", { folder: "", name: "runbook.md" }],
    ["ops/", "runbook.md", "file", { folder: "ops", name: "runbook.md" }],
    ["/ops/db/", "runbook.md", "file", { folder: "ops/db", name: "runbook.md" }],
    ["ops/old-runbook", "runbook.md", "file", { folder: "ops", name: "old-runbook.md" }],
    ["ops/notes.txt", "runbook.md", "file", { folder: "ops", name: "notes.txt" }],
    ["archive/ops", "ops", "folder", { folder: "archive", name: "ops" }],
  ] as const)("%j for %s (%s) is %j", (typed, name, kind, want) => {
    expect(restoreTarget(typed, name, kind)).toEqual(want);
  });
});

describe("Trash", () => {
  it("lists the items", async () => {
    const t = new Trash(deps());
    await t.load();
    expect(t.state.value.items?.map((i) => i.name)).toEqual(["ops", "runbook.md"]);
    expect(t.state.value.error).toBeUndefined();
  });

  it("says why it couldn't list", async () => {
    const t = new Trash(deps({ list: () => Promise.reject(new ApiError(500, "the server had an error; its log says more")) }));
    await t.load();
    expect(t.state.value.error).toBe("Couldn't list the trash: the server had an error; its log says more");
  });

  it("restores where asked, drops the item from the list and shows it in the tree", async () => {
    const d = deps();
    const restore = vi.spyOn(d, "restore");
    const t = new Trash(d);
    await t.load();
    expect(await t.restore(note, "ops/")).toBe("ops/runbook.md");
    expect(restore).toHaveBeenCalledWith(note.id, "ops", "runbook.md");
    expect(t.state.value.items).toEqual([folder]);
    expect(d.changed).toHaveBeenCalledWith("ops/runbook.md");
    expect(d.reveal).toHaveBeenCalledWith("ops/runbook.md");
  });

  it("explains a name clash plainly and keeps the item", async () => {
    const d = deps();
    vi.spyOn(d, "restore").mockRejectedValueOnce(new ApiError(409, "something with that name is already there; choose another name"));
    const t = new Trash(d);
    await t.load();
    expect(await t.restore(note, "")).toBeUndefined();
    expect(t.state.value.error).toBe("Couldn't restore runbook.md: the vault already has something named runbook.md. Type another name.");
    expect(t.state.value.items).toHaveLength(2);
    expect(await t.restore(folder, "archive/")).toBe("archive/ops");
    expect(t.state.value.error).toBeUndefined(); // a later restore clears it
  });

  // After any failure the list is read again: the item may be gone (410), or restored even
  // though the answer was an error (a sync failed after the rename; ADR-0024).
  it.each([
    [410, "that item is no longer in the trash"],
    [500, "the server had an error; its log says more"],
    [404, "that folder doesn't exist"],
  ])("reloads the list after a %i", async (status, why) => {
    const list = vi.fn(async () => ({ items: [folder, note], truncated: false }));
    const t = new Trash(deps({ list, restore: () => Promise.reject(new ApiError(status, why)) }));
    await t.load();
    list.mockResolvedValueOnce({ items: [folder], truncated: false });
    expect(await t.restore(note, "")).toBeUndefined();
    expect(list).toHaveBeenCalledTimes(2);
    expect(t.state.value.items).toEqual([folder]);
    expect(t.state.value.error).toBe(`Couldn't restore runbook.md: ${why}`);
  });
});
