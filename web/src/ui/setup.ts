import { html, type PropertyValues } from "lit";
import { ApiError } from "../core/api";
import * as api from "../core/api";
import type { App } from "../core/app";
import { message } from "../core/workspace";
import { fields, LightElement } from "./base";

/**
 * First-run setup (ADR-0009, ADR-0020) in two steps: the one-time token from the server's
 * output, which also tells how many existing files the first commit will import; then the
 * password and the git author.
 */
export class SetupForm extends LightElement {
  static override properties = {
    app: { attribute: false },
    token: { state: true },
    files: { state: true },
    error: { state: true },
    busy: { state: true },
  };
  declare app: App;
  declare token: string;
  declare files: number;
  declare error: string;
  declare busy: boolean;

  constructor() {
    super();
    this.token = "";
    this.files = 0;
    this.error = "";
    this.busy = false;
  }

  /** autofocus works only for the first form: the second one is rendered later. */
  protected override updated(changed: PropertyValues<this>): void {
    if (changed.has("token") && this.token) this.querySelector<HTMLInputElement>("input[name=password]")?.focus();
  }

  private async run(e: SubmitEvent, step: (f: Record<string, string>) => Promise<void>): Promise<void> {
    e.preventDefault();
    this.busy = true;
    try {
      await step(fields(e.target as HTMLFormElement));
      this.error = "";
    } catch (err) {
      this.error = err instanceof ApiError && err.status === 403 ? "That token is wrong or expired. Check the server's output." : message(err);
    } finally {
      this.busy = false;
    }
  }

  private check = (e: SubmitEvent) =>
    this.run(e, async (f) => {
      const token = (f["token"] ?? "").trim();
      this.files = await api.checkSetup(token);
      this.token = token;
    });

  private finish = (e: SubmitEvent) =>
    this.run(e, async (f) => {
      if (f["password"] !== f["confirm"]) throw new Error("The passwords don't match.");
      await this.app.setup({ token: this.token, password: f["password"] ?? "", name: f["name"] ?? "", email: f["email"] ?? "" });
      history.replaceState(null, "", "/");
    });

  protected override render() {
    const error = this.error ? html`<p class="pm-error" role="alert">${this.error}</p>` : "";
    if (!this.token) {
      return html`
        <form class="pm-card" @submit=${this.check} aria-labelledby="setup-title">
          <h1 id="setup-title">Set up PostelMD</h1>
          <p>Paste the one-time setup token that the server printed when it started.</p>
          <label>Setup token <input name="token" autocomplete="off" spellcheck="false" required autofocus /></label>
          ${error}
          <button type="submit" ?disabled=${this.busy}>Continue</button>
        </form>
      `;
    }
    return html`
      <form class="pm-card" @submit=${this.finish} aria-labelledby="setup-title">
        <h1 id="setup-title">Set up PostelMD</h1>
        <p>
          ${this.files === 0
            ? "The notes folder is empty."
            : `The notes folder has ${this.files} file${this.files === 1 ? "" : "s"}; they become the first git commit.`}
        </p>
        <label
          >Password
          <input name="password" type="password" autocomplete="new-password" minlength="12" required aria-describedby="setup-password-help"
        /></label>
        <p id="setup-password-help" class="pm-help">At least 12 characters. A few unrelated words make a good one.</p>
        <label>Repeat the password <input name="confirm" type="password" autocomplete="new-password" required /></label>
        <fieldset>
          <legend>Git author for your commits</legend>
          <label>Name <input name="name" autocomplete="name" required /></label>
          <label>Email <input name="email" type="email" autocomplete="email" required /></label>
        </fieldset>
        ${error}
        <button type="submit" ?disabled=${this.busy}>Finish setup</button>
      </form>
    `;
  }
}
customElements.define("pm-setup", SetupForm);
