import { html, nothing } from "lit";
import { folderChoices, type ItemKind, moveInto } from "../core/move-into";
import type { Workspace } from "../core/workspace";
import { fields, LightElement } from "./base";

/**
 * "Move to…": the keyboard's way to do what a drag in the file tree does (#130). A button that
 * opens a modal dialog listing the folders path can go into; Move moves it there (moveInto),
 * and the tree's status says so. A failed move keeps the dialog open with the error. Closing
 * gives the focus back to the button (the native dialog does it).
 */
export class MoveToDialog extends LightElement {
  static override properties = {
    workspace: { attribute: false },
    path: { attribute: false },
    kind: { attribute: false },
    label: { attribute: false },
    onDone: { attribute: false },
    open: { state: true },
    busy: { state: true },
    problem: { state: true },
  };
  declare workspace: Workspace;
  declare path: string;
  declare kind: ItemKind;
  /** The button's text. */
  declare label: string;
  /** Called with the new path after a move. */
  declare onDone: ((to: string) => void) | undefined;
  declare open: boolean;
  declare busy: boolean;
  declare problem: string | undefined;
  /** The folders offered: read when the dialog opens, so a tree change meanwhile doesn't reorder them. */
  private choices: string[] = [];

  constructor() {
    super();
    this.open = false;
    this.busy = false;
  }

  private start(): void {
    this.choices = folderChoices(this.workspace.tree.state.value, this.path, this.kind);
    this.problem = undefined;
    this.open = true;
  }

  protected override updated(): void {
    const dialog = this.querySelector("dialog");
    if (this.open && dialog && !dialog.open) dialog.showModal();
  }

  private close = () => this.querySelector("dialog")?.close();

  private async submit(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    const folder = fields(e.target as HTMLFormElement)["folder"];
    if (folder === undefined) return;
    this.busy = true;
    try {
      const to = await moveInto(this.workspace, this.path, this.kind, folder);
      if (to !== undefined) {
        this.close();
        this.onDone?.(to);
        return;
      }
      this.problem = this.workspace.state.value.error ?? this.workspace.tree.state.value.status;
    } finally {
      this.busy = false;
    }
  }

  private dialog() {
    const title = `Move ${this.path}`;
    return html`<dialog class="pm-dialog" aria-labelledby="pm-move-to-title" @close=${() => (this.open = false)}>
      <form class="pm-card" @submit=${this.submit}>
        <h1 id="pm-move-to-title">${title}</h1>
        ${this.choices.length === 0
          ? html`<p>There is no other folder to move it into. Create one with New folder, or open a folder in the tree to list its folders.</p>`
          : html`<label
              >Into the folder
              <select name="folder" aria-describedby=${this.problem ? "pm-move-to-problem" : nothing}>
                ${this.choices.map((f) => html`<option value=${f}>${f === "" ? "Vault (top level)" : f}</option>`)}
              </select>
            </label>`}
        ${this.problem ? html`<p id="pm-move-to-problem" class="pm-error" role="alert">${this.problem}</p>` : nothing}
        <div class="pm-dialog-actions">
          ${this.choices.length ? html`<button type="submit" ?disabled=${this.busy}>Move</button>` : nothing}
          <button type="button" @click=${this.close}>Cancel</button>
        </div>
      </form>
    </dialog>`;
  }

  protected override render() {
    return html`<button type="button" @click=${() => this.start()}>${this.label}</button>${this.open ? this.dialog() : nothing}`;
  }
}
customElements.define("pm-move-to", MoveToDialog);
