import { type Disposable, toDisposable } from "./disposable";

/**
 * A value that UI components subscribe to (CLAUDE.md rule 3). Values are replaced, never
 * mutated in place, so a subscriber can compare the old and new value.
 */
export class Store<T> {
  private readonly listeners = new Set<(value: T) => void>();

  constructor(private current: T) {}

  get value(): T {
    return this.current;
  }

  set(value: T): void {
    if (Object.is(value, this.current)) return;
    this.current = value;
    for (const fn of [...this.listeners]) fn(value);
  }

  update(change: (value: T) => T): void {
    this.set(change(this.current));
  }

  subscribe(fn: (value: T) => void): Disposable {
    this.listeners.add(fn);
    return toDisposable(() => this.listeners.delete(fn));
  }
}
