import { html, nothing, type PropertyValues } from "lit";
import { Trash } from "../core/trash";
import { listTrash, restoreTrashed, type TrashItem } from "../core/trash-api";
import { isNote } from "../core/tree";
import type { Workspace } from "../core/workspace";
import { fields, LightElement, openNote, StoreController } from "./base";

/**
 * The trash (ADR-0024): what was deleted, newest first, and restoring an item. Restore asks
 * where to put it (the vault root under its own name by default) and never overwrites: a
 * name clash says so, and the field stays open for another name. After a restore the
 * dialog closes and the tree shows the item; a note opens. The owner renders it to open it.
 */
export class TrashDialog extends LightElement {
  static override properties = {
    workspace: { attribute: false },
    onClose: { attribute: false },
    restoring: { state: true },
    busy: { state: true },
  };
  declare workspace: Workspace;
  declare onClose: () => void;
  /** The item whose restore form is open. */
  declare restoring: TrashItem | undefined;
  declare busy: boolean;

  private trash: Trash | undefined;
  private readonly st = new StoreController(this, () => this.trash?.state);

  constructor() {
    super();
    this.busy = false;
  }

  override connectedCallback(): void {
    const tree = this.workspace.tree;
    this.trash = new Trash({
      list: listTrash,
      restore: restoreTrashed,
      changed: (p) => tree.changed(p),
      reveal: (p) => tree.reveal(p),
    });
    super.connectedCallback(); // after: the store controller subscribes to this trash
  }

  protected override firstUpdated(): void {
    this.querySelector("dialog")?.showModal();
    void this.trash?.load();
  }

  protected override updated(changed: PropertyValues<this>): void {
    if (!changed.has("restoring")) return;
    if (this.restoring) return this.focusField();
    // The form closed (Escape or Cancel): the focus goes back to its item's Restore button.
    const was = changed.get("restoring");
    const button = [...this.querySelectorAll<HTMLElement>("[data-trash-id]")].find((b) => b.dataset["trashId"] === was?.id);
    button?.focus();
  }

  private focusField(): void {
    const input = this.querySelector<HTMLInputElement>("form.pm-trash-restore input");
    input?.focus();
    input?.select();
  }

  private close = () => this.querySelector("dialog")?.close();

  private async submit(e: SubmitEvent, item: TrashItem): Promise<void> {
    e.preventDefault();
    if (!this.trash || this.busy) return;
    this.busy = true;
    let path: string | undefined;
    try {
      path = await this.trash.restore(item, fields(e.target as HTMLFormElement)["to"] ?? "");
    } finally {
      this.busy = false;
    }
    if (path === undefined) {
      if (this.trash.state.value.items?.some((i) => i.id === item.id)) this.focusField(); // type another name
      else this.restoring = undefined;
      return;
    }
    this.close();
    if (item.kind === "file" && isNote(path)) await openNote(this.workspace, path);
  }

  private form(item: TrashItem) {
    return html`<form class="pm-move pm-trash-restore" @submit=${(e: SubmitEvent) => this.submit(e, item)}>
      <label
        >Restore ${item.name} to
        <input
          name="to"
          .value=${item.name}
          spellcheck="false"
          aria-describedby="pm-trash-hint"
          @keydown=${(e: KeyboardEvent) => {
            if (e.key !== "Escape") return;
            e.preventDefault(); // close the form, not the dialog
            this.restoring = undefined;
          }}
        />
      </label>
      <p id="pm-trash-hint">A path in the vault. A folder ending in / keeps the name: <code>ops/</code>.</p>
      <button type="submit" ?disabled=${this.busy}>Restore</button>
      <button type="button" @click=${() => (this.restoring = undefined)}>Cancel</button>
    </form>`;
  }

  private list(items: TrashItem[]) {
    if (items.length === 0) return html`<p>The trash is empty.</p>`;
    return html`<ul class="pm-draft-list" aria-label="Deleted items">
      ${items.map(
        (i) => html`<li>
          <span><strong>${i.name}</strong>${i.kind === "folder" ? " (folder)" : ""}, deleted ${new Date(i.deletedAt).toLocaleString()}</span>
          ${this.restoring?.id === i.id
            ? this.form(i)
            : html`<button type="button" data-trash-id=${i.id} aria-label="Restore ${i.name}" @click=${() => (this.restoring = i)}>Restore</button>`}
        </li>`,
      )}
    </ul>`;
  }

  protected override render() {
    const s = this.st.value;
    return html`<dialog class="pm-dialog" aria-labelledby="pm-trash-title" @close=${() => this.onClose()}>
      <div class="pm-card">
        <h1 id="pm-trash-title">Trash</h1>
        ${s?.error ? html`<p class="pm-error" role="alert">${s.error}</p>` : nothing}
        ${s?.items === undefined ? (s?.error ? nothing : html`<p>Loading…</p>`) : this.list(s.items)}
        ${s?.truncated ? html`<p>Showing the newest ${s.items?.length} items.</p>` : nothing}
        <div class="pm-dialog-actions"><button type="button" @click=${this.close}>Close</button></div>
      </div>
    </dialog>`;
  }
}
customElements.define("pm-trash", TrashDialog);
