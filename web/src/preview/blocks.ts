import type { Block } from "./render";

/**
 * Checking a batch of sanitized blocks (ADR-0007). The preview sanitizes many blocks in one
 * call, each in a wrapper, and maps the wrappers back to blocks by position. A block's raw
 * HTML can break that: an unclosed <div> swallows the wrappers after it, and a stray </div>
 * followed by a fake wrapper adds one. Each wrapper therefore carries a key the note can't
 * know, and a batch whose top level isn't exactly one keyed wrapper per block, in order, is
 * done again one block at a time.
 */

/** The key's attribute: removed once the batch is checked. */
export const keyAttr = "data-pm-key";

/** A random key, made once per preview: a note can't guess it to forge a wrapper. */
export function newKey(): string {
  return [...crypto.getRandomValues(new Uint8Array(16))].map((b) => b.toString(16).padStart(2, "0")).join("");
}

/** The batch's HTML: every block in its own wrapper, numbered under key. */
export function batchHTML(blocks: readonly Block[], key: string): string {
  return blocks.map((b, i) => `<div class="pm-block" data-line="${b.line}" ${keyAttr}="${key}-${i}">${b.html}</div>`).join("");
}

/** What the check reads of a node: the DOM's Node and Element have it. */
export interface NodeLike {
  readonly nodeType: number;
  readonly nodeName: string;
  getAttribute?(name: string): string | null;
}

/** Whether a sanitized batch's top-level nodes are exactly the wrappers batchHTML made. */
export function intact(nodes: ArrayLike<NodeLike>, blocks: readonly Block[], key: string): boolean {
  if (nodes.length !== blocks.length) return false;
  for (let i = 0; i < blocks.length; i++) {
    const n = nodes[i];
    const at = (name: string) => n?.getAttribute?.(name);
    if (n?.nodeType !== 1 || n.nodeName !== "DIV") return false;
    if (at("class") !== "pm-block" || at("data-line") !== String(blocks[i]?.line) || at(keyAttr) !== `${key}-${i}`) return false;
  }
  return true;
}
