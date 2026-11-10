import { describe, expect, it } from "vitest";
import { claimEsid, type EsidDeps } from "./esid";

/** Web Locks with some names already held by other tabs. */
function locks(held: string[]): EsidDeps["locks"] {
  const taken = new Set(held);
  return {
    request(name, _opts, fn) {
      const lock = taken.has(name) ? null : { name, mode: "exclusive" };
      if (lock) taken.add(name);
      return Promise.resolve(fn(lock));
    },
  };
}

function storage(initial?: string) {
  const m = new Map<string, string>(initial ? [["postelmd.esid", initial]] : []);
  return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), m };
}

let n = 0;
const mint = () => `new-${++n}`;

describe("claimEsid (ADR-0003 §7)", () => {
  it("keeps the tab's esid across a reload when it can lock it", async () => {
    const s = storage("tab-1");
    expect(await claimEsid({ storage: s, locks: locks([]), mint })).toBe("tab-1");
  });

  it("mints a new one when another live tab holds it (a duplicated tab)", async () => {
    const s = storage("tab-1");
    const esid = await claimEsid({ storage: s, locks: locks(["esid:tab-1"]), mint });
    expect(esid).toMatch(/^new-/);
    expect(s.m.get("postelmd.esid")).toBe(esid);
  });

  it("without Web Locks, never reuses a stored esid", async () => {
    const s = storage("tab-1");
    const esid = await claimEsid({ storage: s, locks: undefined, mint });
    expect(esid).toMatch(/^new-/);
    expect(s.m.get("postelmd.esid")).toBe(esid);
  });

  it("works without sessionStorage", async () => {
    expect(await claimEsid({ storage: undefined, locks: locks([]), mint })).toMatch(/^new-/);
  });
});
