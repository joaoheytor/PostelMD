/**
 * The commit dialog's secret check (ADR-0020): it scans exactly the bytes the dialog lists,
 * so a commit never takes a change the user hasn't been warned about. Nothing leaves the
 * browser; findings carry only the rule, the line and a short hint, never the secret.
 */
import type { GitChange } from "./git-api";
import { scanSecrets, type SecretFinding } from "./secrets";

/** A file's current bytes and the ETag they were read with. */
export interface FileBytes {
  bytes: Uint8Array;
  etag: string;
}

/** Why a file wasn't checked. Like a finding, it means only "Commit anyway" commits. */
export type NotChecked = "binary" | "too large" | "over the limit";

export type FileScan =
  | { path: string; result: "clean" }
  | { path: string; result: "findings"; findings: SecretFinding[] }
  | { path: string; result: "not-checked"; reason: NotChecked };

export type ChangesScan =
  /** One entry per added or modified file, in the listed order. */
  | { kind: "scanned"; files: FileScan[] }
  /** A file no longer has the listed bytes (or is gone): list the changes again. */
  | { kind: "changed"; path: string };

/** Reads a file's bytes; undefined when it no longer exists. */
export type ReadFile = (path: string) => Promise<FileBytes | undefined>;

export interface ScanLimits {
  /** The largest file scanned: the editor's own limit (the server's MaxNoteSize). */
  file: number;
  /** The most bytes scanned in one go; the files after that aren't checked. */
  total: number;
}

export const defaultLimits: ScanLimits = { file: 5 << 20, total: 32 << 20 };

/** Scans each added or modified change's current bytes, checking they are the listed ones. */
export async function scanChanges(changes: GitChange[], read: ReadFile, limits = defaultLimits): Promise<ChangesScan> {
  const files: FileScan[] = [];
  let scanned = 0;
  for (const c of changes) {
    if (c.kind === "deleted") continue; // nothing new enters the history
    if (scanned >= limits.total) {
      files.push({ path: c.path, result: "not-checked", reason: "over the limit" });
      continue;
    }
    const f = await read(c.path);
    if (!f || !c.rev || f.etag !== `"${c.rev}"`) return { kind: "changed", path: c.path };
    const size = f.bytes.length;
    if (size > limits.file) {
      files.push({ path: c.path, result: "not-checked", reason: "too large" });
      continue;
    }
    if (scanned + size > limits.total) {
      files.push({ path: c.path, result: "not-checked", reason: "over the limit" });
      continue;
    }
    scanned += size;
    const text = decodeText(f.bytes);
    if (text === undefined) {
      files.push({ path: c.path, result: "not-checked", reason: "binary" });
      continue;
    }
    const findings = scanText(text);
    files.push(findings.length ? { path: c.path, result: "findings", findings } : { path: c.path, result: "clean" });
  }
  return { kind: "scanned", files };
}

/**
 * scanSecrets over text in chunks of whole lines, so finding a line number costs at most a
 * chunk, not the whole text. A match spanning two chunks (only a multi-line one could) is
 * missed; every rule but one is single-line.
 */
export function scanText(text: string, chunk = 64 << 10): SecretFinding[] {
  const out: SecretFinding[] = [];
  let line = 0; // lines before the chunk
  for (let start = 0; start < text.length; ) {
    let end = text.length;
    if (text.length - start > chunk) {
      end = text.lastIndexOf("\n", start + chunk); // the last line break in the chunk
      if (end < start) end = text.indexOf("\n", start + chunk); // one line longer than a chunk
      if (end === -1) end = text.length;
    }
    const part = text.slice(start, end);
    for (const f of scanSecrets(part)) out.push({ ...f, line: f.line + line });
    for (let i = part.indexOf("\n"); i !== -1; i = part.indexOf("\n", i + 1)) line++;
    line++; // the line break at end
    start = end + 1;
  }
  return out;
}

/** The bytes as text (invalid UTF-8 replaced), or undefined for binary content (a NUL byte). */
export function decodeText(bytes: Uint8Array): string | undefined {
  if (bytes.includes(0)) return undefined;
  return new TextDecoder("utf-8").decode(bytes);
}

/** The files with findings. */
export function withFindings(files: FileScan[] | undefined): Extract<FileScan, { result: "findings" }>[] {
  return (files ?? []).filter((f): f is Extract<FileScan, { result: "findings" }> => f.result === "findings");
}

/** The files that weren't checked. */
export function notChecked(files: FileScan[] | undefined): Extract<FileScan, { result: "not-checked" }>[] {
  return (files ?? []).filter((f): f is Extract<FileScan, { result: "not-checked" }> => f.result === "not-checked");
}

/** A finding, or a file that wasn't checked: only "Commit anyway" commits. */
export function needsConfirmation(files: FileScan[] | undefined): boolean {
  return withFindings(files).length > 0 || notChecked(files).length > 0;
}
