import { html, nothing } from "lit";
import { keyed } from "lit/directives/keyed.js";
import type { DocumentSession } from "../core/document";
import { type Disposable, DisposableStore } from "../core/disposable";
import { shownState, statusText } from "../core/save-status";
import type { OpenNote, Workspace } from "../core/workspace";
import { type Editor, mountEditor } from "../editor/editor";
import { Preview } from "../preview/pane";
import { ScrollSync } from "../preview/sync";
import { followLink, LightElement, listen, StoreController } from "./base";
import { conflictBanner } from "./conflict";
import "./drafts";
import "./history";
import "./move";

/** Whether the preview is shown, remembered in this browser only. */
const previewKey = "postelmd.preview";

function previewPref(): boolean {
  try {
    return localStorage.getItem(previewKey) === "on";
  } catch {
    return false; // storage blocked: start hidden
  }
}

/**
 * The open note: CodeMirror in its own shadow root (ADR-0019), the save status, and the
 * conflict and deleted-note banners, and the live preview beside it. Edits save on a
 * debounce, on blur, on Ctrl+S, and when the tab is hidden.
 */
export class NoteEditor extends LightElement {
  static override properties = {
    workspace: { attribute: false },
    afterDelete: { attribute: false },
    moving: { state: true },
    moveTyped: { state: true },
    browsing: { state: true },
    previewing: { state: true },
  };
  declare workspace: Workspace;
  /** Called with the path of a note just moved to the trash: the owner moves the focus. */
  declare afterDelete: ((path: string) => void) | undefined;
  /** The rename-or-move form is open. */
  declare moving: boolean;
  /** What the form held when a move failed: the form is rendered again when the note reopens. */
  declare moveTyped: string | undefined;
  /** The history dialog is open. */
  declare browsing: boolean;
  /** The preview is shown. While it is hidden, nothing renders it (CLAUDE.md rule 9). */
  declare previewing: boolean;

  private readonly ws = new StoreController(this, () => this.workspace?.state);
  private readonly secrets = new StoreController(this, () => this.workspace?.secrets.warning);
  private editor: Editor | undefined;
  private mounted: { note: OpenNote; host: Element } | undefined;
  private docSub: Disposable | undefined;
  private preview: { pane: Preview; sync: ScrollSync; el: Element } | undefined;
  private listeners: DisposableStore | undefined;
  private draftSub: { doc: DocumentSession; sub: Disposable } | undefined;

  constructor() {
    super();
    this.moving = false;
    this.browsing = false;
    this.previewing = previewPref();
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
        const doc = this.ws.value?.open?.doc;
        if (document.visibilityState !== "hidden" || !doc) return;
        void doc.writeDraft(); // first: the page may be gone before the save answers
        void doc.flush();
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
    this.draftSub?.sub.dispose();
    this.draftSub = undefined;
  }

  private unmount(): void {
    this.closePreview();
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
    this.mountEditor();
    this.mountPreview();
    this.watchDraft();
  }

  /** Puts the focus in the note's text (a dialog over it closed). */
  focusText(): void {
    this.editor?.focus();
  }

  /** The status line says the edits are kept only while a draft that outlives the tab holds them. */
  private keptLocally(doc: DocumentSession): boolean {
    return !!this.workspace.drafts?.persistent && !doc.draftFailed.value;
  }

  /** Renders again when the open note's draft starts or stops failing. */
  private watchDraft(): void {
    const doc = this.mounted?.note.doc;
    if (this.draftSub?.doc === doc) return;
    this.draftSub?.sub.dispose();
    this.draftSub = doc && { doc, sub: doc.draftFailed.subscribe(() => this.requestUpdate()) };
  }

  private mountEditor(): void {
    const note = this.ws.value?.open;
    const host = this.querySelector(".pm-editor-host");
    if (note === this.mounted?.note && host === this.mounted?.host) return;
    this.unmount();
    if (!note || !host) return;
    const editor = mountEditor(host.attachShadow({ mode: "open" }), {
      doc: note.initial,
      label: `Text of ${note.doc.path}`,
      onEdit: () => note.doc.edit(),
      onChange: () => this.preview?.pane.update(() => editor.text()),
      onPaste: () => this.workspace.pasted(),
    });
    this.editor = editor;
    this.mounted = { note, host };
    this.workspace.reader = () => editor.text();
    this.docSub = note.doc.state.subscribe(() => this.requestUpdate());
    editor.focus();
  }

  /** Starts the preview when it is shown (or its element changed), and stops it when hidden. */
  private mountPreview(): void {
    const el = this.previewing ? this.querySelector<HTMLElement>(".pm-preview") : null;
    if (el === (this.preview?.el ?? null)) return;
    this.closePreview();
    const host = this.mounted?.host;
    if (!el || !this.editor || !(host instanceof HTMLElement)) return;
    const sync = new ScrollSync(this.editor, host, el);
    const pane = new Preview(el, () => sync.rendered());
    this.preview = { pane, sync, el };
    pane.show(this.editor.text());
  }

  private closePreview(): void {
    this.preview?.sync.dispose();
    this.preview?.pane.dispose();
    this.preview = undefined;
  }

