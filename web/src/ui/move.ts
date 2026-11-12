import { html, nothing, type PropertyValues } from "lit";
import { folderChoices, intoFolder } from "../core/move-into";
import { nameProblem } from "../core/names";
import { moveTarget, parent } from "../core/tree";
import type { Workspace } from "../core/workspace";
import { fields, LightElement } from "./base";

let forms = 0;

/**
 * Renames or moves a note or folder: one field with its full path, focused and selected when
 * the form opens, and a Folder list that puts the typed name into the folder picked (#130): the
 * keyboard's way to do what a drag in the tree does. The owner shows it and hides it again
 * through onDone, which gets the new path after a move and nothing after a cancel. A failed
 * move stays open with what was typed, and the field gets the focus; the workspace reports why.
 * An owner that renders a new form meanwhile (the open note is reopened after a failed move)
 * gets the typed text through onFailed and passes it as value.
 */
export class MoveForm extends LightElement {
  static override properties = {
    workspace: { attribute: false },
    path: { attribute: false },
    kind: { attribute: false },
    value: { attribute: false },
    onDone: { attribute: false },
    onFailed: { attribute: false },
    problem: { state: true },
  };
  declare workspace: Workspace;
  declare path: string;
  declare kind: "file" | "folder";
  /** The field's text; the path when unset. */
  declare value: string | undefined;
  declare onDone: (to?: string) => void;
  declare onFailed: ((typed: string) => void) | undefined;
  /** Why the typed name can't be used, shown under the field. */
  declare problem: string | undefined;
  /**
   * A move is under way: another submit is ignored. The Move button stays enabled, so it keeps
   * the focus (a disabled button drops it to the page).
   */
  private busy = false;
  /** Ids unique to this form: the tree's and the editor's can be open together. */
  private readonly uid = ++forms;

  private input(): HTMLInputElement | null {
    return this.querySelector("input");
  }

  protected override firstUpdated(): void {
    const input = this.input();
    input?.focus();
    if (this.value === undefined) input?.select();
  }

  protected override updated(changed: PropertyValues<this>): void {
    if (changed.has("value")) this.input()?.focus(); // the owner put back what was typed
  }

  /** A folder was picked: the field keeps its name and gets that folder. */
  private pick(e: Event): void {
    const input = this.input();
    if (!input) return;
    input.value = intoFolder(input.value, (e.target as HTMLSelectElement).value);
    this.problem = undefined;
  }

  private async submit(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (this.busy) return;
    const typed = fields(e.target as HTMLFormElement)["to"] ?? "";
    const to = moveTarget(this.path, typed, this.kind);
    if (to === "" || to === this.path) return this.onDone();
    // A note's new name follows the new-note rule; it may still go into a folder like .github.
    this.problem = this.kind === "file" ? nameProblem(to) : undefined;
    if (this.problem) return this.input()?.focus();
    this.busy = true;
    try {
      if (await this.workspace.move(this.path, to)) return this.onDone(to);
      this.onFailed?.(typed);
      this.input()?.focus();
    } finally {
      this.busy = false;
    }
  }

  protected override render() {
    const typed = this.value ?? this.path;
    const shown = parent(moveTarget(this.path, typed, this.kind));
    // The typed path's folder is listed too, so the list never shows another one as picked.
    const folders = [...new Set([...folderChoices(this.workspace.tree.state.value, this.path, this.kind), shown])].sort();
    const problem = `pm-move-problem-${this.uid}`;
    const help = `pm-move-help-${this.uid}`;
    const select = `pm-move-folder-${this.uid}`;
    return html`
      <form class="pm-move" @submit=${this.submit}>
        <label
          >New path for ${this.path}
          <input
            name="to"
            .value=${typed}
            required
            spellcheck="false"
            aria-invalid=${this.problem ? "true" : "false"}
            aria-describedby=${this.problem ? problem : nothing}
            @input=${() => (this.problem = undefined)}
            @keydown=${(e: KeyboardEvent) => e.key === "Escape" && this.onDone()}
          />
        </label>
        <!-- Labelled with for, not wrapped: a wrapping label would add the picked folder to its name. -->
        <span class="pm-move-folder">
          <label for=${select}>Folder</label>
          <select id=${select} aria-describedby=${help} @change=${this.pick} @keydown=${(e: KeyboardEvent) => e.key === "Escape" && this.onDone()}>
            ${folders.map((f) => html`<option value=${f} ?selected=${f === shown}>${f === "" ? "Vault" : f}</option>`)}
          </select>
        </span>
        <p id=${help} class="pm-move-help">Only folders open in the tree are listed. To go deeper, open the folder in the tree, or type a path.</p>
        ${this.problem ? html`<p id=${problem} class="pm-error" role="alert">${this.problem}</p>` : nothing}
        <button type="submit">Move</button>
        <button type="button" @click=${() => this.onDone()}>Cancel</button>
      </form>
    `;
  }
}
customElements.define("pm-move", MoveForm);
