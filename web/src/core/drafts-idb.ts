import { type Draft, type DraftBackend, Drafts, MemoryDraftBackend } from "./drafts";
import { claimEsid, type Locks } from "./esid";

/**
 * This tab's drafts: claims its esid, then opens IndexedDB. Without IndexedDB (some private
 * modes) drafts live in memory, so they survive a closed note but not a closed tab. Asks the
 * browser to keep the data under storage pressure.
 */
export async function browserDrafts(): Promise<Drafts> {
  // Plain http off localhost isn't a secure context: there crypto.randomUUID and Web Locks
  // don't exist, and claimEsid mints a new esid on every load.
  const mint = () => Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) => b.toString(16).padStart(2, "0")).join("");
  const esid = await claimEsid({ storage: sessionStorageOrNone(), locks: navigator.locks as Locks | undefined, mint });
  let backend: DraftBackend;
  try {
    backend = await IdbDraftBackend.open();
  } catch {
    backend = new MemoryDraftBackend();
  }
  void navigator.storage?.persist?.().catch(() => false);
  return Drafts.open(backend, esid, mint);
}

function sessionStorageOrNone(): Storage | undefined {
  try {
    return sessionStorage;
  } catch {
    return undefined; // blocked
  }
}

/**
 * Drafts in IndexedDB (ADR-0003 §7). The origin is the namespace: in the MVP one origin is one
 * instance, one user and one vault (ADR-0020). Each operation is one transaction, so a
 * conditional delete or a rekey is never half done.
 */
export class IdbDraftBackend implements DraftBackend {
  readonly persistent = true;
  private constructor(private readonly db: IDBDatabase) {}

  static async open(name = "postelmd-drafts"): Promise<IdbDraftBackend> {
    const req = indexedDB.open(name, 1);
    req.onupgradeneeded = () => req.result.createObjectStore(store, { keyPath: ["esid", "path"] });
    return new IdbDraftBackend(await done(req));
  }

  put(d: Draft): Promise<void> {
    return this.tx("readwrite", (s) => void s.put(d));
  }

  deleteUpTo(path: string, esid: string, gen: number): Promise<void> {
    return this.tx("readwrite", (s) => {
      const get = s.get([esid, path]);
      get.onsuccess = () => {
        const d = get.result as Draft | undefined;
        if (d && d.gen <= gen) s.delete([esid, path]);
      };
    });
  }

  delete(path: string, esid: string): Promise<void> {
    return this.tx("readwrite", (s) => void s.delete([esid, path]));
  }

  async all(): Promise<Draft[]> {
    let rows: Draft[] = [];
    await this.tx("readonly", (s) => {
      const get = s.getAll();
      get.onsuccess = () => (rows = get.result as Draft[]);
    });
    return rows;
  }

  rekey(from: string, to: string): Promise<void> {
    return this.tx("readwrite", (s) => {
      const get = s.getAll(IDBKeyRange.bound([from], [from, []]));
      get.onsuccess = () => {
        for (const d of get.result as Draft[]) {
          s.delete([from, d.path]);
          s.put({ ...d, esid: to });
        }
      };
    });
  }

  /** Runs fn in one transaction; resolves when it commits. */
  private tx(mode: IDBTransactionMode, fn: (s: IDBObjectStore) => void): Promise<void> {
    return new Promise((resolve, reject) => {
      const t = this.db.transaction(store, mode, { durability: "strict" });
      t.oncomplete = () => resolve();
      t.onerror = t.onabort = () => reject(t.error ?? new Error("the draft transaction was aborted"));
      fn(t.objectStore(store));
    });
  }
}

const store = "drafts";

function done<T>(req: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error ?? new Error("IndexedDB failed"));
  });
}
