import { html, nothing, type PropertyValues, type TemplateResult } from "lit";
import type { Entry } from "../core/api";
import { nameProblem, noteFile } from "../core/names";
import type { Template } from "../core/templates";
import { join, parent } from "../core/tree";
import type { Workspace } from "../core/workspace";
import { LightElement, openNote, refocus, StoreController } from "./base";
import { destination, dragType, type ItemKind, moveInto } from "../core/move-into";
import "./move";
import "./move-to";
import { chosen, templateFields, values } from "./template-fields";

type Creating = "note" | "folder" | undefined;

/**
 * The vault's files as nested lists of native buttons: folders toggle (aria-expanded),
 * notes open, and the open note is marked (aria-current) and scrolled into view. New notes
 * and folders go into the selected folder.
 */
export class FileTree extends LightElement {
  static override properties = {
    workspace: { attribute: false },
    creating: { state: true },
    moving: { state: true },
    folder: { state: true },
    template: { state: true },
    problem: { state: true },
    over: { state: true },
  };
  declare workspace: Workspace;
  declare creating: Creating;
  /** The rename-or-move form for the selected folder is open. */
  declare moving: boolean;
  /** The selected folder: where a new note or folder goes. */
  declare folder: string;
  /** The new-note form's chosen template; undefined for a blank note. */
  declare template: Template | undefined;
  /** Why the typed note name can't be used, shown in the form. */
  declare problem: string | undefined;
  /** The folder a dragged item would drop into, highlighted; undefined for none. */
  declare over: string | undefined;

  private readonly tree = new StoreController(this, () => this.workspace?.tree.state);
  private readonly ws = new StoreController(this, () => this.workspace?.state);
  /** The button that opened the inline form: it gets the focus back when the form closes. */
  private opener: Element | null = null;
  /** The open note last scrolled into view. */
  private shown: string | undefined;
  /** The item being dragged, if the drag started in this tree: no other drag moves anything. */
  private dragging: { path: string; kind: ItemKind } | undefined;

  constructor() {
    super();
    this.folder = "";
    this.moving = false;
  }

  /** Selects folder and focuses its entry (the Vault button for the root), e.g. after a delete. */
  async focusFolder(folder: string): Promise<void> {
    this.folder = folder;
    await this.updateComplete;
    const at = [...this.querySelectorAll<HTMLElement>("[data-folder]")].find((b) => b.dataset["folder"] === folder);
    (at ?? this.querySelector<HTMLElement>(".pm-tree-root"))?.focus();
  }

  protected override updated(changed: PropertyValues<this>): void {
    if (changed.has("creating") && this.creating) this.querySelector<HTMLInputElement>("form.pm-tree-create input[name=name]")?.focus();
    const open = this.ws.value?.open?.doc.path;
    if (open === this.shown) return;
    const current = this.querySelector('.pm-tree-note[aria-current="page"]');
    if (open && !current) return; // its folder is still loading: try again on the next render
    current?.scrollIntoView({ block: "nearest" });
    this.shown = open;
  }

  private start(what: "note" | "folder" | "move", e: Event): void {
    this.opener = e.currentTarget as Element;
    this.workspace.tree.announce(""); // the last status is old news once the tree is used again
    if (what === "move") this.moving = true;
    else this.creating = what;
  }

  /** Opens a file; its folder is selected only once it opened: a refused one leaves the selection (#134). */
  private async open(path: string): Promise<void> {
    await openNote(this.workspace, path);
    if (this.workspace.state.value.open?.doc.path === path) this.select(parent(path));
  }

  private async toggle(path: string): Promise<void> {
    this.select(path);
    await this.workspace.tree.toggle(path);
  }

  /** The user picks a folder (or a note in it): the last status is cleared. */
  private select(folder: string): void {
    this.folder = folder;
    this.workspace.tree.announce("");
  }

  private async create(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    const input = (e.target as HTMLFormElement).querySelector("input");
    const name = input?.value.trim() ?? "";
    if (!name) return;
    const form = e.target as HTMLFormElement;
    const template = chosen(form);
    if (this.creating === "note") {
      this.problem = nameProblem(noteFile(name));
      if (!this.problem && (await this.workspace.createNote(this.folder, name, template && { template, values: values(form) }))) {
        this.creating = this.template = undefined; // the new note's editor has the focus
        return;
      }
      // Not created: the form keeps what was typed, and the name gets the focus to fix it.
      await this.updateComplete;
      input?.focus();
      return;
    }
    await this.workspace.createFolder(this.folder, name);
    this.cancel();
  }

  private cancel(): void {
    this.creating = undefined;
    this.template = this.problem = undefined;
    void refocus(this, this.opener);
  }

  /**
   * Moves the selected folder and everything in it to the trash, after asking. An open note
   * inside it is saved first, so the trash holds its latest text. Once it went, the status
   * says so and the parent folder takes the focus, as after a note's delete; if it didn't,
   * the error says why and the focus stays.
   */
  private async trashFolder(): Promise<void> {
    const folder = this.folder;
    this.workspace.tree.announce("");
    if (!confirm(`Move the folder ${folder} and everything in it to the trash? You can restore it from the Trash.`)) return;
    const open = this.workspace.state.value.open?.doc.path;
    if (open?.startsWith(`${folder}/`) && !(await this.workspace.saveOpen()) && !confirm(`Some edits to ${open} aren't saved. Delete the folder anyway?`)) {
      return;
    }
    if (!(await this.workspace.trash(folder, "folder"))) return;
    this.workspace.tree.announce(`Moved the folder ${folder} to the trash.`);
    await this.focusFolder(parent(folder));
  }

