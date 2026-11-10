import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "./api";
import { Workspace } from "./workspace";

vi.mock("./api", async (actual) => ({
  ...(await actual<typeof import("./api")>()),
  listFolder: vi.fn(async () => []),
  readNote: vi.fn(async (path: string) => ({ content: `# ${path}`, etag: '"e1"' })),
  saveNote: vi.fn(async () => ({ kind: "saved", etag: '"e2"' })),
}));

const key = `AKIA${"Q7".repeat(8)}`; // built at run time: no scanner flags this file

describe("Workspace secret warning (ADR-0020)", () => {
  let ws: Workspace;
  let text: string;

  beforeEach(async () => {
    vi.clearAllMocks();
    ws = new Workspace();
    await ws.open("a.md");
    text = "# a.md\n";
    ws.reader = () => text;
  });

  const save = async (t: string) => {
    text = t;
    ws.state.value.open?.doc.edit();
    await ws.state.value.open?.doc.flush();
  };
  const shown = () => (ws.secrets.warning.value.dismissed ? [] : ws.secrets.warning.value.findings.map((f) => `${f.rule}@${f.line}`));

  it("warns when a save holds a secret, and stops once it's gone; the save still happens", async () => {
    await save(`# a\n\n${key}\n`);
    expect(api.saveNote).toHaveBeenCalledTimes(1);
    expect(shown()).toEqual(["AWS access key ID@3"]);
    await save("# a\n");
    expect(shown()).toEqual([]);
  });

  it("warns at once on paste, before any save", () => {
    text = `# a\n${key}\n`;
    ws.pasted();
    expect(shown()).toEqual(["AWS access key ID@2"]);
    expect(api.saveNote).not.toHaveBeenCalled();
  });

  it("a dismissed warning stays hidden through saves until the findings change", async () => {
    await save(`${key}\n`);
    ws.secrets.dismiss();
    await save(`${key}\nmore text\n`);
    expect(shown()).toEqual([]);
    await save(`${key}\n${key.replace("Q7", "R5")}\n`);
    expect(shown()).toEqual(["AWS access key ID@1", "AWS access key ID@2"]);
  });

  it("opening another note clears the warning", async () => {
    await save(`${key}\n`);
    await ws.open("b.md");
    expect(shown()).toEqual([]);
  });
});
