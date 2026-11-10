/**
 * The HTTP API (ADR-0020), as plain functions over fetch. Nothing here keeps state, except
 * the hook that tells the app when the session has expired.
 */

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
    readonly retryAfterMs = 0,
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
  try {
    const body = (await resp.json()) as { error?: string };
    if (body.error) message = body.error;
  } catch {
    // Not JSON: keep the status text.
  }
  const retry = Number(resp.headers.get("Retry-After"));
  return new ApiError(resp.status, message, Number.isFinite(retry) ? retry * 1000 : 0);
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