  private togglePreview(): void {
    this.previewing = !this.previewing;
    try {
      localStorage.setItem(previewKey, this.previewing ? "on" : "off");
    } catch {
      // storage blocked: the choice lasts until reload
    }
  }

  /** ADR-0020: a warning only. It names each finding by rule, line and a short hint, never the secret. */
  private secretWarning() {
    const w = this.secrets.value;
    if (!w || w.dismissed || w.findings.length === 0) return nothing;
    return html`<section class="pm-banner" role="alert" aria-labelledby="pm-secret-title">
      <h3 id="pm-secret-title">This note looks like it contains a secret</h3>
      <ul>
        ${w.findings.map((f) => html`<li>${f.rule} on line ${f.line} (<code>${f.hint}</code>)</li>`)}
      </ul>
      <p>It is saved in the note, and a commit would keep it in the git history. Remove it, and rotate it if it is real.</p>
      <button type="button" @click=${() => this.workspace.secrets.dismiss()}>Dismiss</button>
    </section>`;
  }

  private takeTheirs(note: OpenNote): void {
    this.editor?.replace(note.doc.takeTheirs());
  }

  private async trash(path: string): Promise<void> {
    if (!confirm(`Move ${path} to the trash?`)) return;
    await this.workspace.trash(path, "file");
    this.afterDelete?.(path); // the Delete button is gone with the note
  }

  private startMove(): void {
    this.moveTyped = undefined;
    this.moving = true;
  }

  /**
   * A move closes the form and the moved note's editor takes the focus (the form, rendered
   * again while the note reopened, had taken it); a cancel gives it back to the Rename or move
   * button (a new one if a failed move reopened the note).
   */
  private moveDone(to?: string): void {
    this.moving = false;
    this.moveTyped = undefined;
    void this.updateComplete.then(() => (to ? this.editor?.focus() : this.querySelector<HTMLElement>(".pm-editor-move")?.focus()));
  }

  /**
   * The dialog gives the focus back to its opener, unless the opener went away meanwhile (a
   * restore reopens the note): then the editor gets it.
   */
  private historyClosed(): void {
    this.browsing = false;
    void this.updateComplete.then(() => {
      const at = document.activeElement;
      if (!at || at === document.body) this.editor?.focus();
    });
  }

  protected override render() {
    const note = this.ws.value?.open;
    if (!note) return html`<p class="pm-editor-empty">Open a note from the list, or create one.</p>`;
    const st = shownState(note.doc.state.value, this.keptLocally(note.doc));
    return html`
      <header class="pm-editor-bar">
        <h2 class="pm-editor-path">${note.doc.path}</h2>
        <span class="pm-editor-status" data-status=${st.status} role="status" aria-label="Save status"
          >${statusText[st.status]}${st.message ? `: ${st.message}` : ""}</span
        >
        <button type="button" aria-pressed=${this.previewing} @click=${this.togglePreview}>Preview</button>
        <button type="button" @click=${() => (this.browsing = true)}>History</button>
        <button type="button" class="pm-editor-move" @click=${this.startMove}>Rename or move</button>
        <button type="button" @click=${() => this.trash(note.doc.path)}>Delete</button>
      </header>
      ${this.browsing ? html`<pm-history .workspace=${this.workspace} .onClose=${() => this.historyClosed()}></pm-history>` : nothing}
      ${this.moving
        ? html`<pm-move
            .workspace=${this.workspace}
            .path=${note.doc.path}
            .kind=${"file"}
            .value=${this.moveTyped}
            .onDone=${(to?: string) => this.moveDone(to)}
            .onFailed=${(typed: string) => (this.moveTyped = typed)}
          ></pm-move>`
        : nothing}
      ${st.status === "conflict" && st.conflict
        ? conflictBanner(
            st.conflict,
            () => this.editor?.text() ?? note.initial,
            () => note.doc.keepMine(),
            () => this.takeTheirs(note),
          )
        : nothing}
      ${this.secretWarning()}
      <pm-draft-warning .doc=${note.doc}></pm-draft-warning>
      <pm-draft-banner .workspace=${this.workspace} .put=${(t: string) => this.editor?.replace(t)}></pm-draft-banner>
      ${st.status === "gone"
        ? html`<section class="pm-banner" role="alert" aria-labelledby="pm-gone-title">
            <h3 id="pm-gone-title">This note was deleted or moved outside this editor</h3>
            <p>Your text is still here, but it isn't saved anywhere.</p>
            <button type="button" @click=${() => this.workspace.saveAsNew()}>Save it as a new note</button>
          </section>`
        : nothing}
      <div class="pm-editor-panes">
        ${keyed(note, html`<div class="pm-editor-host" @focusout=${() => void note.doc.flush()}></div>`)}
        ${this.previewing
          ? keyed(
              note,
              html`<section
                class="pm-preview"
                aria-label="Preview of ${note.doc.path}"
                @click=${(e: MouseEvent) => followLink(this.workspace, e, note.doc.path)}
              ></section>`,
            )
          : nothing}
      </div>
    `;
  }
}
customElements.define("pm-editor", NoteEditor);
