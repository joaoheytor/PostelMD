/**
 * Every timer, listener and subscription returns a Disposable, and its owner disposes it
 * (CLAUDE.md rule 8). DisposableStore collects them so a component tears down in one call.
 */
export interface Disposable {
  dispose(): void;
}

export function toDisposable(fn: () => void): Disposable {
  let done = false;
  return {
    dispose() {
      if (done) return;
      done = true;
      fn();
    },
  };
}

/** setTimeout, as a Disposable that clears it. */
export function timeout(fn: () => void, ms: number): Disposable {
  const id = setTimeout(fn, ms);
  return toDisposable(() => clearTimeout(id));
}

export class DisposableStore implements Disposable {
  private readonly items = new Set<Disposable>();
  private disposed = false;

  get isDisposed(): boolean {
    return this.disposed;
  }

  /** Adds an item. Adding to a disposed store disposes the item immediately (no leaks). */
  add<T extends Disposable>(item: T): T {
    if (this.disposed) {
      item.dispose();
    } else {
      this.items.add(item);
    }
    return item;
  }

  /** Disposes every item, in reverse order of addition. Errors don't stop the others. */
  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    const errors: unknown[] = [];
    for (const item of [...this.items].reverse()) {
      try {
        item.dispose();
      } catch (err) {
        errors.push(err);
      }
    }
    this.items.clear();
    if (errors.length > 0) {
      throw new AggregateError(errors, "errors while disposing");
    }
  }
}
