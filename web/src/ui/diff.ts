import { html } from "lit";
import type { DiffLine } from "../core/diff";

const mark: Record<DiffLine["kind"], string> = { same: "  ", added: "+ ", removed: "- " };

/**
 * A line diff as a labelled <pre>, with added and removed lines marked for every reader. It
 * scrolls, so it takes the focus for keyboard scrolling.
 */
export function diffView(lines: DiffLine[], label: string) {
  return html`<pre class="pm-diff" tabindex="0" aria-label=${label}>${lines.map((l) =>
    l.kind === "same"
      ? html`<span>${mark.same}${l.text}\n</span>`
      : l.kind === "added"
        ? html`<ins>${mark.added}${l.text}\n</ins>`
        : html`<del>${mark.removed}${l.text}\n</del>`,
  )}</pre>`;
}
