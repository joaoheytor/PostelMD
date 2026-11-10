import { html } from "lit";
import { moveTarget } from "../core/tree";
import type { Workspace } from "../core/workspace";
import { fields, LightElement } from "./base";

/**
 * Renames or moves a note or folder: one field with its full path. The owner shows it and
 * hides it again through onDone, which gets the new path after a move and nothing after a
 * cancel. A failed move stays open; the workspace reports why.
 */
export class MoveForm extends LightElement {
  static override properties = {
    workspace: { attribute: false },
    path: { attribute: false },
    kind: { attribute: false },
    onDone: { attribute: false },
    busy: { state: true },
  };
  declare workspace: Workspace;
  declare path: string;
  declare kind: "file" | "folder";
  declare onDone: (to?: string) => void;
  declare busy: boolean;

  constructor() {
    super();
    this.busy = false;
  }

  private async submit(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    const to = moveTarget(this.path, fields(e.target as HTMLFormElement)["to"] ?? "", this.kind);
    if (to === "" || to === this.path) return this.onDone();
    this.busy = true;
    try {
      if (await this.workspace.move(this.path, to)) this.onDone(to);
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
            .value=${this.path}
            required
            autofocus
            spellcheck="false"
            @keydown=${(e: KeyboardEvent) => e.key === "Escape" && this.onDone()}
          />
        </label>
        <button type="submit" ?disabled=${this.busy}>Move</button>
        <button type="button" @click=${() => this.onDone()}>Cancel</button>
      </form>
    `;
  }
}
customElements.define("pm-move", MoveForm);
