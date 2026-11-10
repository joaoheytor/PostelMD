export interface DiffLine {
  kind: "same" | "added" | "removed";
  text: string;
}

/**
 * A line diff from a to b (Myers' O(ND) algorithm), each removed line before the line added
 * in its place. A diff needing more than maxEdits edits (texts that share little) falls back
 * to "everything removed, everything added", which keeps time and memory bounded.
 */
export function diffLines(a: string, b: string, maxEdits = 2000): DiffLine[] {
  const x = lines(a);
  const y = lines(b);
  let start = 0;
  while (start < x.length && start < y.length && x[start] === y[start]) start++;
  let endX = x.length;
  let endY = y.length;
  while (endX > start && endY > start && x[endX - 1] === y[endY - 1]) {
    endX--;
    endY--;
  }
  const same = (text: string): DiffLine => ({ kind: "same", text });
  const mid = myers(x.slice(start, endX), y.slice(start, endY), maxEdits) ?? [
    ...x.slice(start, endX).map((text): DiffLine => ({ kind: "removed", text })),
    ...y.slice(start, endY).map((text): DiffLine => ({ kind: "added", text })),
  ];
  return [...x.slice(0, start).map(same), ...mid, ...x.slice(endX).map(same)];
}

function lines(s: string): string[] {
  return s === "" ? [] : s.split("\n");
}

/** The shortest edit script, or undefined if it needs more than maxEdits edits. */
function myers(a: string[], b: string[], maxEdits: number): DiffLine[] | undefined {
  const n = a.length;
  const m = b.length;
  const max = Math.min(n + m, maxEdits);
  const off = max + 1;
  const v = new Int32Array(2 * max + 3);
  // Typed-array reads are in bounds by construction; ?? only satisfies the type checker.
  const at = (arr: Int32Array, k: number): number => arr[k + off] ?? 0;
  // trace[d] is v before round d: enough to walk back from the end.
  const trace: Int32Array[] = [];
  let found = n + m === 0;
  for (let d = 0; d <= max && !found; d++) {
    trace.push(v.slice());
    for (let k = -d; k <= d; k += 2) {
      let x = k === -d || (k !== d && at(v, k - 1) < at(v, k + 1)) ? at(v, k + 1) : at(v, k - 1) + 1;
      let y = x - k;
      while (x < n && y < m && a[x] === b[y]) {
        x++;
        y++;
      }
      v[k + off] = x;
      if (x >= n && y >= m) {
        found = true;
        break;
      }
    }
  }
  if (!found) return undefined;

  const out: DiffLine[] = [];
  let x = n;
  let y = m;
  for (let d = trace.length - 1; d >= 0; d--) {
    const vd = trace[d] ?? v;
    const k = x - y;
    const prevK = k === -d || (k !== d && at(vd, k - 1) < at(vd, k + 1)) ? k + 1 : k - 1;
    const prevX = d === 0 ? 0 : at(vd, prevK);
    const prevY = d === 0 ? 0 : prevX - prevK;
    while (x > prevX && y > prevY) {
      out.push({ kind: "same", text: a[--x] ?? "" });
      y--;
    }
    if (d === 0) break;
    if (x === prevX) out.push({ kind: "added", text: b[--y] ?? "" });
    else out.push({ kind: "removed", text: a[--x] ?? "" });
  }
  return out.reverse();
}
