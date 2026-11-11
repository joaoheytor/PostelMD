import { describe, expect, it } from "vitest";
import { type DiffLine, diffLines, textDiff } from "./diff";

/** A compact form: " a", "+b", "-c". */
function show(lines: DiffLine[]): string[] {
  return lines.map((l) => ({ same: " ", added: "+", removed: "-" })[l.kind] + l.text);
}

describe("diffLines", () => {
  it("finds nothing between equal texts", () => {
    expect(show(diffLines("a\nb\n", "a\nb\n"))).toEqual([" a", " b", " "]);
  });

  it("shows added, removed and changed lines in order", () => {
    expect(show(diffLines("a\nb\nc\nd", "a\nB\nc\nd\ne"))).toEqual([" a", "-b", "+B", " c", " d", "+e"]);
    expect(show(diffLines("x\na\nb", "a\nb"))).toEqual(["-x", " a", " b"]);
  });

  it("is a minimal diff", () => {
    const d = diffLines("a\nb\nc\na\nb\nb\na", "c\nb\na\nb\na\nc");
    expect(d.filter((l) => l.kind !== "same")).toHaveLength(5); // Myers' paper example: D = 5
    expect(d.filter((l) => l.kind !== "added").map((l) => l.text)).toEqual("a\nb\nc\na\nb\nb\na".split("\n"));
    expect(d.filter((l) => l.kind !== "removed").map((l) => l.text)).toEqual("c\nb\na\nb\na\nc".split("\n"));
  });

  it("always rebuilds both texts (random inputs)", () => {
    let seed = 42;
    const rand = (n: number) => ((seed = (seed * 1103515245 + 12345) % 2 ** 31), seed % n);
    const text = () => Array.from({ length: rand(12) }, () => "abc"[rand(3)]).join("\n");
    for (let i = 0; i < 500; i++) {
      const [a, b] = [text(), text()];
      const d = diffLines(a, b);
      expect(d.filter((l) => l.kind !== "added").map((l) => l.text).join("\n")).toBe(a);
      expect(d.filter((l) => l.kind !== "removed").map((l) => l.text).join("\n")).toBe(b);
    }
  });

  it("handles empty texts", () => {
    expect(show(diffLines("", "a"))).toEqual(["+a"]);
    expect(show(diffLines("a", ""))).toEqual(["-a"]);
    expect(diffLines("", "")).toEqual([]);
  });

  it("falls back to replacing everything when the texts are too different to compare cheaply", () => {
    const a = Array.from({ length: 3000 }, (_, i) => `a${i}`).join("\n");
    const b = Array.from({ length: 3000 }, (_, i) => `b${i}`).join("\n");
    const d = diffLines(a, b, 1000);
    expect(d.filter((l) => l.kind === "removed")).toHaveLength(3000);
    expect(d.filter((l) => l.kind === "added")).toHaveLength(3000);
  });
});

describe("textDiff: two file texts, compared as the editor shows them", () => {
  const bom = String.fromCharCode(0xfeff);

  it("says so when only the line endings differ, instead of -/+ pairs that look the same", () => {
    expect(textDiff("a\r\nb\r\n", "a\nb\n")).toBe("endings");
  });

  it("says so when only a byte order mark differs", () => {
    expect(textDiff(`${bom}a\nb\n`, "a\nb\n")).toBe("endings");
  });

  it("shows the real change without the endings noise", () => {
    expect(show(textDiff("a\r\nb\r\n", `${bom}a\nc\n`) as DiffLine[])).toEqual([" a", "-b", "+c", " "]);
  });

  it("identical texts are an ordinary diff with nothing changed", () => {
    expect(show(textDiff("a\r\n", "a\r\n") as DiffLine[])).toEqual([" a", " "]);
  });
});
