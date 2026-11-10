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

// Bounding a pattern for speed must not cost real secrets: long tokens exist.
describe("scanSecrets on long secrets", () => {
  const b64 = (n: number) => r("aB3_x-", Math.ceil(n / 6)).slice(0, n);
  it.each([
    ["a JWT with a 3000-character payload (group claims)", `eyJ${b64(30)}.eyJ${b64(3000)}.${b64(40)}`, "JSON Web Token"],
    ["a JWT with a 1000-character header (x5c)", `eyJ${b64(1000)}.eyJ${b64(40)}.${b64(40)}`, "JSON Web Token"],
    ["a URL with a 300-character password", `https://app:${b64(300)}@db.internal/app`, "Password in a URL"],
    ["a URL with a 300-character user", `https://${b64(300)}:s3cr3t-Pa55@db.internal/app`, "Password in a URL"],
  ])("finds %s", (_, secret, rule) => {
    expect(scanSecrets(`x ${secret} y`).map((f) => f.rule)).toEqual([rule]);
  });
});

// The scan runs on paste and on every save: a note crafted so a rule restarts a long match at
// every position must not freeze the tab. Each case is one rule's near-miss, repeated. The
// test measures growth, not a wall-clock budget that depends on the machine: four times the
// input must cost about four times the time (linear), well under sixteen (quadratic).
const fill = (unit: string, size: number) => unit.repeat(Math.ceil(size / unit.length));
const adversarial: [string, (n: number) => string][] = [
  ["URL scheme", (n) => fill("a.", n)],
  ["URL user", (n) => `a://${fill("b", n)}`],
  ["URL password", (n) => `a://b:${fill("c", n)}`],
  ["URL users", (n) => fill("a://b:c", n)],
  ["AWS", (n) => fill("AKIA", n)],
  ["GitHub", (n) => fill("ghp_", n)],
  ["GitHub PAT", (n) => fill("github_pat_", n)],
  ["GitLab", (n) => fill("glpat-", n)],
  ["Slack", (n) => fill("xoxb-", n)],
  ["Slack webhook", (n) => fill("hooks.slack.com/services/", n)],
  ["private key", (n) => fill("-----BEGIN ", n)],
  ["Google", (n) => fill("AIza-", n)],
  ["Stripe", (n) => fill("sk_test_", n)],
  ["npm", (n) => fill("npm_", n)],
  ["OpenAI", (n) => fill("sk-", n)],
  ["OpenAI project", (n) => fill("sk-proj-", n)],
  ["OpenAI markers", (n) => `sk-${fill("T3BlbkFJ", n)}`],
  ["Anthropic", (n) => fill("sk-ant-a", n)],
  ["Anthropic dashes", (n) => fill("sk-ant-a-", n)],
  ["JWT", (n) => fill("eyJ-", n)],
  ["JWT segments", (n) => fill("eyJaaaaaaaaaaa.", n)],
  ["JWT payloads", (n) => fill("eyJaaaaaaaaaaa.eyJ-", n)],
  ["Kubernetes", (n) => fill("client-key-data: ", n)],
  ["Kubernetes spaces", (n) => `client-key-data:${fill(" ", n)}`],
];

/** The best of three runs, in ms: the least disturbed by the machine. */
function timeScan(text: string): number {
  let best = Infinity;
  for (let i = 0; i < 3; i++) {
    const start = performance.now();
    scanSecrets(text);
    best = Math.min(best, performance.now() - start);
  }
  return best;
}

describe("scanSecrets on hostile input", () => {
  it.each(adversarial)("stays linear on %s", (_, make) => {
    const small = timeScan(make(50_000));
    const large = timeScan(make(200_000));
    // Below 2 ms, timer resolution is noise: count it as 2 ms.
    expect(large / Math.max(small, 2)).toBeLessThan(8);
    expect(large).toBeLessThan(2000); // a hang guard only
  });
});
