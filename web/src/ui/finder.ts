import { html, nothing } from "lit";
import * as api from "../core/api";
import { type Match, rank } from "../core/fuzzy";
import { isNote } from "../core/tree";
import { message, type Workspace } from "../core/workspace";
import { LightElement, openNote } from "./base";

const limit = 50;

/** One path with the matched characters marked. */
function marked(m: Match) {
  const at = new Set(m.positions);
  const parts: (string | ReturnType<typeof html>)[] = [];
  let plain = "";
  for (let i = 0; i < m.path.length; i++) {
    const ch = m.path[i] ?? "";
    if (!at.has(i)) {
      plain += ch;
      continue;
    }
    if (plain) parts.push(plain);
    plain = "";
    parts.push(html`<mark>${ch}</mark>`);
  }
  if (plain) parts.push(plain);
  return parts;
}

/**
 * The fuzzy finder (ADR-0020): type part of a note's path, pick it, and it opens. A combobox
 * over a listbox: arrows move, Enter opens, Escape closes. The owner renders it to open it.
 */
export class Finder extends LightElement {
  static override properties = {
    workspace: { attribute: false },
    onClose: { attribute: false },
    query: { state: true },
    active: { state: true },
    files: { state: true },
    truncated: { state: true },
    error: { state: true },
  };
  declare workspace: Workspace;
  declare onClose: () => void;
  declare query: string;
  /** The highlighted option's index. */
  declare active: number;
  declare files: string[] | undefined;
  declare truncated: boolean;
  declare error: string | undefined;

  constructor() {
    super();
    this.query = "";
    this.active = 0;
    this.truncated = false;
  }

  protected override firstUpdated(): void {
    this.querySelector("dialog")?.showModal();
    void this.load();
  }

  private async load(): Promise<void> {
    try {
      const { files, truncated } = await api.listFiles();
      this.files = files.filter((f) => isNote(f.slice(f.lastIndexOf("/") + 1)));
      this.truncated = truncated;
    } catch (err) {
      this.error = `Couldn't list the notes: ${message(err)}`;
    }
  }

  private close = () => this.querySelector("dialog")?.close();

  private async pick(path: string): Promise<void> {
    this.close();
    await openNote(this.workspace, path);
  }

  private onKey(e: KeyboardEvent, matches: Match[]): void {
    const n = matches.length;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (n) this.active = (this.active + (e.key === "ArrowDown" ? 1 : n - 1)) % n;
    } else if (e.key === "Enter") {
      e.preventDefault();
      const m = matches[this.active];
      if (m) void this.pick(m.path);
    }
  }

  protected override render() {
    const matches = this.files ? rank(this.files, this.query, limit) : [];
    const active = matches[this.active] ? this.active : 0;
    return html`<dialog class="pm-dialog pm-finder" aria-labelledby="pm-finder-title" @close=${() => this.onClose()}>
      <div class="pm-card">
        <h1 id="pm-finder-title">Find a note</h1>
        <input
          type="text"
          role="combobox"
          aria-label="Note path"
          aria-expanded="true"
          aria-controls="pm-finder-list"
          aria-autocomplete="list"
          aria-activedescendant=${matches.length ? `pm-finder-${active}` : nothing}
          autocomplete="off"
          spellcheck="false"
          .value=${this.query}
          @input=${(e: Event) => {
            this.query = (e.target as HTMLInputElement).value;
            this.active = 0;
          }}
          @keydown=${(e: KeyboardEvent) => this.onKey(e, matches)}
        />
        ${this.error ? html`<p class="pm-error" role="alert">${this.error}</p>` : nothing}
        <ul id="pm-finder-list" role="listbox" aria-label="Matching notes">
          ${matches.map(
            (m, i) => html`<li
              id="pm-finder-${i}"
              role="option"
              aria-selected=${i === active}
              @mousedown=${(e: Event) => e.preventDefault()}
              @click=${() => this.pick(m.path)}
            >
              ${marked(m)}
            </li>`,
          )}
        </ul>
        <p class="pm-finder-hint" role="status">
          ${this.files === undefined
            ? "Loading…"
            : matches.length === 0
              ? "No note matches."
              : `${matches.length === limit ? `The first ${limit}` : matches.length} of ${this.files.length} notes${this.truncated ? " (the vault has more than the finder lists)" : ""}`}
        </p>
      </div>
    </dialog>`;
  }
}
customElements.define("pm-finder", Finder);
