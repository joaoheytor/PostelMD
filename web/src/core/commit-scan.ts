/**
 * The commit dialog's secret check (ADR-0020): it scans exactly the bytes the dialog lists,
 * so a commit never takes a change the user hasn't been warned about. Nothing leaves the
 * browser; findings carry only the rule, the line and a short hint, never the secret.
 */
import type { GitChange } from "./api";
import { scanSecrets, type SecretFinding } from "./secrets";

/** A file's current bytes and the ETag they were read with. */
export interface FileBytes {
  bytes: Uint8Array;
  etag: string;
}

export type FileScan =
  | { path: string; result: "clean" }
  | { path: string; result: "findings"; findings: SecretFinding[] }
  /** Binary (a NUL byte) or not UTF-8: listed as not scanned. */
  | { path: string; result: "not-text" };

export type ChangesScan =
  /** One entry per added or modified file, in the listed order. */
  | { kind: "scanned"; files: FileScan[] }
  /** A file no longer has the listed bytes (or is gone): list the changes again. */
  | { kind: "changed"; path: string };

/** Reads a file's bytes; undefined when it no longer exists. */
export type ReadFile = (path: string) => Promise<FileBytes | undefined>;

/** Scans each added or modified change's current bytes, checking they are the listed ones. */
export async function scanChanges(changes: GitChange[], read: ReadFile): Promise<ChangesScan> {
  const files: FileScan[] = [];
  for (const c of changes) {
    if (c.kind === "deleted") continue; // nothing new enters the history
    const f = await read(c.path);
    if (!f || !c.rev || f.etag !== `"${c.rev}"`) return { kind: "changed", path: c.path };
    const text = decodeText(f.bytes);
    if (text === undefined) {
      files.push({ path: c.path, result: "not-text" });
      continue;
    }
    const findings = scanSecrets(text);
    files.push(findings.length ? { path: c.path, result: "findings", findings } : { path: c.path, result: "clean" });
  }
  return { kind: "scanned", files };
}

/** The bytes as text, or undefined for binary content (a NUL byte, or invalid UTF-8). */
export function decodeText(bytes: Uint8Array): string | undefined {
  if (bytes.includes(0)) return undefined;
  try {
    return new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  } catch {
    return undefined;
  }
}

/** The files with findings. */
export function withFindings(files: FileScan[] | undefined): Extract<FileScan, { result: "findings" }>[] {
  return (files ?? []).filter((f): f is Extract<FileScan, { result: "findings" }> => f.result === "findings");
}
