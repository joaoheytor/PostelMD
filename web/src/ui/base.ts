import { LitElement, type ReactiveController, type ReactiveControllerHost } from "lit";
import type { Disposable } from "../core/disposable";
import type { Store } from "../core/store";

/**
 * Shell components render in the light DOM (ADR-0019), so ARIA references work across them
 * and one stylesheet applies. Class names are prefixed with the component's name.
 */
export class LightElement extends LitElement {
  protected override createRenderRoot(): HTMLElement {
    return this;
  }
}

/** Subscribes a component to a core store while it is connected (ADR-0019, rule 8). */
export class StoreController<T> implements ReactiveController {
  private sub: Disposable | undefined;

  constructor(
    private readonly host: ReactiveControllerHost,
    private readonly store: () => Store<T> | undefined,
  ) {
    host.addController(this);
  }

  get value(): T | undefined {
    return this.store()?.value;
  }

  hostConnected(): void {
    this.sub = this.store()?.subscribe(() => this.host.requestUpdate());
  }

  hostDisconnected(): void {
    this.sub?.dispose();
    this.sub = undefined;
  }
}

/** Adds a DOM listener and returns its removal (CLAUDE.md rule 8). */
export function listen<K extends keyof WindowEventMap>(
  target: Window,
  type: K,
  fn: (e: WindowEventMap[K]) => void,
): Disposable;
export function listen<K extends keyof DocumentEventMap>(
  target: Document,
  type: K,
  fn: (e: DocumentEventMap[K]) => void,
): Disposable;
export function listen(target: EventTarget, type: string, fn: (e: Event) => void): Disposable {
  target.addEventListener(type, fn);
  return { dispose: () => target.removeEventListener(type, fn) };
}

/** The form's named text fields. */
export function fields(form: HTMLFormElement): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [k, v] of new FormData(form)) if (typeof v === "string") out[k] = v;
  return out;
}
