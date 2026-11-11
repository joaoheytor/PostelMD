import { html } from "lit";
import { type DiffLine, textDiff } from "../core/diff";
import type { DocState } from "../core/document";
import { LineEndingsError, textFormat } from "../core/textfile";
import { diffView } from "./diff";

type Conflict = NonNullable<DocState["conflict"]>;

/** The ways out of a conflict: the workspace's and the document session's, unchanged. */
export interface ConflictActions {
  /** The editor's text as a save would write it, for the diff. */
  mine(): string;
  keep(): void;
  /** Puts the other version in the editor; converted: its line endings made LF, saved. */
  take(convert: boolean): void;
  /** Saves the editor's text as a new file next to this one. */
  saveAsNew(): void;
}

/** Each conflict's diff, computed once: the banner renders again on every status change. */
const diffs = new WeakMap<Conflict, DiffLine[] | "endings">();

/** Whether the other version has line endings the editor can only take converted. */
function needsConvert(content: string): boolean {
  try {
    textFormat(content);
    return false;
  } catch (err) {
    return err instanceof LineEndingsError;
  }
}

/**
 * A save found the note changed elsewhere (412, ADR-0003): what differs between the editor's
 * text and the other version, and what each way out does. Another version that isn't text has
 * nothing to compare or take: the user's text can only go to a new file.
 */
export function conflictBanner(conflict: Conflict, act: ConflictActions) {
  if (conflict.binary) return binaryBanner(act);
  let lines = diffs.get(conflict);
  if (!lines) diffs.set(conflict, (lines = textDiff(act.mine(), conflict.content)));
  const convert = needsConvert(conflict.content);
  const takeConverted = () => {
    const ask = "The other version has line endings the editor can't keep. Put it in the editor with LF line endings and save it that way?";
    if (confirm(ask)) act.take(true);
  };
  return html`<section class="pm-banner" role="alert" aria-labelledby="pm-conflict-title">
    <h3 id="pm-conflict-title">This note changed in another tab or outside this editor</h3>
    <p>Your edits aren't saved yet. Lines marked - are only in your text; lines marked + are only in the other version.</p>
    ${diffView(lines, "Changes from your text to the other version")}
    <ul class="pm-conflict-actions">
      <li>
        <button type="button" aria-describedby="pm-keep-mine" @click=${act.keep}>Keep my version</button>
        <span id="pm-keep-mine">saves your text over the other version.</span>
      </li>
      <li>
        ${convert
          ? html`<button type="button" aria-describedby="pm-take-theirs" @click=${takeConverted}>
                Use the other version, converted to LF
              </button>
              <span id="pm-take-theirs">replaces your text in the editor and saves it with LF line endings; Ctrl+Z brings yours back.</span>`
          : html`<button type="button" aria-describedby="pm-take-theirs" @click=${() => act.take(false)}>Use the other version</button>
              <span id="pm-take-theirs">replaces your text in the editor; Ctrl+Z brings it back.</span>`}
      </li>
    </ul>
  </section>`;
}

function binaryBanner(act: ConflictActions) {
  return html`<section class="pm-banner" role="alert" aria-labelledby="pm-conflict-title">
    <h3 id="pm-conflict-title">This file changed outside this editor into something PostelMD can't open</h3>
    <p>The other version is kept on disk as it is. Your edits aren't saved: save them as a new file to keep them.</p>
    <button type="button" @click=${act.saveAsNew}>Save my version as a new file</button>
  </section>`;
}
