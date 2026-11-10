import { describe, expect, it } from "vitest";
import { type Draft, Drafts, MemoryDraftBackend } from "./drafts";

const draft = (over: Partial<Draft>): Draft => ({ path: "a.md", esid: "me", text: "t", base: '"e0"', gen: 1, savedAt: 0, ...over });

async function setup(existing: Draft[] = []) {
  const backend = new MemoryDraftBackend();
  for (const d of existing) await backend.put(d);
  let n = 0;
  const drafts = await Drafts.open(backend, "me", () => `moved-${++n}`);
  return { backend, drafts };
}

describe("Drafts (ADR-0003 §7)", () => {
  it("moves drafts left under this tab's esid by an earlier page load aside, so they are offered, never overwritten", async () => {
    const { backend, drafts } = await setup([draft({ text: "before the reload", gen: 7 }), draft({ esid: "other", path: "b.md" })]);
    expect((await backend.all()).map((d) => [d.path, d.esid])).toEqual([
      ["b.md", "other"],
      ["a.md", "moved-1"],
    ]);
    expect((await drafts.offered("a.md")).map((d) => d.text)).toEqual(["before the reload"]);
  });

  it("a save clears only this tab's draft of the same or an older generation", async () => {
    const { backend, drafts } = await setup([draft({ esid: "other", text: "another tab" })]);
    const mine = drafts.forNote("a.md");
    mine.write("v3", '"e0"', 3);
    mine.saved(2); // an older save: the draft is newer and stays
    await drafts.idle();
    expect((await backend.all()).map((d) => [d.esid, d.text])).toEqual([
      ["other", "another tab"],
      ["me", "v3"],
    ]);
    mine.saved(3);
    await drafts.idle();
    expect((await backend.all()).map((d) => d.esid)).toEqual(["other"]);
  });

  it("applies a tab's writes and clears in order", async () => {
    const { backend, drafts } = await setup();
    const mine = drafts.forNote("a.md");
    mine.write("v1", '"e0"', 1);
    mine.saved(1); // sent right after the write: it must not run first and leave v1 behind
    await drafts.idle();
    expect(await backend.all()).toEqual([]);
  });

  it("lists the drafts of other tabs and earlier loads, for a note and for the vault", async () => {
    const { drafts } = await setup([draft({ esid: "x", path: "b.md", savedAt: 2 }), draft({ esid: "y", path: "a.md", savedAt: 1 })]);
    drafts.forNote("a.md").write("mine", '"e0"', 1);
    await drafts.idle();
    expect((await drafts.offered("a.md")).map((d) => d.esid)).toEqual(["y"]);
    expect((await drafts.offered()).map((d) => d.esid)).toEqual(["x", "y"]); // newest first
  });

  it("discards only the draft the user picked", async () => {
    const { backend, drafts } = await setup([draft({ esid: "x" }), draft({ esid: "y" })]);
    await drafts.discard(draft({ esid: "x" }));
    expect((await backend.all()).map((d) => d.esid)).toEqual(["y"]);
  });
});
