import { html, nothing } from "lit";
import { diffLines } from "../core/diff";
import type { Disposable } from "../core/disposable";
import type { DocumentSession } from "../core/document";
import type { Draft } from "../core/drafts";
import type { Workspace } from "../core/workspace";
import { LightElement, openNote, StoreController } from "./base";
import { diffView } from "./diff";

const when = (d: Draft) => new Date(d.savedAt).toLocaleString();

/**
 * The open note's drafts from other tabs, earlier loads or an earlier opening in this tab
 * (ADR-0003 §7, ADR-0023): compare each with the editor's text, restore it into the editor, or
 * discard it. Nothing goes without the user.
 */
export class DraftBanner extends LightElement {
  static override properties = {
    workspace: { attribute: false },
    put: { attribute: false },
    comparing: { state: true },
  };
  declare workspace: Workspace;
  /** Replaces the editor's text. */
  declare put: (text: string) => void;
  /** The draft whose changes are shown. */
  declare comparing: Draft | undefined;

  private readonly ws = new StoreController(this, () => this.workspace?.state);
  private readonly drafts = new StoreController(this, () => this.workspace?.noteDrafts);
  /** The open note's save state, followed so the "older version" hint tracks its ETag. */
  private watched: { doc: DocumentSession; sub: Disposable } | undefined;

  protected override willUpdate(): void {
    const doc = this.ws.value?.open?.doc;
    if (this.watched?.doc === doc) return;
    this.watched?.sub.dispose();
    this.watched = doc && { doc, sub: doc.state.subscribe(() => this.requestUpdate()) };
  }

  override disconnectedCallback(): void {
    super.disconnectedCallback();
    this.watched?.sub.dispose();
    this.watched = undefined;
  }

  private async restore(d: Draft): Promise<void> {
    if (!confirm(`Replace the editor's text with the draft from ${when(d)}?\n\nCtrl+Z brings the current text back.`)) return;
    await this.workspace.recoverDraft(d, this.put);
    this.comparing = undefined;
  }

  private async discard(d: Draft): Promise<void> {
    if (!confirm(`Discard the draft from ${when(d)}? It can't be recovered.`)) return;
    await this.workspace.discardDraft(d);
    this.comparing = undefined;
  }

  protected override render() {
    const open = this.ws.value?.open;
    const drafts = this.drafts.value ?? [];
    if (!open || drafts.length === 0) return nothing;
    const n = drafts.length;
    return html`<section class="pm-banner" role="region" aria-labelledby="pm-drafts-title">
      <h3 id="pm-drafts-title">${n === 1 ? "This note has an unsaved draft" : `This note has ${n} unsaved drafts`}</h3>
      <p>Edits from another tab, an earlier visit or an earlier opening in this tab that never reached the server.</p>
      <ul class="pm-draft-list">
        ${drafts.map(
          (d) => html`<li>
            <span>Draft from ${when(d)}${d.base === open.doc.etag ? nothing : html`, based on an older version; compare first`}</span>
            <button type="button" aria-pressed=${this.comparing === d} @click=${() => (this.comparing = this.comparing === d ? undefined : d)}>
              Compare
            </button>
            <button type="button" @click=${() => this.restore(d)}>Restore draft</button>
            <button type="button" @click=${() => this.discard(d)}>Discard draft</button>
          </li>`,
        )}
      </ul>
      ${this.comparing
        ? diffView(diffLines(this.workspace.currentText() ?? open.initial, this.comparing.text), "Changes from the current text to the draft")
        : nothing}
    </section>`;
  }
}
customElements.define("pm-draft-banner", DraftBanner);

/** Every unsaved draft on this device (ADR-0003 §7), including those of deleted notes. */
export class DraftsDialog extends LightElement {
  static override properties = { workspace: { attribute: false }, onClose: { attribute: false } };
  declare workspace: Workspace;
  declare onClose: () => void;

  private readonly offered = new StoreController(this, () => this.workspace?.offered);

  protected override firstUpdated(): void {
    this.querySelector("dialog")?.showModal();
    void this.workspace.refreshOffered();
  }

  private close = () => this.querySelector("dialog")?.close();

  private async open(d: Draft): Promise<void> {
    this.close();
    await openNote(this.workspace, d.path);
  }

  private async discard(d: Draft): Promise<void> {
    if (confirm(`Discard the draft of ${d.path} from ${when(d)}? It can't be recovered.`)) await this.workspace.discardDraft(d);
  }

  private async saveAsNew(d: Draft): Promise<void> {
    await this.workspace.saveDraftAsNew(d);
    if (!this.workspace.state.value.error) this.close();
  }

  protected override render() {
    const drafts = this.offered.value ?? [];
    return html`<dialog class="pm-dialog" aria-labelledby="pm-drafts-dialog-title" @close=${() => this.onClose()}>
      <div class="pm-card">
        <h1 id="pm-drafts-dialog-title">Unsaved drafts</h1>
        <p>Edits kept in this browser that never reached the server. Open the note to compare a draft and restore it; for a note that was deleted, save the draft as a new note.</p>
        ${drafts.length === 0
          ? html`<p>No unsaved drafts.</p>`
          : html`<ul class="pm-draft-list" aria-label="Drafts">
              ${drafts.map(
                (d) => html`<li>
                  <span><strong>${d.path}</strong>, ${when(d)}</span>
                  <button type="button" @click=${() => this.open(d)}>Open note</button>
                  <button type="button" @click=${() => this.saveAsNew(d)}>Save as new note</button>
                  <button type="button" @click=${() => this.discard(d)}>Discard</button>
                </li>`,
              )}
            </ul>`}
        <div class="pm-dialog-actions"><button type="button" @click=${this.close}>Close</button></div>
      </div>
    </dialog>`;
  }
}
customElements.define("pm-drafts", DraftsDialog);

/**
 * Says, without blocking, that the browser refused the open note's draft (quota, private
 * mode): until a save lands, its edits are only in this tab. Empty once a draft is written.
 */
export class DraftWarning extends LightElement {
  static override properties = { doc: { attribute: false } };
  declare doc: DocumentSession | undefined;
  private watched: { doc: DocumentSession; sub: Disposable } | undefined;

  override connectedCallback(): void {
    super.connectedCallback();
    this.requestUpdate(); // subscribes again after a reconnect
  }

  override disconnectedCallback(): void {
    super.disconnectedCallback();
    this.watched?.sub.dispose();
    this.watched = undefined;
  }

  protected override willUpdate(): void {
    if (this.watched?.doc === this.doc) return;
    this.watched?.sub.dispose();
    this.watched = this.doc && { doc: this.doc, sub: this.doc.draftFailed.subscribe(() => this.requestUpdate()) };
  }

  /** Always on the page, empty until needed: a live region added with its text isn't announced. */
  protected override render() {
    return html`<p class="pm-draft-failed" role="status" aria-label="Local copy">${this.doc?.draftFailed.value
      ? "Couldn't keep a local copy of your edits in this browser; until they save, they are only in this tab."
      : ""}</p>`;
  }
}
customElements.define("pm-draft-warning", DraftWarning);
