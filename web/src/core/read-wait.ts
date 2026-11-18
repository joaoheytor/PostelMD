/**
 * A read that waits out the server's 503s with Retry-After: a path a rename rule holds
 * (ADR-0031) answers that for up to SecondPass (10 s). Its waits stop when its signal aborts
 * (a newer open, a close, a logout), and leave no timer or listener behind (CLAUDE.md rule 8).
 */
import { timeout } from "./disposable";

/** How long a read waits out 503s with Retry-After, in all. */
export const readWaitMs = 20_000;

/**
 * Resolves after ms, or rejects with signal's reason once it aborts. Either way, the timer
 * and the abort listener are disposed.
 */
export function abortableWait(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    if (signal?.aborted) {
      reject(signal.reason as Error);
      return;
    }
    const onAbort = () => {
      timer.dispose();
      reject(signal?.reason as Error);
    };
    const timer = timeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}

/**
 * get(), again after each 503 that has a Retry-After, as it says, for up to readWaitMs. Any
 * other answer, or a 503 without one, is returned as it is; an abort rejects with its reason.
 */
export async function readWaiting(get: () => Promise<Response>, signal?: AbortSignal): Promise<Response> {
  for (let waited = 0; ; ) {
    const resp = await get();
    const ms = Number(resp.headers.get("Retry-After")) * 1000;
    if (resp.status !== 503 || !(ms > 0) || waited + ms > readWaitMs) return resp;
    await resp.body?.cancel();
    await abortableWait(ms, signal);
    waited += ms;
  }
}
