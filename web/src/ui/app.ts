import { html, nothing } from "lit";
import type { App } from "../core/app";
import { DisposableStore } from "../core/disposable";
import { parent } from "../core/tree";
import { LightElement, listen, StoreController } from "./base";
import "./commit";
import "./drafts";
import "./finder";
import "./editor";
import type { NoteEditor } from "./editor";
import "./leave";
import "./login";
import "./setup";
import "./trash";
import type { FileTree } from "./tree";
import "./tree";

/** The workspace: the file tree beside the open note. */
export class WorkspaceView extends LightElement {
  static override properties = {
    app: { attribute: false },
    committing: { state: true },
    listingDrafts: { state: true },
    finding: { state: true },
    inTrash: { state: true },
  };
  declare app: App;
  /** The trash dialog is open. */
  declare inTrash: boolean;
  /** The commit dialog is open. */
  declare committing: boolean;
  /** The unsaved-drafts dialog is open. */
  declare listingDrafts: boolean;
  /** The finder is open. */
  declare finding: boolean;
  private listeners: DisposableStore | undefined;

  private readonly ws = new StoreController(this, () => this.app?.workspace.state);
  private readonly offered = new StoreController(this, () => this.app?.workspace.offered);
  private readonly appState = new StoreController(this, () => this.app?.state);

  constructor() {
    super();
    this.committing = false;
    this.listingDrafts = false;
    this.finding = false;
    this.inTrash = false;
  }

  override connectedCallback(): void {
    super.connectedCallback();
    this.listeners = new DisposableStore();
    // Ctrl+P (the browser's print) opens the finder, from the editor too.
    this.listeners.add(
      listen(window, "keydown", (e) => {
        if ((e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey && e.key.toLowerCase() === "p") {
          e.preventDefault();
          this.finding = true;
        }
      }),
    );
  }

  override disconnectedCallback(): void {
    super.disconnectedCallback();
    this.listeners?.dispose();
  }

  protected override firstUpdated(): void {
    const w = this.app.workspace;
    void w.tree.load("");
    void w.refreshOffered();
    // A reload keeps the open note: its path is in the URL.
    const m = /^\/notes\/(.+)$/.exec(location.pathname);
    if (m?.[1]) void w.open(m[1].split("/").map(decodeURIComponent).join("/"));
  }

  /** The URL follows the open note, however it was opened (tree, new note, save as new). */
  protected override updated(): void {
    const path = this.ws.value?.open?.doc.path;
    const url = path ? `/notes/${path.split("/").map(encodeURIComponent).join("/")}` : "/";
    if (location.pathname !== url) history.replaceState(null, "", url);
  }

  protected override render() {
    const w = this.app.workspace;
    const error = this.ws.value?.error;
    const notice = this.appState.value?.notice;
    return html`
      <header class="pm-top">
        <h1 class="pm-top-name">PostelMD</h1>
        <button type="button" aria-keyshortcuts="Control+P" @click=${() => (this.finding = true)}>Find a note</button>
        ${this.offered.value?.length
          ? html`<button type="button" @click=${() => (this.listingDrafts = true)}>Unsaved drafts (${this.offered.value.length})</button>`
          : nothing}
        <button type="button" @click=${() => (this.inTrash = true)}>Trash</button>
        <button type="button" @click=${() => (this.committing = true)}>Commit…</button>
        <!-- aria-disabled, not disabled: the button keeps the focus, so its new label is read
             out; logout() ignores clicks while it runs. -->
        <button type="button" aria-disabled=${this.appState.value?.loggingOut ? "true" : "false"} @click=${() => this.app.logout()}>
          ${this.appState.value?.loggingOut ? "Saving before you log out…" : "Log out"}
        </button>
      </header>
      ${error
        ? html`<p class="pm-error pm-top-error" role="alert">
            ${error} <button type="button" @click=${() => w.dismissError()}>Dismiss</button>
          </p>`
        : nothing}
      ${notice
        ? html`<p class="pm-top-notice" role="status">
            ${notice} <button type="button" @click=${() => this.app.dismissNotice()}>Dismiss</button>
          </p>`
        : nothing}
      <div class="pm-main">
        <pm-tree class="pm-side" .workspace=${w}></pm-tree>
        <main class="pm-note">
          <pm-editor
            .workspace=${w}
            .afterDelete=${(path: string) => this.querySelector<FileTree>("pm-tree")?.focusFolder(parent(path))}
          ></pm-editor>
        </main>
      </div>
      ${this.committing
        ? html`<pm-commit .git=${this.app.git} .onClose=${() => (this.committing = false)}></pm-commit>`
        : nothing}
      ${this.finding ? html`<pm-finder .workspace=${w} .onClose=${() => (this.finding = false)}></pm-finder>` : nothing}
      ${this.listingDrafts ? html`<pm-drafts .workspace=${w} .onClose=${() => (this.listingDrafts = false)}></pm-drafts>` : nothing}
      ${this.inTrash ? html`<pm-trash .workspace=${w} .onClose=${() => (this.inTrash = false)}></pm-trash>` : nothing}
    `;
  }
}
customElements.define("pm-workspace", WorkspaceView);

/** The top level: one screen per phase; an expired session asks over the open work. */
export class AppView extends LightElement {
  static override properties = { app: { attribute: false } };
  declare app: App;

  private readonly st = new StoreController(this, () => this.app?.state);

  protected override updated(): void {
    const dialog = this.querySelector<HTMLDialogElement>("dialog.pm-reauth");
    if (dialog && !dialog.open) dialog.showModal();
  }

  protected override render() {
    const s = this.st.value;
    switch (s?.phase) {
      case undefined:
      case "loading":
        return html`<p class="pm-loading">Loading…</p>`;
      case "failed":
        return html`<p class="pm-error" role="alert">${s.error}</p>`;
      case "setup":
        return html`<main><pm-setup .app=${this.app}></pm-setup></main>`;
      case "login":
        return html`<main><pm-login .app=${this.app}></pm-login></main>`;
      case "ready":
        return html`
          <pm-workspace .app=${this.app}></pm-workspace>
          ${s.leaving
            ? html`<pm-leave
                .app=${this.app}
                .leaving=${s.leaving}
                .afterStay=${() => this.querySelector<NoteEditor>("pm-editor")?.focusText()}
              ></pm-leave>`
            : s.reauth
              ? html`<dialog class="pm-reauth" @cancel=${(e: Event) => e.preventDefault()}><pm-login .app=${this.app}></pm-login></dialog>`
              : nothing}
        `;
    }
  }
}
customElements.define("pm-app", AppView);
