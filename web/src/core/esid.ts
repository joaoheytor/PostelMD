/**
 * The tab's editing-session ID (esid, ADR-0003 §7): drafts are keyed by it, so two tabs never
 * write over each other's drafts.
 */

export interface EsidDeps {
  /** sessionStorage: the esid survives a reload of the same tab. */
  storage: Pick<Storage, "getItem" | "setItem"> | undefined;
  /** Web Locks (navigator.locks); a held lock proves no other live tab uses the esid. */
  locks: Locks | undefined;
  mint: () => string;
}

/** The part of the Web Locks API used here. */
export interface Locks {
  request(name: string, options: { ifAvailable: true }, callback: (lock: unknown) => Promise<void> | undefined): Promise<unknown>;
}

const key = "postelmd.esid";

/**
 * Claims the tab's esid before any draft is written. A duplicated tab copies sessionStorage,
 * and with it the esid, so the stored one is reused only if this tab can lock it; otherwise,
 * and always without Web Locks, the tab mints a new one.
 */
export async function claimEsid(deps: EsidDeps): Promise<string> {
  const stored = read(deps.storage);
  if (deps.locks && stored && (await hold(deps.locks, stored))) return stored;
  const esid = deps.mint();
  if (deps.locks) await hold(deps.locks, esid);
  write(deps.storage, esid);
  return esid;
}

/** Locks esid, if no one holds it, until the tab goes away. */
function hold(locks: Locks, esid: string): Promise<boolean> {
  return new Promise((resolve) => {
    void locks.request(`esid:${esid}`, { ifAvailable: true }, (lock) => {
      resolve(lock !== null);
      // The lock is held while this promise is pending: for the tab's lifetime.
      return lock ? new Promise<void>(() => {}) : undefined;
    });
  });
}

function read(storage: EsidDeps["storage"]): string | null {
  try {
    return storage?.getItem(key) ?? null;
  } catch {
    return null; // storage blocked
  }
}

function write(storage: EsidDeps["storage"], esid: string): void {
  try {
    storage?.setItem(key, esid);
  } catch {
    // storage blocked: a reload mints a new esid, and this one's drafts are offered then
  }
}
