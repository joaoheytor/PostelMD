/**
 * The HTTP API (ADR-0020), as plain functions over fetch. Nothing here keeps state, except
 * the hook that tells the app when the session has expired.
 */

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
    readonly retryAfterMs = 0,
    /** A machine-readable reason, when the server gives one ("changes-differ"). */
    readonly code = "",
  ) {
    super(message);
  }
}

let onUnauthorized: () => void = () => {};

/** Sets what happens when any request comes back 401 (the app asks for the password again). */
export function setUnauthorizedHandler(fn: () => void): void {
  onUnauthorized = fn;
}

/** Encodes a vault path for a URL, one segment at a time ("a b/c.md" → "a%20b/c.md"). */
export function encodePath(path: string): string {
  return path.split("/").map(encodeURIComponent).join("/");
}

async function request(method: string, url: string, init: RequestInit = {}): Promise<Response> {
  const resp = await fetch(url, { ...init, method, credentials: "same-origin", cache: "no-store" });
  if (resp.status === 401 && !url.startsWith("/api/v1/session")) onUnauthorized();
  return resp;
}

function json(body: unknown): RequestInit {
  return { headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) };
}

async function errorOf(resp: Response): Promise<ApiError> {
  let message = resp.statusText || `HTTP ${resp.status}`;
  let code = "";
  try {
    const body = (await resp.json()) as { error?: string; code?: string };
    if (body.error) message = body.error;
    if (typeof body.code === "string") code = body.code;
  } catch {
    // Not JSON: keep the status text.
  }
  const retry = Number(resp.headers.get("Retry-After"));
  return new ApiError(resp.status, message, Number.isFinite(retry) ? retry * 1000 : 0, code);
}

async function fail(resp: Response): Promise<never> {
  throw await errorOf(resp);
}

async function ok(resp: Response): Promise<Response> {
  return resp.ok ? resp : fail(resp);
}

// Session and setup.

export type SessionState = "ready" | "login" | "setup";

export async function sessionState(): Promise<SessionState> {
  const resp = await request("GET", "/api/v1/session");
  if (resp.status === 204) return "ready";
  if (resp.status === 401) return "login";
  if (resp.status === 503) return "setup";
  return fail(resp);
}

export async function login(password: string): Promise<void> {
  await ok(await request("POST", "/api/v1/session", json({ password })));
}

export async function logout(): Promise<void> {
  await ok(await request("DELETE", "/api/v1/session"));
}

/** Checks the setup token; returns how many files setup will commit. */
export async function checkSetup(token: string): Promise<number> {
  const resp = await ok(await request("POST", "/api/v1/setup/check", json({ token })));
  return ((await resp.json()) as { files: number }).files;
}

export interface SetupForm {
  token: string;
  password: string;
  name: string;
  email: string;
}

/** Runs the first-run setup; returns how many files the initial commit imported. */
export async function setup(form: SetupForm): Promise<number> {
  const resp = await ok(await request("POST", "/api/v1/setup", json(form)));
  return ((await resp.json()) as { imported: number }).imported;
}

// Notes and folders.

export type EntryKind = "file" | "folder" | "other";
export interface Entry {
  name: string;
  kind: EntryKind;
}

export async function listFolder(path: string): Promise<Entry[]> {
  const resp = await ok(await request("GET", `/api/v1/folders/${encodePath(path)}`));
  return ((await resp.json()) as { entries: Entry[] }).entries;
}

/** Every file's path, for the finder; truncated when the vault holds more than the server lists. */
export async function listFiles(): Promise<{ files: string[]; truncated: boolean }> {
  const resp = await ok(await request("GET", "/api/v1/files"));
  return (await resp.json()) as { files: string[]; truncated: boolean };
}

export interface Note {
  content: string;
  etag: string;
}

export async function readNote(path: string): Promise<Note> {
  const resp = await ok(await request("GET", `/api/v1/notes/${encodePath(path)}`));
  return { content: await resp.text(), etag: resp.headers.get("ETag") ?? "" };
}

