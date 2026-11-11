/** A note's git history (ADR-0020), beside api.ts. */
import { encodePath, ok, request } from "./api";

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
