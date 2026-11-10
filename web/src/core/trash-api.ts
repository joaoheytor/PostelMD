/** The trash API (ADR-0024), beside api.ts. */
import { json, ok, request } from "./api";

export interface TrashItem {
  /** The item's entry in the trash: what a restore names it by. Opaque to the app. */
  id: string;
  /** The item's name before the delete. */
  name: string;
  kind: "file" | "folder";
  /** When it was deleted (RFC 3339). */
  deletedAt: string;
  /** Files only. */
  size?: number;
}

/** The newest items in the trash; truncated when there are more than the server lists. */
export async function listTrash(): Promise<{ items: TrashItem[]; truncated: boolean }> {
  return (await (await ok(await request("GET", "/api/v1/trash"))).json()) as { items: TrashItem[]; truncated: boolean };
}

/**
 * Moves a trashed item back into folder ("" for the vault root), under name ("" keeps its
 * own); returns its new path. Fails with 409 if something there has that name: it never
 * overwrites.
 */
export async function restoreTrashed(id: string, folder: string, name: string): Promise<string> {
  const resp = await ok(await request("POST", `/api/v1/trash/${encodeURIComponent(id)}/restore`, json({ folder, name })));
  return ((await resp.json()) as { path: string }).path;
}