/** The outcome of a save; saveNote never throws. */
export type SaveResult =
  | { kind: "saved"; etag: string }
  | { kind: "conflict"; etag: string; content: string }
  | { kind: "gone" }
  | { kind: "error"; message: string; retryAfterMs: number; retry: boolean };

/** Saves over baseETag (If-Match). It never creates a note (ADR-0003 §7). */
export async function saveNote(path: string, content: string, baseETag: string): Promise<SaveResult> {
  let resp: Response;
  try {
    resp = await request("PUT", `/api/v1/notes/${encodePath(path)}`, {
      headers: { "If-Match": baseETag, "Content-Type": "text/markdown; charset=utf-8" },
      body: content,
    });
  } catch (err) {
    return { kind: "error", message: `offline: ${String(err)}`, retryAfterMs: 0, retry: true };
  }
  if (resp.status === 204) return { kind: "saved", etag: resp.headers.get("ETag") ?? "" };
  if (resp.status === 412) return { kind: "conflict", etag: resp.headers.get("ETag") ?? "", content: await resp.text() };
  if (resp.status === 404) return { kind: "gone" };
  const e = await errorOf(resp);
  // Busy (503), an expired session (401) and server errors are retried; 413 and other client
  // errors won't get better by themselves.
  const retry = e.status === 401 || e.status === 503 || e.status >= 500;
  return { kind: "error", message: e.message, retryAfterMs: e.retryAfterMs, retry };
}

/** Creates a note (If-None-Match: *); fails with 409 if it exists. */
export async function createNote(path: string, content: string): Promise<string> {
  const resp = await ok(
    await request("PUT", `/api/v1/notes/${encodePath(path)}`, {
      headers: { "If-None-Match": "*", "Content-Type": "text/markdown; charset=utf-8" },
      body: content,
    }),
  );
  return resp.headers.get("ETag") ?? "";
}

export async function createFolder(path: string): Promise<void> {
  await ok(await request("POST", `/api/v1/folders/${encodePath(path)}`));
}

/** Renames a note or folder; fails with 409 if the destination exists (never overwrites). */
export async function move(from: string, to: string): Promise<void> {
  await ok(await request("POST", "/api/v1/move", json({ from, to })));
}

/** Moves a note or folder into the vault's trash. */
export async function trash(path: string, kind: "file" | "folder"): Promise<void> {
  const base = kind === "file" ? "notes" : "folders";
  await ok(await request("DELETE", `/api/v1/${base}/${encodePath(path)}`));
}

// Git (ADR-0020).

export interface GitChange {
  path: string;
  kind: "added" | "modified" | "deleted";
  /** The git blob ID of the file's bytes; null for a deletion. */
  blob: string | null;
  /** The same bytes' revision, the ETag a read of them answers (unquoted); none for a deletion. */
  rev?: string;
}

/** A change as the commit dialog listed it: the commit takes exactly these. */
export interface ListedChange {
  path: string;
  blob: string | null;
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

export interface Version {
  commit: string;
  message: string;
  author: string;
  when: string;
  /** The commit removed the note: there is no content to show. */
  deleted: boolean;
}

/** The commits that changed the note, newest first. */
export async function history(path: string): Promise<Version[]> {
  const resp = await ok(await request("GET", `/api/v1/history/${encodePath(path)}`));
  return ((await resp.json()) as { versions: Version[] }).versions;
}

/** The note's text in a commit. */
export async function version(commit: string, path: string): Promise<string> {
  return (await ok(await request("GET", `/api/v1/versions/${encodeURIComponent(commit)}/${encodePath(path)}`))).text();
}

/** Writes the note's text from commit through the save path; 412 if it isn't at base. */
export async function restore(path: string, commit: string, baseETag: string): Promise<string> {
  const resp = await ok(
    await request("POST", "/api/v1/restore", { ...json({ path, commit }), headers: { "Content-Type": "application/json", "If-Match": baseETag } }),
  );
  return resp.headers.get("ETag") ?? "";
}
