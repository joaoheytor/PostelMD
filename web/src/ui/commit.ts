import { html, nothing } from "lit";
import type { GitStatus } from "../core/git-api";
import { type FileScan, needsConfirmation, notChecked, withFindings } from "../core/commit-scan";
import type { Git } from "../core/git";
import { fields, LightElement, StoreController } from "./base";

const kindText: Record<GitStatus["changes"][number]["kind"], string> = {
  added: "Added",
  modified: "Changed",
  deleted: "Deleted",
};

/**
 * The commit dialog: the changes since the last commit, a secret check of exactly those
 * bytes, a message, and the way out of a git lock left by a crash. The owner renders it to
 * open it; closing calls onClose.
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

  /** The plain Commit button. With findings there is none, so Enter can't commit. */
  private async submit(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (this.git.needsConfirmation()) return;
    const form = e.target as HTMLFormElement;
    if (await this.git.commit(fields(form)["message"] ?? "")) form.reset();
  }

  /** "Commit anyway": a plain button, never the form's default action. */
  private async commitAnyway(): Promise<void> {
    const form = this.querySelector("form");
    if (!form?.reportValidity()) return;
    if (await this.git.commit(fields(form)["message"] ?? "", true)) form.reset();
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
    // Focusable: a long list scrolls, and the keyboard must reach it.
    return html`<ul class="pm-commit-changes" aria-label="Changes to commit" tabindex="0">
      ${st.changes.map((c) => html`<li><span class="pm-commit-kind" data-kind=${c.kind}>${kindText[c.kind]}</span> ${c.path}</li>`)}
    </ul>`;
  }

  /**
   * ADR-0020: each finding by file, rule, line and a short hint, never the secret; and each
   * file the dialog couldn't check, which needs "Commit anyway" too.
   */
  private secrets(scan: FileScan[] | undefined) {
    if (!needsConfirmation(scan)) return nothing;
    const found = withFindings(scan);
    const unchecked = notChecked(scan);
    return html`<section class="pm-banner pm-commit-secrets" role="alert" aria-labelledby="pm-secrets-title">
      <h3 id="pm-secrets-title">
        ${found.length ? "These changes look like they contain secrets" : "Some changes couldn't be checked for secrets"}
      </h3>
      ${found.length
        ? html`<ul aria-label="Possible secrets">
            ${found.flatMap((f) =>
              f.findings.map((s) => html`<li>${f.path}: ${s.rule} on line ${s.line} (<code>${s.hint}</code>)</li>`),
            )}
          </ul>`
        : nothing}
      ${unchecked.length
        ? html`<ul aria-label="Not checked for secrets">
            ${unchecked.map((f) => html`<li>${f.path}: not checked (${f.reason})</li>`)}
          </ul>`
        : nothing}
      <p>A commit keeps everything in the git history; removing a secret later means rewriting that history.</p>
    </section>`;
  }

  private actions() {
    const s = this.st.value;
    const ready = !!s?.status?.changes.length && !!s.scan && !s.busy && !s.scanning;
    const commit = this.git.needsConfirmation()
      ? html`<button type="button" class="pm-danger" ?disabled=${!ready} @click=${this.commitAnyway}>Commit anyway</button>`
      : html`<button type="submit" ?disabled=${!ready}>Commit</button>`;
    return html`<div class="pm-dialog-actions">
      ${commit}
      <button type="button" @click=${this.close}>Close</button>
    </div>`;
  }

  protected override render() {
    const s = this.st.value;
    const st = s?.status;
    return html`
      <dialog class="pm-dialog" aria-labelledby="pm-commit-title" @close=${() => this.onClose()}>
        <form class="pm-card" @submit=${this.submit}>
          <h1 id="pm-commit-title">Commit changes</h1>
          ${s?.notice ? html`<p class="pm-banner" role="alert">${s.notice}</p>` : nothing}
          ${st
            ? html`<p>On branch <code>${st.branch}</code></p>
                ${this.locks(st)} ${this.changes(st)}
                ${st.skipped?.length
                  ? html`<p>Too large to commit, left out: ${st.skipped.join(", ")}</p>`
                  : nothing}`
            : html`<p>Loading…</p>`}
          ${s?.scanning && st?.changes.length ? html`<p>Checking the changes for secrets…</p>` : nothing}
          ${this.secrets(s?.scan)}
          ${s?.committed ? html`<p role="status">Committed ${s.committed}.</p>` : nothing}
          ${s?.error ? html`<p class="pm-error" role="alert">${s.error}</p>` : nothing}
          <label>Commit message <textarea name="message" rows="3" required></textarea></label>
          ${this.actions()}
        </form>
      </dialog>
    `;
  }
}
customElements.define("pm-commit", CommitDialog);
