import { html, nothing } from "lit";
import type { App } from "../core/app";
import { LightElement, StoreController } from "./base";
import "./editor";
import "./login";
import "./setup";
import "./tree";

/** The workspace: the file tree beside the open note. */
export class WorkspaceView extends LightElement {
  static override properties = { app: { attribute: false } };
  declare app: App;

  private readonly ws = new StoreController(this, () => this.app?.workspace.state);

  protected override firstUpdated(): void {
    const w = this.app.workspace;
    void w.tree.load("");
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
    return html`
      <header class="pm-top">
        <span class="pm-top-name">PostelMD</span>
        <button type="button" @click=${() => this.app.logout()}>Log out</button>
      </header>
      ${error
        ? html`<p class="pm-error pm-top-error" role="alert">
            ${error} <button type="button" @click=${() => w.dismissError()}>Dismiss</button>
          </p>`
        : nothing}
      <div class="pm-main">
        <pm-tree class="pm-side" .workspace=${w}></pm-tree>
        <main class="pm-note"><pm-editor .workspace=${w}></pm-editor></main>
      </div>
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
        return html`<pm-setup .app=${this.app}></pm-setup>`;
      case "login":
        return html`<pm-login .app=${this.app}></pm-login>`;
      case "ready":
        return html`
          <pm-workspace .app=${this.app}></pm-workspace>
          ${s.reauth
            ? html`<dialog class="pm-reauth" @cancel=${(e: Event) => e.preventDefault()}><pm-login .app=${this.app}></pm-login></dialog>`
            : nothing}
        `;
    }
  }
}
customElements.define("pm-app", AppView);
