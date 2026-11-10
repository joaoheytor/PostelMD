import { html, nothing, type TemplateResult } from "lit";
import type { Entry } from "../core/api";
import { join, parent } from "../core/tree";
import { isNote, type Workspace } from "../core/workspace";
import { LightElement, StoreController } from "./base";

type Creating = "note" | "folder" | undefined;

/**
 * The vault's files as nested lists of native buttons: folders toggle (aria-expanded),
 * notes open. New notes and folders go into the selected folder.
 */
export class FileTree extends LightElement {
  static override properties = { workspace: { attribute: false }, creating: { state: true }, folder: { state: true } };
  declare workspace: Workspace;
  declare creating: Creating;
  /** The selected folder: where a new note or folder goes. */
  declare folder: string;

  private readonly tree = new StoreController(this, () => this.workspace?.tree.state);
  private readonly ws = new StoreController(this, () => this.workspace?.state);

  constructor() {
    super();
    this.folder = "";
  }

  private async open(path: string): Promise<void> {
    this.folder = parent(path);
    if (!(await this.workspace.saveOpen()) && !confirm("Some edits to the open note aren't saved. Leave them?")) return;
    await this.workspace.open(path);
  }

  private async toggle(path: string): Promise<void> {
    this.folder = path;
    await this.workspace.tree.toggle(path);
  }

  private async create(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    const input = (e.target as HTMLFormElement).querySelector("input");
    const name = input?.value.trim() ?? "";
    if (!name) return;
    if (this.creating === "note") await this.workspace.createNote(this.folder, name);
    else await this.workspace.createFolder(this.folder, name);
    this.creating = undefined;
  }

  private entry(folder: string, e: Entry): TemplateResult {
    const path = join(folder, e.name);
    if (e.kind === "folder") {
      const open = this.tree.value?.expanded.has(path) ?? false;
      return html`<li>
        <button type="button" class="pm-tree-folder" aria-expanded=${open} @click=${() => this.toggle(path)}>${e.name}</button>
        ${open ? this.list(path) : nothing}
      </li>`;
    }
    if (e.kind === "file" && isNote(e.name)) {
      const current = this.ws.value?.open?.doc.path === path;
      return html`<li>
        <button type="button" class="pm-tree-note" aria-current=${current ? "page" : "false"} @click=${() => this.open(path)}>${e.name}</button>
      </li>`;
    }
    return html`<li><span class="pm-tree-other">${e.name}</span></li>`;
  }

  private list(folder: string): TemplateResult {
    const entries = this.tree.value?.folders.get(folder);
    if (!entries) return html`<p class="pm-tree-loading">Loading…</p>`;
    if (entries.length === 0) return html`<p class="pm-tree-empty">Empty folder</p>`;
    return html`<ul>
      ${entries.map((e) => this.entry(folder, e))}
    </ul>`;
  }

  protected override render() {
    const error = this.tree.value?.error;
    const where = this.folder === "" ? "the vault" : this.folder;
    return html`
      <nav aria-label="Notes">
        <div class="pm-tree-actions">
          <button type="button" @click=${() => (this.creating = "note")}>New note</button>
          <button type="button" @click=${() => (this.creating = "folder")}>New folder</button>
        </div>
        ${this.creating
          ? html`<form class="pm-tree-create" @submit=${this.create}>
              <label>${this.creating === "note" ? "Note" : "Folder"} name in ${where}
                <input name="name" required autofocus @keydown=${(e: KeyboardEvent) => e.key === "Escape" && (this.creating = undefined)} />
              </label>
              <button type="submit">Create</button>
              <button type="button" @click=${() => (this.creating = undefined)}>Cancel</button>
            </form>`
          : nothing}
        ${error ? html`<p class="pm-error" role="alert">${error}</p>` : nothing}
        <button type="button" class="pm-tree-root" aria-pressed=${this.folder === ""} @click=${() => (this.folder = "")}>Vault</button>
        ${this.list("")}
      </nav>
    `;
  }
}
customElements.define("pm-tree", FileTree);
