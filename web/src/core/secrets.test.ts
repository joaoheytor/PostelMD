import { describe, expect, it } from "vitest";
import { scanSecrets } from "./secrets";

// Fake secrets are built at run time, so no scanner flags this file (it is public).
const r = (s: string, n: number) => s.repeat(n);
const fakes: [string, string][] = [
  ["AWS access key ID", `AKIA${r("Q7", 8)}`],
  ["GitHub token", `ghp_${r("aB3", 12)}`],
  ["GitHub token", `github_pat_${r("aB3_", 20)}ab`],
  ["GitLab token", `glpat-${r("xY9_", 5)}`],
  ["Slack token", `xoxb-${r("12345-", 3)}abcdef`],
  ["Slack webhook", `https://hooks.slack.com/services/${r("T0B1C2D3/", 5)}xy`],
  ["Private key", `-----BEGIN ${"OPENSSH"} PRIVATE KEY-----`],
  ["Google API key", `AIza${r("Sy9_-q", 5)}abcde`],
  ["Stripe key", `sk_live_${r("4eC39Hq", 3)}`],
  ["npm token", `npm_${r("abc123", 6)}`],
  ["OpenAI key", `sk-proj-${r("a1", 12)}T3BlbkFJ${r("b2", 12)}`],
  ["Anthropic key", `sk-ant-api03-${r("Zx9_", 22)}`],
  ["JSON Web Token", `eyJ${r("hbGci", 3)}.eyJ${r("zdWIi", 3)}.${r("SflKx", 3)}`],
  ["Kubernetes client key", `client-key-data: ${r("LS0tLS1CRUdJTi", 3)}`],
  ["Password in a URL", "postgres://app:" + "s3cr3t-Pa55@db.internal:5432/app"],
];

describe("scanSecrets", () => {
  it.each(fakes)("finds a %s", (rule, secret) => {
    const found = scanSecrets(`# Runbook\n\nexport X=${secret}\n`);
    expect(found.map((f) => f.rule)).toEqual([rule]);
    expect(found[0]?.line).toBe(3);
  });

  it("never returns the secret itself, only a masked hint", () => {
    for (const [, secret] of fakes) {
      const [f] = scanSecrets(secret);
      // At most the first four characters, enough to find it in the note.
      expect(f?.hint).toMatch(/^.{1,4}…$/);
      expect(secret).toContain(f?.hint.slice(0, -1) ?? "?");
    }
  });

  it("ignores documentation placeholders", () => {
    const text = [
      `aws_access_key_id = AKIA${"IOSFODNN7"}EXAMPLE`,
      "postgres://app:<password>@db:5432/app",
      "https://user:${PASSWORD}@example.com",
      "https://user:****@example.com",
      "https://example.com/path:with@colon",
      "ghp_short",
      "BEGIN PUBLIC KEY",
    ].join("\n");
    expect(scanSecrets(text)).toEqual([]);
  });

  it("reports each finding with its own line, in order", () => {
    const text = `a\nAKIA${r("Q7", 8)}\nb\nghp_${r("aB3", 12)} and AKIA${r("R5", 8)}\n`;
    expect(scanSecrets(text).map((f) => [f.rule, f.line])).toEqual([
      ["AWS access key ID", 2],
      ["GitHub token", 4],
      ["AWS access key ID", 4],
    ]);
  });

  it("finds nothing in an ordinary note", () => {
    expect(scanSecrets("# Restart the API\n\nkubectl rollout restart deploy/api -n prod\n")).toEqual([]);
  });
});
