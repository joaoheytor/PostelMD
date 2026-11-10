import { html, nothing } from "lit";
import { builtIns, placeholders, starterPack, type Template } from "../core/templates";

/** "on_call" → "On call": a placeholder's name as a field label. */
function label(name: string): string {
  const words = name.replace(/_/g, " ");
  return words.charAt(0).toUpperCase() + words.slice(1);
}

/** The template a form chose ("" is a blank note). */
export function chosen(form: HTMLFormElement): Template | undefined {
  const i = (form.elements.namedItem("template") as HTMLSelectElement | null)?.value ?? "";
  return i === "" ? undefined : starterPack[Number(i)];
}

/** The values typed into a form's placeholder fields, by placeholder name. */
export function values(form: HTMLFormElement): Record<string, string> {
  const out: Record<string, string> = {};
  for (const input of form.querySelectorAll<HTMLInputElement>("input[data-placeholder]")) {
    out[input.dataset["placeholder"] ?? ""] = input.value.trim();
  }
  return out;
}

/**
 * The new-note form's template choice, and a field for each of the chosen template's own
 * placeholders. The note's name and today's date fill the built-in ones; an empty field
 * leaves its placeholder in the note, as a reminder.
 */
export function templateFields(template: Template | undefined, onChange: () => void) {
  const own = template ? placeholders(template.body).filter((p) => !builtIns.includes(p)) : [];
  return html`<label
      >Template
      <select name="template" @change=${onChange}>
        <option value="" ?selected=${!template}>Blank note</option>
        ${starterPack.map((t, i) => html`<option value=${i} ?selected=${t === template}>${t.name}</option>`)}
      </select>
    </label>
    ${own.length
      ? html`<fieldset class="pm-template-fields">
          <legend>Fill in (optional)</legend>
          ${own.map((p) => html`<label>${label(p)} <input data-placeholder=${p} autocomplete="off" /></label>`)}
        </fieldset>`
      : nothing}`;
}
