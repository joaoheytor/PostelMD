import { describe, expect, it, vi } from "vitest";
import { ApiError } from "./api";
import type { GitStatus } from "./git-api";
import { changedNotice, Git, type GitDeps } from "./git";

// Fake secrets, built at run time so no scanner flags this public file.
const awsKey = `AKIA${"Q7".repeat(8)}`;
const enc = new TextEncoder();

const clean: GitStatus = { branch: "main", changes: [], locks: [] };
const edited: GitStatus = { branch: "main", changes: [{ path: "a.md", kind: "modified", rev: "r1" }], locks: [] };
const leaked: GitStatus = { branch: "main", changes: [{ path: "creds.md", kind: "added", rev: "r2" }], locks: [] };

/** A file server where every listed file has its listed bytes. */
function readAs(content: string, etag: string) {
  return vi.fn(async () => ({ bytes: enc.encode(content), etag }));
}

function deps(over: Partial<GitDeps> = {}): GitDeps {
  return {
    status: vi.fn(async () => edited),
    readFile: readAs("# A\n", `"r1"`),
    commit: vi.fn(async () => "0123456789abcdef"),
    removeLocks: vi.fn(async () => {}),
    saveOpen: vi.fn(async () => true),
    ...over,
  };
}

async function opened(d: GitDeps): Promise<Git> {
  const git = new Git(d);
  await git.open();
  return git;
}

describe("Git", () => {
  it("saves the open note, commits exactly the listed changes, and shows the new status", async () => {
    const order: string[] = [];
    const status = vi.fn<GitDeps["status"]>().mockResolvedValueOnce(edited).mockResolvedValue(clean);
    const d = deps({
      status,
      saveOpen: vi.fn(async () => (order.push("save"), true)),
      commit: vi.fn(async () => (order.push("commit"), "0123456789abcdef")),
    });
    const git = await opened(d);
    order.length = 0;
    expect(await git.commit("Add a runbook")).toBe(true);
    expect(order).toEqual(["save", "commit"]);
    expect(d.commit).toHaveBeenCalledWith("Add a runbook", [{ path: "a.md", rev: "r1" }]);
    expect(git.state.value).toMatchObject({ committed: "0123456", status: clean, busy: false });
  });

  it("lists findings, and commits them only with Commit anyway", async () => {
    const d = deps({ status: vi.fn(async () => leaked), readFile: readAs(`aws = ${awsKey}\n`, `"r2"`) });
    const git = await opened(d);
    expect(git.needsConfirmation()).toBe(true);
    expect(git.state.value.scan).toEqual([
      { path: "creds.md", result: "findings", findings: [{ rule: "AWS access key ID", line: 1, hint: "AKIA…" }] },
    ]);
    expect(await git.commit("x")).toBe(false);
    expect(d.commit).not.toHaveBeenCalled();
    expect(await git.commit("x", true)).toBe(true);
    expect(d.commit).toHaveBeenCalledWith("x", [{ path: "creds.md", rev: "r2" }]);
  });

  it("treats a file it couldn't check like a finding, and lists a deletion with rev null", async () => {
    const mixed: GitStatus = {
      branch: "main",
      changes: [
        { path: "img.bin", kind: "added", rev: "r3" },
        { path: "old.md", kind: "deleted" },
      ],
      locks: [],
    };
    const d = deps({ status: vi.fn(async () => mixed), readFile: vi.fn(async () => ({ bytes: new Uint8Array([0x41, 0]), etag: `"r3"` })) });
    const git = await opened(d);
    expect(git.state.value.scan).toEqual([{ path: "img.bin", result: "not-checked", reason: "binary" }]);
    expect(await git.commit("x")).toBe(false);
    expect(d.commit).not.toHaveBeenCalled();
    expect(await git.commit("x", true)).toBe(true);
    expect(d.commit).toHaveBeenCalledWith("x", [
      { path: "img.bin", rev: "r3" },
      { path: "old.md", rev: null },
    ]);
  });

  it("lists the changes again when a file's bytes aren't the listed ones", async () => {
    const status = vi.fn<GitDeps["status"]>().mockResolvedValueOnce(edited).mockResolvedValue(leaked);
    const readFile = vi.fn<GitDeps["readFile"]>(async (p) =>
      p === "a.md" ? { bytes: enc.encode("newer"), etag: `"r9"` } : { bytes: enc.encode(`aws = ${awsKey}`), etag: `"r2"` },
    );
    const git = await opened(deps({ status, readFile }));
    expect(git.state.value).toMatchObject({ status: leaked, notice: changedNotice, scanning: false });
    expect(git.needsConfirmation()).toBe(true);
  });

  it("doesn't commit before the check has run", async () => {
    const d = deps();
    const git = new Git(d);
    expect(await git.commit("x")).toBe(false);
    expect(d.commit).not.toHaveBeenCalled();
  });

  it("on changes-differ, commits nothing and lists and checks the changes again", async () => {
    const status = vi.fn<GitDeps["status"]>().mockResolvedValueOnce(edited).mockResolvedValue(leaked);
    const d = deps({
      status,
      readFile: vi.fn(async (p: string) => ({ bytes: enc.encode(p === "a.md" ? "# A" : awsKey), etag: p === "a.md" ? `"r1"` : `"r2"` })),
      commit: vi.fn(() => Promise.reject(new ApiError(409, "the changes were updated", 0, "changes-differ"))),
    });
    const git = await opened(d);
    expect(await git.commit("x")).toBe(false);
    expect(git.state.value).toMatchObject({ notice: changedNotice, status: leaked, error: undefined });
    expect(git.needsConfirmation()).toBe(true);
  });

  it("doesn't commit while the open note has unsaved edits", async () => {
    const saveOpen = vi.fn<GitDeps["saveOpen"]>().mockResolvedValueOnce(true).mockResolvedValue(false);
    const d = deps({ saveOpen });
    const git = await opened(d);
    expect(await git.commit("x")).toBe(false);
    expect(d.commit).not.toHaveBeenCalled();
    expect(git.state.value.error).toContain("aren't saved");
  });

  it("reports a refused commit and refreshes, so a lock that blocked it shows", async () => {
    const locked: GitStatus = { ...edited, locks: [{ path: ".git/index.lock", modified: "2026-11-10T00:00:00Z" }] };
    const status = vi.fn<GitDeps["status"]>().mockResolvedValueOnce(edited).mockResolvedValue(locked);
    const d = deps({ commit: vi.fn(() => Promise.reject(new ApiError(423, "a git lock is blocking commits"))), status });
    const git = await opened(d);
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
    const git = await opened(deps());
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
    expect(git.state.value.error).toBe("Couldn't read the changes: 503");
  });
});
