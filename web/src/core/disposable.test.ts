import { describe, expect, it } from "vitest";
import { DisposableStore, toDisposable } from "./disposable";

describe("toDisposable", () => {
  it("runs its cleanup once", () => {
    let calls = 0;
    const d = toDisposable(() => calls++);
    d.dispose();
    d.dispose();
    expect(calls).toBe(1);
  });
});

describe("DisposableStore", () => {
  it("disposes items in reverse order of addition", () => {
    const order: number[] = [];
    const store = new DisposableStore();
    store.add(toDisposable(() => order.push(1)));
    store.add(toDisposable(() => order.push(2)));
    store.dispose();
    expect(order).toEqual([2, 1]);
    expect(store.isDisposed).toBe(true);
  });

  it("disposes items added after it was disposed", () => {
    const store = new DisposableStore();
    store.dispose();
    let disposed = false;
    store.add(toDisposable(() => (disposed = true)));
    expect(disposed).toBe(true);
  });

  it("disposes every item even when one throws", () => {
    let second = false;
    const store = new DisposableStore();
    store.add(toDisposable(() => (second = true)));
    store.add(
      toDisposable(() => {
        throw new Error("boom");
      }),
    );
    expect(() => store.dispose()).toThrow(AggregateError);
    expect(second).toBe(true);
  });

  it("is idempotent", () => {
    let calls = 0;
    const store = new DisposableStore();
    store.add(toDisposable(() => calls++));
    store.dispose();
    store.dispose();
    expect(calls).toBe(1);
  });
});
