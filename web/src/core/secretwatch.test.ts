import { describe, expect, it } from "vitest";
import { SecretWatch } from "./secretwatch";

const key = `AKIA${"Q7".repeat(8)}`; // built at run time: no scanner flags this file

const shown = (w: SecretWatch) => (w.warning.value.dismissed ? [] : w.warning.value.findings.map((f) => `${f.rule}@${f.line}`));

describe("SecretWatch (ADR-0020)", () => {
  it("warns on a secret, and stops once it's gone", () => {
    const w = new SecretWatch();
    w.check(`# a\n\n${key}\n`);
    expect(shown(w)).toEqual(["AWS access key ID@3"]);
    w.check("# a\n");
    expect(shown(w)).toEqual([]);
  });

  it("a dismissed warning stays hidden until the findings change", () => {
    const w = new SecretWatch();
    w.check(`${key}\n`);
    w.dismiss();
    w.check(`${key}\nmore text\n`);
    expect(shown(w)).toEqual([]);
    w.check(`${key}\n${key.replace("Q7", "R5")}\n`);
    expect(shown(w)).toEqual(["AWS access key ID@1", "AWS access key ID@2"]);
  });

  it("clear forgets the findings and the dismissal", () => {
    const w = new SecretWatch();
    w.check(`${key}\n`);
    w.dismiss();
    w.clear();
    expect(w.warning.value).toEqual({ findings: [], dismissed: false });
  });
});
