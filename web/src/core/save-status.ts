import type { DocState } from "./document";

/** The save status line's label, by status. */
export const statusText: Record<DocState["status"], string> = {
  clean: "Saved",
  dirty: "Edited",
  saving: "Saving…",
  stalled: "Saving is taking too long; retrying",
  error: "Not saved",
  conflict: "Not saved: conflict",
  gone: "Not saved: the note is gone",
};

/**
 * The save status line's detail, after its label ("Not saved: …"): what happened, what happens
 * next and, only when kept (a draft in storage that outlives the tab holds the edits), that
 * they are kept in this browser. Undefined when the status has no detail.
 */
export function statusDetail(st: DocState, kept: boolean): string | undefined {
  if (!st.message) return undefined;
  const what = st.message.replace(/\.$/, "");
  if (!st.next) return `${what}.`;
  return `${what}. ${st.next}${kept ? "; your edits are kept in this browser" : ""}.`;
}

/** The state as the status line shows it: its message is the whole detail. */
export function shownState(st: DocState, kept: boolean): DocState {
  const detail = statusDetail(st, kept);
  const shown: DocState = { status: st.status, ...(st.conflict && { conflict: st.conflict }) };
  return detail === undefined ? shown : { ...shown, message: detail };
}
