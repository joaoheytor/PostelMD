import { html } from "lit";
import { ApiError } from "../core/api";
import type { App } from "../core/app";
import { message } from "../core/workspace";
import { fields, LightElement } from "./base";

/** The password form, as a page or, after an expired session, as a dialog over the work. */
export class LoginForm extends LightElement {
  static override properties = { app: { attribute: false }, error: { state: true }, busy: { state: true } };
  declare app: App;
  declare error: string;
  declare busy: boolean;

  constructor() {
    super();
    this.error = "";
    this.busy = false;
  }

  private async submit(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    const form = e.target as HTMLFormElement;
    this.busy = true;
    try {
      await this.app.login(fields(form)["password"] ?? "");
      this.error = "";
    } catch (err) {
      this.error = loginError(err);
      form.querySelector("input")?.select();
    } finally {
      this.busy = false;
    }
  }

  protected override render() {
    const reauth = this.app.state.value.reauth;
    return html`
      <form class="pm-card" @submit=${this.submit} aria-labelledby="login-title">
        <h1 id="login-title">${reauth ? "Your session expired" : "PostelMD"}</h1>
        ${reauth ? html`<p>Log in again to keep saving. Your edits are still here.</p>` : ""}
        <label>Password <input name="password" type="password" autocomplete="current-password" required autofocus /></label>
        ${this.error ? html`<p class="pm-error" role="alert">${this.error}</p>` : ""}
        <button type="submit" ?disabled=${this.busy}>Log in</button>
      </form>
    `;
  }
}
customElements.define("pm-login", LoginForm);

/** What a failed login tells the user. A wrong password can also start the wait (Retry-After). */
function loginError(err: unknown): string {
  if (!(err instanceof ApiError)) return message(err);
  const wait = `Try again in ${Math.ceil(err.retryAfterMs / 1000)} s.`;
  if (err.status === 401) return err.retryAfterMs > 0 ? `Wrong password. ${wait}` : "Wrong password.";
  if (err.status === 429) return `Too many attempts. ${wait}`;
  return message(err);
}
