/**
 * Note templates (PRD, ADR-0020): the starter pack ships in the app. Placeholders are
 * `{{name}}`, filled by plain, logic-less substitution (ADR-0011): a value is text, never
 * a template, and there are no conditions or loops.
 */
import adr from "../templates/adr.md?raw";
import oncallHandoff from "../templates/oncall-handoff.md?raw";
import postmortem from "../templates/postmortem.md?raw";
import runbook from "../templates/runbook.md?raw";

export interface Template {
  name: string;
  body: string;
}

export const starterPack: readonly Template[] = [
  { name: "Runbook", body: runbook },
  { name: "Postmortem", body: postmortem },
  { name: "ADR", body: adr },
  { name: "On-call handoff", body: oncallHandoff },
];

/** Placeholders the app fills itself: the note's name and today's date. */
export const builtIns: readonly string[] = ["title", "date"];

const placeholder = /\{\{\s*([a-z][a-z0-9_]*)\s*\}\}/gi;

/** The placeholders in body, once each, in order of first use. */
export function placeholders(body: string): string[] {
  return [...new Set(Array.from(body.matchAll(placeholder), (m) => m[1] ?? ""))];
}

/** Replaces each placeholder that has a non-empty value; the others stay, as reminders. */
export function fill(body: string, values: Readonly<Record<string, string>>): string {
  return body.replace(placeholder, (whole, name: string) => (Object.hasOwn(values, name) && values[name]) || whole);
}

/** date as YYYY-MM-DD in the local calendar. */
export function localDate(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}
