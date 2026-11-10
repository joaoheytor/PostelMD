import { html, nothing } from "lit";
import type { GitStatus } from "../core/api";
import type { Git } from "../core/git";
import { fields, LightElement, StoreController } from "./base";

const kindText: Record<GitStatus["changes"][number]["kind"], string> = {
  added: "Added",
  modified: "Changed",
  deleted: "Deleted",
};

/**
 * The commit dialog: the changes since the last commit, a message, and the way out of a
 * git lock left by a crash. The owner renders it to open it; closing calls onClose.
 */
export class CommitDialog extends LightElement {
  static override properties = {
    git: { attribute: false },
    onClose: { attribute: false },
    confirmed: { state: true },
  };
  declare git: Git;
  declare onClose: () => void;
  /** The user ticked "no other git command is running". */
  declare confirmed: boolean;

  private readonly st = new StoreController(this, () => this.git?.state);

  constructor() {
    super();
    this.confirmed = false;
  }

  protected override firstUpdated(): void {
    this.querySelector("dialog")?.showModal();
    void this.git.open();
  }

  private close = () => this.querySelector("dialog")?.close();

  private async submit(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    const form = e.target as HTMLFormElement;
    if (await this.git.commit(fields(form)["message"] ?? "")) form.reset();
  }

  private async removeLocks(): Promise<void> {
    await this.git.removeLocks();
    this.confirmed = false;
  }

  private locks(st: GitStatus) {
    if (st.locks.length === 0) return nothing;
    return html`<section class="pm-banner" role="alert" aria-labelledby="pm-lock-title">
      <h3 id="pm-lock-title">A git lock is blocking commits</h3>
      <ul>
        ${st.locks.map((l) => html`<li><code>${l.path}</code>, from ${new Date(l.modified).toLocaleString()}</li>`)}
      </ul>
      <p>
        A crash can leave it behind, but so can a git command still running on the host. Remove it only if no other git
        command is running.
      </p>
      <label class="pm-check">
        <input type="checkbox" .checked=${this.confirmed} @change=${(e: Event) => (this.confirmed = (e.target as HTMLInputElement).checked)} />
        No other git command is running
      </label>
      <button type="button" ?disabled=${!this.confirmed || this.st.value?.busy} @click=${this.removeLocks}>Remove the lock</button>
    </section>`;
  }

  private changes(st: GitStatus) {
    if (st.changes.length === 0) return html`<p>No changes since the last commit.</p>`;
    return html`<ul class="pm-commit-changes" aria-label="Changes to commit">
      ${st.changes.map((c) => html`<li><span class="pm-commit-kind" data-kind=${c.kind}>${kindText[c.kind]}</span> ${c.path}</li>`)}
    </ul>`;
  }

  protected override render() {
    const s = this.st.value;
    const st = s?.status;
    return html`
      <dialog class="pm-dialog" aria-labelledby="pm-commit-title" @close=${() => this.onClose()}>
        <form class="pm-card" @submit=${this.submit}>
          <h1 id="pm-commit-title">Commit changes</h1>
          ${st
            ? html`<p>On branch <code>${st.branch}</code></p>
                ${this.locks(st)} ${this.changes(st)}
                ${st.skipped?.length
                  ? html`<p>Too large to commit, left out: ${st.skipped.join(", ")}</p>`
                  : nothing}`
            : html`<p>Loading…</p>`}
          ${s?.committed ? html`<p role="status">Committed ${s.committed}.</p>` : nothing}
          ${s?.error ? html`<p class="pm-error" role="alert">${s.error}</p>` : nothing}
          <label>Commit message <textarea name="message" rows="3" required></textarea></label>
          <div class="pm-dialog-actions">
            <button type="submit" ?disabled=${s?.busy || !st?.changes.length}>Commit</button>
            <button type="button" @click=${this.close}>Close</button>
          </div>
        </form>
      </dialog>
    `;
  }
}
customElements.define("pm-commit", CommitDialog);
