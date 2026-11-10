/**
 * The finder's fuzzy match over file paths (ADR-0020): the query's characters, in order,
 * anywhere in the path. A path is scored by one greedy alignment, tried in the file name first,
 * so ranking 50,000 paths stays cheap per keystroke.
 */

export interface Match {
  path: string;
  score: number;
  /** The indexes in path of the matched characters. */
  positions: number[];
}

const boundary = /[/\-_. ]/;

/** Scores path for query, or null when the query's characters aren't all in it, in order. */
export function match(path: string, query: string): Match | null {
  const q = query.toLowerCase().replace(/\s+/g, "");
  if (q === "") return { path, score: 0, positions: [] };
  const lower = path.toLowerCase();
  const base = path.lastIndexOf("/") + 1;
  const positions = align(lower, q, base) ?? align(lower, q, 0);
  if (!positions) return null;

  let score = 0;
  positions.forEach((at, i) => {
    score += 1;
    if (at === 0 || boundary.test(path[at - 1] ?? "") || isCamel(path, at)) score += 8;
    if (i > 0 && positions[i - 1] === at - 1) score += 5;
    if (at >= base) score += 2;
  });
  if (lower.startsWith(q, base)) score += 10;
  return { path, score: score - path.length / 100, positions };
}

/** The best matches of query in files, best first, at most limit. */
export function rank(files: readonly string[], query: string, limit: number): Match[] {
  const found: Match[] = [];
  for (const f of files) {
    const m = match(f, query);
    if (m) found.push(m);
  }
  found.sort((a, b) => b.score - a.score || a.path.localeCompare(b.path));
  return found.slice(0, limit);
}

/** Matches q's characters in order in lower from index from, preferring word starts. */
function align(lower: string, q: string, from: number): number[] | null {
  const out: number[] = [];
  let at = from;
  for (const ch of q) {
    const next = lower.indexOf(ch, at);
    if (next === -1) return null;
    // A word start a little further on scores better than a letter inside a word, unless
    // this one continues the previous match.
    const prev = out.at(-1);
    let pick = next;
    if (prev !== next - 1 && !(next === 0 || boundary.test(lower[next - 1] ?? ""))) {
      for (let i = next + 1; i < lower.length; i++) {
        if (lower[i] === ch && boundary.test(lower[i - 1] ?? "")) {
          if (canFinish(lower, q.slice(out.length + 1), i + 1)) pick = i;
          break;
        }
      }
    }
    out.push(pick);
    at = pick + 1;
  }
  return out;
}

function canFinish(lower: string, rest: string, from: number): boolean {
  let at = from;
  for (const ch of rest) {
    const next = lower.indexOf(ch, at);
    if (next === -1) return false;
    at = next + 1;
  }
  return true;
}

function isCamel(path: string, at: number): boolean {
  const c = path[at] ?? "";
  const p = path[at - 1] ?? "";
  return c !== c.toLowerCase() && p === p.toLowerCase() && p !== p.toUpperCase();
}
