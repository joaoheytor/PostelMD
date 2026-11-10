import { describe, expect, it, vi } from "vitest";
import type { Version } from "./api";
import { History } from "./history";

const v = (commit: string, deleted = false): Version => ({ commit, message: commit, author: "me", when: "2026-11-10T00:00:00Z", deleted });

describe("History", () => {
  it("lists the versions and loads the selected one's text", async () => {
    const version = vi.fn(async (commit: string) => `text of ${commit}`);
    const h = new History("a.md", { history: async () => [v("c2"), v("c1")], version });
    await h.load();
    expect(h.state.value.versions?.map((x) => x.commit)).toEqual(["c2", "c1"]);
    await h.select(v("c1"));
    expect(h.state.value.selected?.content).toBe("text of c1");
    expect(version).toHaveBeenCalledWith("c1", "a.md");
  });

  it("drops a slow answer for an earlier choice", async () => {
    let answer: (s: string) => void = () => {};
    const version = vi
      .fn<(commit: string) => Promise<string>>()
      .mockImplementationOnce(() => new Promise((r) => (answer = r)))
      .mockResolvedValueOnce("second");
    const h = new History("a.md", { history: async () => [], version });
    const first = h.select(v("c1"));
    await h.select(v("c2"));
    answer("first, but late");
    await first;
    expect(h.state.value.selected).toEqual({ version: v("c2"), content: "second" });
  });

  it("doesn't fetch a version that deleted the note", async () => {
    const version = vi.fn(async () => "");
    const h = new History("a.md", { history: async () => [], version });
    await h.select(v("c3", true));
    expect(version).not.toHaveBeenCalled();
    expect(h.state.value.selected?.content).toBeUndefined();
  });
});
