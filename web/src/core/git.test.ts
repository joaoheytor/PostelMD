import { describe, expect, it, vi } from "vitest";
import { ApiError, type GitStatus } from "./api";
import { Git, type GitDeps } from "./git";

const clean: GitStatus = { branch: "main", changes: [], locks: [] };
const edited: GitStatus = { branch: "main", changes: [{ path: "a.md", kind: "modified" }], locks: [] };

function deps(over: Partial<GitDeps> = {}): GitDeps {
  return {
    status: vi.fn(async () => edited),
    commit: vi.fn(async () => "0123456789abcdef"),
    removeLocks: vi.fn(async () => {}),
    saveOpen: vi.fn(async () => true),
    ...over,
  };
}

describe("Git", () => {
  it("saves the open note, commits, and shows the new status", async () => {
    const order: string[] = [];
    const d = deps({
      saveOpen: vi.fn(async () => (order.push("save"), true)),
      commit: vi.fn(async () => (order.push("commit"), "0123456789abcdef")),
      status: vi.fn(async () => clean),
    });
    const git = new Git(d);
    expect(await git.commit("Add a runbook")).toBe(true);
    expect(order).toEqual(["save", "commit"]);
    expect(d.commit).toHaveBeenCalledWith("Add a runbook");
    expect(git.state.value).toMatchObject({ committed: "0123456", status: clean, busy: false });
  });

  it("doesn't commit while the open note has unsaved edits", async () => {
    const d = deps({ saveOpen: vi.fn(async () => false) });
    const git = new Git(d);
    expect(await git.commit("x")).toBe(false);
    expect(d.commit).not.toHaveBeenCalled();
    expect(git.state.value.error).toContain("aren't saved");
  });

  it("reports a refused commit and refreshes, so a lock that blocked it shows", async () => {
    const locked: GitStatus = { ...edited, locks: [{ path: ".git/index.lock", modified: "2026-11-10T00:00:00Z" }] };
    const d = deps({
      commit: vi.fn(() => Promise.reject(new ApiError(423, "a git lock is blocking commits"))),
      status: vi.fn(async () => locked),
    });
    const git = new Git(d);
    expect(await git.commit("x")).toBe(false);
    expect(git.state.value.error).toBe("Couldn't commit: a git lock is blocking commits");
    expect(git.state.value.status?.locks).toHaveLength(1);
  });

  it("removes the locks and refreshes", async () => {
    const d = deps();
    const git = new Git(d);
    await git.removeLocks();
    expect(d.removeLocks).toHaveBeenCalled();
    expect(git.state.value.status).toBe(edited);
  });

  it("forgets the last commit's messages when opened again", async () => {
    const git = new Git(deps());
    await git.commit("x");
    expect(git.state.value.committed).toBe("0123456");
    await git.open();
    expect(git.state.value.committed).toBeUndefined();
  });

  it("keeps the last status when a refresh fails", async () => {
    const status = vi.fn<GitDeps["status"]>().mockResolvedValueOnce(edited).mockRejectedValueOnce(new Error("503"));
    const git = new Git(deps({ status }));
    await git.refresh();
    await git.refresh();
    expect(git.state.value.status).toBe(edited);
    expect(git.state.value.error).toBe("Couldn't read the git status: 503");
  });
});
