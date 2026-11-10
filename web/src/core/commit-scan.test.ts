import { describe, expect, it, vi } from "vitest";
import type { GitChange } from "./api";
import { decodeText, type FileBytes, scanChanges } from "./commit-scan";

// Fake secrets, built at run time so no scanner flags this public file.
const awsKey = `AKIA${"Q7".repeat(8)}`;
const enc = new TextEncoder();

function change(path: string, kind: GitChange["kind"], rev = `rev-${path}`): GitChange {
  return kind === "deleted" ? { path, kind, blob: null } : { path, kind, blob: `blob-${path}`, rev };
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

  it("lists binary and non-UTF-8 files as not scanned", async () => {
    const read = disk({
      "img.bin": new Uint8Array([0x50, 0x4e, 0x47, 0x00, 0x41, 0x4b, 0x49, 0x41]), // valid UTF-8 but for the NUL
      "latin1.txt": new Uint8Array([0x63, 0x61, 0x66, 0xe9]),
    });
    const scan = await scanChanges([change("img.bin", "added"), change("latin1.txt", "added")], read);
    expect(scan).toEqual({
      kind: "scanned",
      files: [
        { path: "img.bin", result: "not-text" },
        { path: "latin1.txt", result: "not-text" },
      ],
    });
  });

  it("reports a file whose bytes aren't the listed ones as changed", async () => {
    const read = disk({ "a.md": "edited in another tab" });
    expect(await scanChanges([change("a.md", "modified", "rev-listed-earlier")], read)).toEqual({ kind: "changed", path: "a.md" });
  });

  it("reports a listed file that is gone as changed", async () => {
    expect(await scanChanges([change("a.md", "added")], disk({}))).toEqual({ kind: "changed", path: "a.md" });
  });
});

describe("decodeText", () => {
  it("decodes UTF-8 text and refuses NUL bytes and invalid UTF-8", () => {
    expect(decodeText(enc.encode("olá"))).toBe("olá");
    expect(decodeText(new Uint8Array([0x61, 0x00]))).toBeUndefined();
    expect(decodeText(new Uint8Array([0xff]))).toBeUndefined();
  });
});
