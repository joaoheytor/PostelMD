import { html, nothing } from "lit";
import type { Workspace } from "../core/workspace";

/** ADR-0020: a warning only. It names each finding by rule, line and a short hint, never the secret. */
export function secretWarning(workspace: Workspace) {
  const w = workspace.secrets.warning.value;
  if (!w || w.dismissed || w.findings.length === 0) return nothing;
  return html`<section class="pm-banner" role="alert" aria-labelledby="pm-secret-title">
    <h3 id="pm-secret-title">This note looks like it contains a secret</h3>
    <ul>
      ${w.findings.map((f) => html`<li>${f.rule} on line ${f.line} (<code>${f.hint}</code>)</li>`)}
    </ul>
    <p>It is saved in the note, and a commit would keep it in the git history. Remove it, and rotate it if it is real.</p>
    <button type="button" @click=${() => workspace.secrets.dismiss()}>Dismiss</button>
  </section>`;
}

/** The open note was deleted or moved elsewhere: autosave never recreates it (ADR-0003 §7). */
export function goneBanner(workspace: Workspace) {
  return html`<section class="pm-banner" role="alert" aria-labelledby="pm-gone-title">
    <h3 id="pm-gone-title">This note was deleted or moved outside this editor</h3>
    <p>Your text is still here, but it isn't saved anywhere.</p>
    <button type="button" @click=${() => workspace.saveAsNew()}>Save it as a new note</button>
  </section>`;
}
