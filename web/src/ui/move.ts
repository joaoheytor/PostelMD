import { html, nothing, type PropertyValues } from "lit";
import { nameProblem } from "../core/names";
import { moveTarget } from "../core/tree";
import type { Workspace } from "../core/workspace";
import { fields, LightElement } from "./base";

/**
 * Renames or moves a note or folder: one field with its full path, focused and selected when
 * the form opens. The owner shows it and hides it again through onDone, which gets the new
 * path after a move and nothing after a cancel. A failed move stays open with what was typed;
 * the workspace reports why. An owner that renders a new form meanwhile (the open note is
 * reopened after a failed move) gets the typed text through onFailed and passes it as value.
 */
export class MoveForm extends LightElement {
  static override properties = {
    workspace: { attribute: false },
    path: { attribute: false },
    kind: { attribute: false },
    value: { attribute: false },
    onDone: { attribute: false },
    onFailed: { attribute: false },
    busy: { state: true },
    problem: { state: true },
  };
  declare workspace: Workspace;
  declare path: string;
  declare kind: "file" | "folder";
  /** The field's text; the path when unset. */
  declare value: string | undefined;
  declare onDone: (to?: string) => void;
  declare onFailed: ((typed: string) => void) | undefined;
  declare busy: boolean;
  /** Why the typed name can't be used, shown under the field. */
  declare problem: string | undefined;

  constructor() {
    super();
    this.busy = false;
  }

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

  private async submit(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    const typed = fields(e.target as HTMLFormElement)["to"] ?? "";
    const to = moveTarget(this.path, typed, this.kind);
    if (to === "" || to === this.path) return this.onDone();
    // A note's new name follows the new-note rule; it may still go into a folder like .github.
    this.problem = this.kind === "file" ? nameProblem(to.slice(to.lastIndexOf("/") + 1)) : undefined;
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
    return html`
      <form class="pm-move" @submit=${this.submit}>
        <label
          >New path for ${this.path}
          <input
            name="to"
            .value=${this.value ?? this.path}
            required
            spellcheck="false"
            aria-invalid=${this.problem ? "true" : "false"}
            aria-describedby=${this.problem ? "pm-move-problem" : nothing}
            @input=${() => (this.problem = undefined)}
            @keydown=${(e: KeyboardEvent) => e.key === "Escape" && this.onDone()}
          />
        </label>
        ${this.problem ? html`<p id="pm-move-problem" class="pm-error" role="alert">${this.problem}</p>` : nothing}
        <button type="submit" ?disabled=${this.busy}>Move</button>
        <button type="button" @click=${() => this.onDone()}>Cancel</button>
      </form>
    `;
  }
}
customElements.define("pm-move", MoveForm);
