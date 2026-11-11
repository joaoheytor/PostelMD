import { html, nothing } from "lit";
import * as api from "../core/history-api";
import { type DiffLine, diffLines } from "../core/diff";
import { History } from "../core/history";
import type { Workspace } from "../core/workspace";
import { LightElement, StoreController } from "./base";
import { diffView } from "./diff";

/**
 * The open note's history: the commits that changed it; for the one selected, what changed
 * since then (or its whole text); and restoring it. The owner renders it to open it.
 */
export class HistoryDialog extends LightElement {
  static override properties = {
    workspace: { attribute: false },
    onClose: { attribute: false },
    whole: { state: true },
    busy: { state: true },
  };
  declare workspace: Workspace;
  declare onClose: () => void;
  /** Show the selected version's whole text instead of the changes since. */
  declare whole: boolean;
  declare busy: boolean;

  private history: History | undefined;
  private readonly st = new StoreController(this, () => this.history?.state);
  /** The last diff, kept while its inputs don't change: rendering it again is free. */
  private diff: { from: string; to: string; lines: DiffLine[] } | undefined;

  constructor() {
    super();
    this.whole = false;
    this.busy = false;
  }

  override connectedCallback(): void {
    const path = this.workspace.state.value.open?.doc.path ?? "";
    this.history = new History(path, { history: api.history, version: api.version });
    super.connectedCallback(); // after: the store controller subscribes to this history
  }

  protected override firstUpdated(): void {
    this.querySelector("dialog")?.showModal();
    void this.history?.load();
  }

  private close = () => this.querySelector("dialog")?.close();

  private current(): string {
    const open = this.workspace.state.value.open;
    return this.workspace.currentText() ?? open?.initial ?? "";
  }

  private changes(from: string): DiffLine[] {
    const to = this.current();
    if (this.diff?.from !== from || this.diff.to !== to) this.diff = { from, to, lines: diffLines(from, to) };
    return this.diff.lines;
  }

  private async restore(v: api.Version): Promise<void> {
    const path = this.history?.path;
    const when = new Date(v.when).toLocaleString();
    if (!confirm(`Replace the text of ${path} with its version from ${when}?\n\nIf the current text isn't committed, it won't be in the history.`)) return;
    this.busy = true;
    try {
      if (await this.workspace.restore(v.commit)) this.close();
    } finally {
      this.busy = false;
    }
  }

  private view() {
    const sel = this.st.value?.selected;
    if (!sel) return html`<p class="pm-history-hint">Choose a version.</p>`;
    if (sel.version.deleted) return html`<p class="pm-history-hint">This commit deleted the note.</p>`;
    if (sel.content === undefined) return html`<p class="pm-history-hint">Loading…</p>`;
    const body = this.whole
      ? html`<pre class="pm-diff" tabindex="0" aria-label="Text of this version">${sel.content}</pre>`
      : diffView(this.changes(sel.content), "Changes from this version to the current text");
    return html`
      <div class="pm-dialog-actions">
        <button type="button" aria-pressed=${!this.whole} @click=${() => (this.whole = false)}>Changes since</button>
        <button type="button" aria-pressed=${this.whole} @click=${() => (this.whole = true)}>Whole version</button>
        <button type="button" ?disabled=${this.busy} @click=${() => this.restore(sel.version)}>Restore this version</button>
      </div>
      ${body}
    `;
  }

  protected override render() {
    const s = this.st.value;
    const selected = s?.selected?.version.commit;
    return html`
      <dialog class="pm-dialog pm-dialog-wide" aria-labelledby="pm-history-title" @close=${() => this.onClose()}>
        <div class="pm-card">
          <h1 id="pm-history-title">History of ${s?.path}</h1>
          ${s?.error ? html`<p class="pm-error" role="alert">${s.error}</p>` : nothing}
          <div class="pm-history">
            ${s?.versions === undefined
              ? html`<p>Loading…</p>`
              : s.versions.length === 0
                ? html`<p>No commits have changed this note yet.</p>`
                : html`<ul class="pm-history-list" aria-label="Versions">
                    ${s.versions.map(
                      (v) => html`<li>
                        <button type="button" aria-pressed=${v.commit === selected} @click=${() => this.history?.select(v)}>
                          <span class="pm-history-msg">${v.message.split("\n")[0]}</span>
                          <span class="pm-history-meta">${v.author}, ${new Date(v.when).toLocaleString()}${v.deleted ? ", deleted" : ""}</span>
                        </button>
                      </li>`,
                    )}
                  </ul>`}
            <div class="pm-history-view">${this.view()}</div>
          </div>
          <div class="pm-dialog-actions"><button type="button" @click=${this.close}>Close</button></div>
        </div>
      </dialog>
    `;
  }
}
customElements.define("pm-history", HistoryDialog);