  // Drag and drop (#130): an item dragged from this tree onto a folder, or onto Vault, moves into
  // it (moveInto). Lit binds these handlers to the nav and removes them with it (rule 8).

  private dragStart(e: DragEvent): void {
    const item = (e.target as Element).closest<HTMLElement>("li[data-path]");
    const path = item?.dataset["path"];
    if (!item || path === undefined || !e.dataTransfer) return;
    this.dragging = { path, kind: item.dataset["kind"] === "folder" ? "folder" : "file" };
    e.dataTransfer.setData(dragType, path);
    e.dataTransfer.effectAllowed = "move";
  }

  /** The folder under a drag from this tree that its item can move into; undefined for none. */
  private dropTarget(e: DragEvent): string | undefined {
    if (!this.dragging || !e.dataTransfer?.types.includes(dragType)) return undefined; // e.g. a file from the desktop
    const folder = (e.target as Element).closest<HTMLElement>("[data-folder]")?.dataset["folder"];
    if (folder === undefined) return undefined;
    const d = destination(this.dragging.path, this.dragging.kind, folder);
    return d && "to" in d ? folder : undefined;
  }

  private dragOver(e: DragEvent): void {
    const folder = this.dropTarget(e);
    this.over = folder;
    if (folder === undefined || !e.dataTransfer) return; // not a drop target: the browser shows so
    e.preventDefault();
    e.dataTransfer.dropEffect = "move";
  }

  private async drop(e: DragEvent): Promise<void> {
    const folder = this.dropTarget(e);
    const item = this.dragging;
    this.dragEnd();
    if (folder === undefined || !item) return;
    e.preventDefault();
    const to = await moveInto(this.workspace, item.path, item.kind, folder);
    if (to !== undefined && item.kind === "folder") this.folder = to;
  }

  private dragLeave(e: DragEvent): void {
    if (!(e.relatedTarget instanceof Node && this.contains(e.relatedTarget))) this.over = undefined;
  }

  private dragEnd(): void {
    this.dragging = this.over = undefined;
  }

  /** A drop target's class, highlighted while a drag is over it. */
  private dropClass(base: string, folder: string): string {
    return this.over === folder ? `${base} pm-tree-drop` : base;
  }

  private entry(folder: string, e: Entry): TemplateResult {
    const path = join(folder, e.name);
    if (e.kind === "folder") {
      const open = this.tree.value?.expanded.has(path) ?? false;
      return html`<li draggable="true" data-path=${path} data-kind="folder">
        <button type="button" class=${this.dropClass("pm-tree-folder", path)} data-folder=${path} aria-expanded=${open} @click=${() => this.toggle(path)}>
          ${e.name}
        </button>
        ${open ? this.list(path) : nothing}
      </li>`;
    }
    // Every file is a button: one the editor can't hold says why when opened (textfile.ts).
    if (e.kind === "file") {
      const current = this.ws.value?.open?.doc.path === path;
      return html`<li draggable="true" data-path=${path} data-kind="file">
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
      <nav
        aria-label="Notes"
        @dragstart=${this.dragStart}
        @dragover=${this.dragOver}
        @dragleave=${this.dragLeave}
        @drop=${this.drop}
        @dragend=${this.dragEnd}
      >
        <div class="pm-tree-actions">
          <button type="button" @click=${(e: Event) => this.start("note", e)}>New note</button>
          <button type="button" @click=${(e: Event) => this.start("folder", e)}>New folder</button>
          ${this.folder === ""
            ? nothing
            : html`<button type="button" @click=${(e: Event) => this.start("move", e)}>Rename or move folder</button>
                <pm-move-to
                  .workspace=${this.workspace}
                  .path=${this.folder}
                  .kind=${"folder"}
                  .label=${"Move folder to…"}
                  .onDone=${(to: string) => (this.folder = to)}
                ></pm-move-to>
                <button type="button" @click=${() => this.trashFolder()}>Delete folder ${this.folder}</button>`}
        </div>
        <p class="pm-tree-status" role="status" aria-label="File tree status">${this.tree.value?.status ?? ""}</p>
        ${this.moving && this.folder !== ""
          ? html`<pm-move
              .workspace=${this.workspace}
              .path=${this.folder}
              .kind=${"folder"}
              .onDone=${(to?: string) => {
                this.moving = false;
                if (to) this.folder = to;
                void refocus(this, this.opener);
              }}
            ></pm-move>`
          : nothing}
        ${this.creating
          ? html`<form class="pm-tree-create" @submit=${this.create}>
              <label>${this.creating === "note" ? "Note" : "Folder"} name in ${where}
                <input
                  name="name"
                  required
                  aria-invalid=${this.problem ? "true" : "false"}
                  aria-describedby=${this.problem ? "pm-tree-name-problem" : nothing}
                  @input=${() => (this.problem = undefined)}
                  @keydown=${(e: KeyboardEvent) => e.key === "Escape" && this.cancel()}
                />
              </label>
              ${this.problem ? html`<p id="pm-tree-name-problem" class="pm-error" role="alert">${this.problem}</p>` : nothing}
              ${this.creating === "note"
                ? templateFields(this.template, () => (this.template = chosen(this.querySelector("form.pm-tree-create") as HTMLFormElement)))
                : nothing}
              <button type="submit">Create</button>
              <button type="button" @click=${() => this.cancel()}>Cancel</button>
            </form>`
          : nothing}
        ${error ? html`<p class="pm-error" role="alert">${error}</p>` : nothing}
        <button type="button" class=${this.dropClass("pm-tree-root", "")} data-folder="" aria-pressed=${this.folder === ""} @click=${() => this.select("")}>
          Vault
        </button>
        ${this.list("")}
      </nav>
    `;
  }
}
customElements.define("pm-tree", FileTree);
