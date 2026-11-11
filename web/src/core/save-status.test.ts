import { describe, expect, it } from "vitest";
import { shownState, statusDetail } from "./save-status";

describe("statusDetail (the save status line's text after 'Not saved: ')", () => {
  const busy = { status: "error", message: "can't reach the server", next: "Retrying" } as const;

  it("says the edits are kept in this browser only when a draft holds them", () => {
    expect(statusDetail(busy, true)).toBe("can't reach the server. Retrying; your edits are kept in this browser.");
    expect(statusDetail(busy, false)).toBe("can't reach the server. Retrying.");
  });

  it("treats an expired session the same way", () => {
    const expired = { status: "error", message: "your session expired", next: "Log in again" } as const;
    expect(statusDetail(expired, true)).toBe("your session expired. Log in again; your edits are kept in this browser.");
    expect(statusDetail(expired, false)).toBe("your session expired. Log in again.");
  });

  it("says only what happened when nothing will retry it", () => {
    expect(statusDetail({ status: "error", message: "the note is too large to save" }, true)).toBe("the note is too large to save.");
  });

  it("adds nothing to a status without a message", () => {
    expect(statusDetail({ status: "saving" }, true)).toBeUndefined();
  });

  it("shownState puts the whole detail in the message and keeps the conflict", () => {
    const conflict = { content: "theirs", etag: '"e9"' };
    expect(shownState(busy, false)).toEqual({ status: "error", message: "can't reach the server. Retrying." });
    expect(shownState({ status: "conflict", conflict }, true)).toEqual({ status: "conflict", conflict });
  });
});
