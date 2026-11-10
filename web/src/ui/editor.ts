import { html, nothing } from "lit";
import { keyed } from "lit/directives/keyed.js";
import type { DocState } from "../core/document";
import { type Disposable, DisposableStore } from "../core/disposable";
import type { OpenNote, Workspace } from "../core/workspace";
import { type Editor, mountEditor } from "../editor/editor";
import { LightElement, listen, StoreController } from "./base";
import "./history";
import "./move";

const statusText: Record<DocState["status"], string> = {
  clean: "Saved",
  dirty: "Edited",
  saving: "Saving…",
  error: "Not saved",
  conflict: "Not saved: conflict",
  gone: "Not saved: the note is gone",
};

/**
 * The open note: CodeMirror in its own shadow root (ADR-0019), the save status, and the
 * conflict and deleted-note banners. Edits save on a debounce, on blur, on Ctrl+S, and when
 * the tab is hidden.
 */
export class NoteEditor extends LightElement {
  static override properties = { workspace: { attribute: false }, moving: { state: true }, browsing: { state: true } };
  declare workspace: Workspace;
  /** The rename-or-move form is open. */
  declare moving: boolean;
  /** The history dialog is open. */
  declare browsing: boolean;

  private readonly ws = new StoreController(this, () => this.workspace?.state);
  private editor: Editor | undefined;
  private mounted: { note: OpenNote; host: Element } | undefined;
  private docSub: Disposable | undefined;
  private listeners: DisposableStore | undefined;

  constructor() {
    super();
    this.moving = false;
    this.browsing = false;
  }

  override connectedCallback(): void {
    super.connectedCallback();
    const l = (this.listeners = new DisposableStore());
    l.add(
      listen(window, "beforeunload", (e) => {
        if (this.ws.value?.open?.doc.dirty) e.preventDefault(); // the browser asks first
      }),
    );
    l.add(
      listen(document, "visibilitychange", () => {
        if (document.visibilityState === "hidden") void this.ws.value?.open?.doc.flush();
      }),
    );
    l.add(
      listen(window, "keydown", (e) => {
        if ((e.ctrlKey || e.metaKey) && e.key === "s") {
          e.preventDefault();
          void this.ws.value?.open?.doc.flush();
        }
      }),
    );
  }

  override disconnectedCallback(): void {
    super.disconnectedCallback();
    this.listeners?.dispose();
    this.unmount();
  }

  private unmount(): void {
    this.docSub?.dispose();
    this.editor?.dispose();
    this.docSub = this.editor = this.mounted = undefined;
    if (this.workspace) this.workspace.reader = undefined;
  }

  /**
   * Mounts a new editor when the open note changes. keyed() gives each note a new host
   * element, since an element's shadow root can't be replaced.
   */
  protected override updated(): void {
    const note = this.ws.value?.open;
    const host = this.querySelector(".pm-editor-host");
    if (note === this.mounted?.note && host === this.mounted?.host) return;
    this.unmount();
    if (!note || !host) return;
    const editor = mountEditor(host.attachShadow({ mode: "open" }), note.initial, `Text of ${note.doc.path}`, () =>
      note.doc.edit(),
    );
    this.editor = editor;
    this.mounted = { note, host };
    this.workspace.reader = () => editor.text();
    this.docSub = note.doc.state.subscribe(() => this.requestUpdate());
    editor.focus();
  }

  private takeTheirs(note: OpenNote): void {
    this.editor?.replace(note.doc.takeTheirs());
  }

  private async trash(path: string): Promise<void> {
    if (confirm(`Move ${path} to the trash?`)) await this.workspace.trash(path, "file");
  }

  protected override render() {
    const note = this.ws.value?.open;
    if (!note) return html`<p class="pm-editor-empty">Open a note from the list, or create one.</p>`;
    const st = note.doc.state.value;
    return html`
      <header class="pm-editor-bar">
        <h2 class="pm-editor-path">${note.doc.path}</h2>
        <span class="pm-editor-status" data-status=${st.status} role="status"
          >${statusText[st.status]}${st.message ? `: ${st.message}` : ""}</span
        >
        <button type="button" @click=${() => (this.browsing = true)}>History</button>
        <button type="button" @click=${() => (this.moving = true)}>Rename or move</button>
        <button type="button" @click=${() => this.trash(note.doc.path)}>Delete</button>
      </header>
      ${this.browsing
        ? html`<pm-history .workspace=${this.workspace} .onClose=${() => (this.browsing = false)}></pm-history>`
        : nothing}
      ${this.moving
        ? html`<pm-move .workspace=${this.workspace} .path=${note.doc.path} .kind=${"file"} .onDone=${() => (this.moving = false)}></pm-move>`
        : nothing}
      ${st.status === "conflict" && st.conflict
        ? html`<section class="pm-banner" role="alert" aria-labelledby="pm-conflict-title">
            <h3 id="pm-conflict-title">This note changed outside this editor</h3>
            <p>Your edits aren't saved. Keep yours, which replaces the other version, or use the other version, which replaces your edits (Ctrl+Z brings them back).</p>
            <details>
              <summary>Show the other version</summary>
              <pre>${st.conflict.content}</pre>
            </details>
            <button type="button" @click=${() => note.doc.keepMine()}>Keep my version</button>
            <button type="button" @click=${() => this.takeTheirs(note)}>Use the other version</button>
          </section>`
        : nothing}
      ${st.status === "gone"
        ? html`<section class="pm-banner" role="alert" aria-labelledby="pm-gone-title">
            <h3 id="pm-gone-title">This note was deleted or moved outside this editor</h3>
            <p>Your text is still here, but it isn't saved anywhere.</p>
            <button type="button" @click=${() => this.workspace.saveAsNew()}>Save it as a new note</button>
          </section>`
        : nothing}
      ${keyed(note, html`<div class="pm-editor-host" @focusout=${() => void note.doc.flush()}></div>`)}
    `;
  }
}
customElements.define("pm-editor", NoteEditor);
