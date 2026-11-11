import { type LanguageSupport, StreamLanguage, type StreamParser } from "@codemirror/language";
import type { Extension } from "@codemirror/state";
import type { Mode } from "../core/filetype";

const legacy = (parser: StreamParser<unknown>): Extension => StreamLanguage.define(parser);

/**
 * Each mode's language, loaded on first use: a dynamic import keeps it out of the bundle a
 * Markdown note loads (CLAUDE.md rule 9). Markdown and plain text have none here.
 */
const loaders: Partial<Record<Mode, () => Promise<Extension | LanguageSupport>>> = {
  yaml: async () => (await import("@codemirror/lang-yaml")).yaml(),
  json: async () => (await import("@codemirror/lang-json")).json(),
  python: async () => (await import("@codemirror/lang-python")).python(),
  go: async () => (await import("@codemirror/lang-go")).go(),
  javascript: async () => (await import("@codemirror/lang-javascript")).javascript(),
  jsx: async () => (await import("@codemirror/lang-javascript")).javascript({ jsx: true }),
  typescript: async () => (await import("@codemirror/lang-javascript")).javascript({ typescript: true }),
  tsx: async () => (await import("@codemirror/lang-javascript")).javascript({ typescript: true, jsx: true }),
  shell: async () => legacy((await import("@codemirror/legacy-modes/mode/shell")).shell),
  dockerfile: async () => legacy((await import("@codemirror/legacy-modes/mode/dockerfile")).dockerFile),
  toml: async () => legacy((await import("@codemirror/legacy-modes/mode/toml")).toml),
  properties: async () => legacy((await import("@codemirror/legacy-modes/mode/properties")).properties),
};

/** The mode's highlighting, or nothing for plain text (and for a chunk that failed to load). */
export async function loadLanguage(mode: Mode): Promise<Extension> {
  const load = loaders[mode];
  if (!load) return [];
  try {
    return await load();
  } catch {
    return []; // offline or a stale chunk: the text still opens, just unhighlighted
  }
}
