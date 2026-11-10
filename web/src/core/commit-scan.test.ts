import { describe, expect, it, vi } from "vitest";
import type { GitChange } from "./api";
import { decodeText, type FileBytes, needsConfirmation, scanChanges, scanText } from "./commit-scan";
import { scanSecrets } from "./secrets";

// Fake secrets, built at run time so no scanner flags this public file.
const awsKey = `AKIA${"Q7".repeat(8)}`;
const enc = new TextEncoder();

function change(path: string, kind: GitChange["kind"], rev = `rev-${path}`): GitChange {
  return kind === "deleted" ? { path, kind } : { path, kind, rev };
}

function disk(files: Record<string, Uint8Array | string>) {
  return vi.fn(async (path: string): Promise<FileBytes | undefined> => {
    const f = files[path];
    if (f === undefined) return undefined;
    return { bytes: typeof f === "string" ? enc.encode(f) : f, etag: `"rev-${path}"` };
  });
}

describe("scanChanges", () => {
  it("lists each file's findings by rule and line, never the secret", async () => {
    const read = disk({ "creds.md": `# Creds\n\naws = ${awsKey}\n`, "ok.md": "# Fine\n" });
    const scan = await scanChanges([change("creds.md", "added"), change("ok.md", "modified")], read);
    expect(scan).toEqual({
      kind: "scanned",
      files: [
        { path: "creds.md", result: "findings", findings: [{ rule: "AWS access key ID", line: 3, hint: "AKIA…" }] },
        { path: "ok.md", result: "clean" },
      ],
    });
    expect(JSON.stringify(scan)).not.toContain(awsKey);
  });

  it("doesn't read or scan deletions", async () => {
    const read = disk({});
    expect(await scanChanges([change("gone.md", "deleted")], read)).toEqual({ kind: "scanned", files: [] });
    expect(read).not.toHaveBeenCalled();
  });

  it("lists a file with a NUL byte as binary, not checked", async () => {
    const read = disk({ "img.bin": new Uint8Array([0x50, 0x4e, 0x47, 0x00, 0x41]) }); // valid UTF-8 but for the NUL
    expect(await scanChanges([change("img.bin", "added")], read)).toEqual({
      kind: "scanned",
      files: [{ path: "img.bin", result: "not-checked", reason: "binary" }],
    });
  });

  it("scans text that isn't valid UTF-8 anyway", async () => {
    const latin1 = new Uint8Array([...enc.encode(`caf`), 0xe9, ...enc.encode(` ${awsKey}`)]);
    const scan = await scanChanges([change("latin1.txt", "added")], disk({ "latin1.txt": latin1 }));
    expect(scan).toMatchObject({ kind: "scanned", files: [{ path: "latin1.txt", result: "findings" }] });
  });

  it("doesn't check a file over the size limit, nor files past the total limit", async () => {
    const read = disk({ "big.md": "x".repeat(11), "a.md": "x".repeat(6), "b.md": "x".repeat(6), "c.md": "x" });
    const changes = ["big.md", "a.md", "b.md", "c.md"].map((p) => change(p, "added"));
    const scan = await scanChanges(changes, read, { file: 10, total: 10 });
    expect(scan).toEqual({
      kind: "scanned",
      files: [
        { path: "big.md", result: "not-checked", reason: "too large" },
        { path: "a.md", result: "clean" },
        { path: "b.md", result: "not-checked", reason: "over the limit" },
        { path: "c.md", result: "clean" },
      ],
    });
  });

  it("stops reading once the total limit is used up", async () => {
    const read = disk({ "a.md": "x".repeat(10), "b.md": "x" });
    const scan = await scanChanges([change("a.md", "added"), change("b.md", "added")], read, { file: 10, total: 10 });
    expect(scan).toEqual({
      kind: "scanned",
      files: [
        { path: "a.md", result: "clean" },
        { path: "b.md", result: "not-checked", reason: "over the limit" },
      ],
    });
    expect(read).toHaveBeenCalledTimes(1);
  });

  it("reports a file whose bytes aren't the listed ones as changed", async () => {
    const read = disk({ "a.md": "edited in another tab" });
    expect(await scanChanges([change("a.md", "modified", "rev-listed-earlier")], read)).toEqual({ kind: "changed", path: "a.md" });
  });

  it("reports a listed file that is gone as changed", async () => {
    expect(await scanChanges([change("a.md", "added")], disk({}))).toEqual({ kind: "changed", path: "a.md" });
  });
});

describe("scanText", () => {
  it("finds what scanSecrets finds, with the same lines, across chunks", () => {
    const lines = Array.from({ length: 5000 }, (_, i) => (i % 997 === 0 ? `key ${i}: ${awsKey}` : `line ${i} of the runbook`));
    const text = lines.join("\n");
    expect(scanText(text, 4096)).toEqual(scanSecrets(text));
    expect(scanText(text, 4096)).toHaveLength(6);
  });

  it("keeps a line longer than a chunk whole", () => {
    const text = `${"x".repeat(100)} ${awsKey}\nnext`;
    expect(scanText(text, 16)).toEqual([{ rule: "AWS access key ID", line: 1, hint: "AKIA…" }]);
  });
});

describe("decodeText", () => {
  it("decodes text, replacing invalid UTF-8, and refuses a NUL byte", () => {
    expect(decodeText(enc.encode("olá"))).toBe("olá");
    expect(decodeText(new Uint8Array([0x61, 0xff]))).toBe("a�");
    expect(decodeText(new Uint8Array([0x61, 0x00]))).toBeUndefined();
  });
});

describe("needsConfirmation", () => {
  it("is true for a finding or a file that wasn't checked", () => {
    expect(needsConfirmation([{ path: "a", result: "clean" }])).toBe(false);
    expect(needsConfirmation([{ path: "a", result: "not-checked", reason: "binary" }])).toBe(true);
    expect(needsConfirmation([{ path: "a", result: "findings", findings: [] }])).toBe(true);
    expect(needsConfirmation(undefined)).toBe(false);
  });
});
