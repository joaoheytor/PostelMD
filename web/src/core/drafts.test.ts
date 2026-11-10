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
      ["me.1", "v3"],
    ]);
    mine.saved(3);
    await drafts.idle();
    expect((await backend.all()).map((d) => d.esid)).toEqual(["other"]);
  });

  it("a closed session's late save never clears the draft of the note reopened since", async () => {
    const { backend, drafts } = await setup();
    const closed = drafts.forNote("a.md");
    closed.write("old", '"e0"', 5); // closed mid-save at gen 5
    const reopened = drafts.forNote("a.md"); // its generations start again from 0
    reopened.write("new edit", '"e1"', 1);
    closed.saved(5); // the old save lands: it covers "old", not the reopened session's edit
    await drafts.idle();
    expect((await backend.all()).map((d) => d.text)).toEqual(["new edit"]);
  });

  it("a closed session's draft is offered, and a session opened on the note since never writes over it (#105)", async () => {
    const { backend, drafts } = await setup();
    const closed = drafts.forNote("a.md");
    closed.write("unsaved", '"e0"', 5);
    closed.close(); // its save failed: the draft is the edit's only copy
    expect((await drafts.offered("a.md")).map((d) => d.text)).toEqual(["unsaved"]);
    const reopened = drafts.forNote("a.md");
    reopened.write("new edit", '"e0"', 1);
    reopened.saved(1);
    await drafts.idle();
    expect((await backend.all()).map((d) => d.text)).toEqual(["unsaved"]);
    expect((await drafts.offered()).map((d) => d.text)).toEqual(["unsaved"]);
  });

  it("an open session's own draft is never offered", async () => {
    const { drafts } = await setup();
    drafts.forNote("a.md").write("mine", '"e0"', 1);
    expect(await drafts.offered("a.md")).toEqual([]);
  });

  it("a reload moves every earlier session's draft of this tab aside", async () => {
    const backend = new MemoryDraftBackend();
    const before = await Drafts.open(backend, "me", () => "unused");
    before.forNote("a.md").write("first", '"e0"', 1);
    before.forNote("a.md").write("second", '"e0"', 1);
    await before.idle();
    let n = 0;
    const after = await Drafts.open(backend, "me", () => `moved-${++n}`);
    expect((await after.offered("a.md")).map((d) => d.esid).sort()).toEqual(["moved-1", "moved-2"]);
    after.forNote("a.md").write("third", '"e0"', 1);
    await after.idle();
    expect((await backend.all()).map((d) => d.text).sort()).toEqual(["first", "second", "third"]);
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
