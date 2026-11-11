import { html, nothing, type PropertyValues } from "lit";
import type { App, Leaving } from "../core/app";
import { LightElement } from "./base";

/**
 * Logging out found edits that aren't saved: stay with them, or log out keeping them in this
 * browser as a draft (ADR-0023), offered when the note is opened again. Never discarded
 * silently. Without storage that outlives the page, or when the draft can't be written, the
 * only choice is to stay. Stay and Escape close the dialog and give the focus back (afterStay).
 */
export class LeaveDialog extends LightElement {
  static override properties = { app: { attribute: false }, leaving: { attribute: false }, afterStay: { attribute: false } };
  declare app: App;
  declare leaving: Leaving;
  declare afterStay: () => void;

  private get dialog(): HTMLDialogElement | null {
    return this.querySelector("dialog");
  }

  protected override firstUpdated(): void {
    this.dialog?.showModal();
  }

  /** The draft just failed: the focus goes to Stay before the button that had it goes. */
  protected override willUpdate(changed: PropertyValues<this>): void {
    const before = changed.get("leaving") as Leaving | undefined;
    if (before && !before.draftFailed && this.leaving.draftFailed) this.querySelector<HTMLElement>(".pm-leave-stay")?.focus();
  }

  private closed = () => {
    this.app.stay();
    this.afterStay();
  };

  protected override render() {
    const l = this.leaving;
    const canKeep = l.persistent && !l.draftFailed;
    return html`<dialog class="pm-dialog" aria-labelledby="pm-leave-title" @close=${this.closed}>
      <div class="pm-card">
        <h1 id="pm-leave-title">Your edits to ${l.path} aren't saved</h1>
        <p>${l.why}</p>
        ${canKeep
          ? html`<p>Stay to keep working on them, or log out and keep them in this browser as a draft. The draft is offered when you open the note again.</p>`
          : html`<p class="pm-error" role="alert">
              ${l.draftFailed
                ? "Couldn't keep a local copy of your edits in this browser either."
                : "This browser can't keep a copy of your edits after you log out."}
              Stay, and copy your text somewhere safe before you log out.
            </p>`}
        <div class="pm-dialog-actions">
          <button type="button" class="pm-leave-stay" @click=${() => this.dialog?.close()}>Stay</button>
          ${canKeep ? html`<button type="button" @click=${() => this.app.logoutKeepingDraft()}>Log out and keep a draft</button>` : nothing}
        </div>
      </div>
    </dialog>`;
  }
}
customElements.define("pm-leave", LeaveDialog);
