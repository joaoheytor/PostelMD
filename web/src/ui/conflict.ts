import { html } from "lit";
import { type DiffLine, diffLines } from "../core/diff";
import type { DocState } from "../core/document";
import { diffView } from "./diff";

type Conflict = NonNullable<DocState["conflict"]>;

/** Each conflict's diff, computed once: the banner renders again on every status change. */
const diffs = new WeakMap<Conflict, DiffLine[]>();

/**
 * A save found the note changed elsewhere (412, ADR-0003): what differs between the editor's
 * text and the other version, and what each way out does. The actions are the document
 * session's keepMine and takeTheirs, unchanged.
 */
export function conflictBanner(conflict: Conflict, mine: () => string, keep: () => void, take: () => void) {
  let lines = diffs.get(conflict);
  if (!lines) diffs.set(conflict, (lines = diffLines(mine(), conflict.content)));
  return html`<section class="pm-banner" role="alert" aria-labelledby="pm-conflict-title">
    <h3 id="pm-conflict-title">This note changed in another tab or outside this editor</h3>
    <p>Your edits aren't saved yet. Lines marked - are only in your text; lines marked + are only in the other version.</p>
    ${diffView(lines, "Changes from your text to the other version")}
    <ul class="pm-conflict-actions">
      <li>
        <button type="button" aria-describedby="pm-keep-mine" @click=${keep}>Keep my version</button>
        <span id="pm-keep-mine">saves your text over the other version.</span>
      </li>
      <li>
        <button type="button" aria-describedby="pm-take-theirs" @click=${take}>Use the other version</button>
        <span id="pm-take-theirs">replaces your text in the editor; Ctrl+Z brings it back.</span>
      </li>
    </ul>
  </section>`;
}
