/** The git API (ADR-0020), beside api.ts. */
import { encodePath, json, ok, request } from "./api";

export interface GitChange {
  path: string;
  kind: "added" | "modified" | "deleted";
  /** The revision of the file's bytes, the ETag a read of them answers (unquoted); none for a deletion. */
  rev?: string;
}

/** A change as the commit dialog listed it: the commit takes exactly these. */
export interface ListedChange {
  path: string;
  /** The revision the dialog checked; null for a deletion. */
  rev: string | null;
}

export interface GitStatus {
  branch: string;
  changes: GitChange[];
  /** Files over the commit size limit: never committed. */
  skipped?: string[];
  /** Lock files blocking commits (a crash's leftover, or git running on the host). */
  locks: { path: string; modified: string }[];
}

export async function gitStatus(): Promise<GitStatus> {
  return (await (await ok(await request("GET", "/api/v1/git/status"))).json()) as GitStatus;
}

/**
 * Commits the listed changes; returns the commit's hash. A 409 with code "changes-differ"
 * means they are no longer the changes, and nothing was committed.
 */
export async function commit(message: string, changes: ListedChange[]): Promise<string> {
  const resp = await ok(await request("POST", "/api/v1/git/commit", json({ message, changes })));
  return ((await resp.json()) as { commit: string }).commit;
}

/** A file's current bytes and ETag, for the commit's secret check; undefined if it's gone. */
export async function readFileBytes(path: string): Promise<{ bytes: Uint8Array; etag: string } | undefined> {
  const resp = await request("GET", `/api/v1/notes/${encodePath(path)}`);
  if (resp.status === 404) return undefined;
  await ok(resp);
  return { bytes: new Uint8Array(await resp.arrayBuffer()), etag: resp.headers.get("ETag") ?? "" };
}

/** Removes the git locks; the user has confirmed that no other git command is running. */
export async function removeGitLocks(): Promise<void> {
  await ok(await request("POST", "/api/v1/git/locks/remove", json({ confirmNoOtherGit: true })));
}
