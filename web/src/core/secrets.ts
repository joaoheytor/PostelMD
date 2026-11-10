/**
 * The MVP's secret warning (ADR-0010, ADR-0020): a small set of high-precision rules, run on
 * paste and on every save. It only warns: nothing leaves the machine in the MVP. Findings are
 * never stored or logged, and never carry the secret, only a short hint to find it by.
 */

export interface SecretFinding {
  /** What it looks like, for the user ("GitHub token"). */
  rule: string;
  /** 1-based line of the note. */
  line: number;
  /** The first characters of the match and "…": never the secret. */
  hint: string;
}

interface Rule {
  name: string;
  re: RegExp;
}

// After the gitleaks rules of the same names, simplified. Each is anchored on a fixed prefix
// or structure, so ordinary prose doesn't match. A repeat that a near-miss can restart at
// every position of a long run is bounded (or atomic), or the scan turns quadratic and a
// crafted note freezes the tab on paste or save. Repeats that can't be restarted that way stay
// unbounded, so long real secrets still match. secrets.test.ts measures the growth.
const rules: Rule[] = [
  { name: "AWS access key ID", re: /\b(?:A3T[A-Z0-9]|AKIA|ASIA|ABIA|ACCA)[A-Z2-7]{16}\b/g },
  { name: "GitHub token", re: /\b(?:gh[pousr]_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{82})\b/g },
  { name: "GitLab token", re: /\bglpat-[A-Za-z0-9_-]{20}\b/g },
  { name: "Slack token", re: /\bxox[baprs]-[A-Za-z0-9-]{10,256}/g },
  { name: "Slack webhook", re: /hooks\.slack\.com\/(?:services|workflows|triggers)\/[A-Za-z0-9+/]{43,256}/g },
  { name: "Private key", re: /-----BEGIN[ A-Z0-9_-]{0,100}PRIVATE KEY(?: BLOCK)?-----/g },
  { name: "Google API key", re: /\bAIza[0-9A-Za-z_-]{35}\b/g },
  { name: "Stripe key", re: /\b(?:sk|rk)_(?:test|live|prod)_[A-Za-z0-9]{10,99}\b/g },
  { name: "npm token", re: /\bnpm_[A-Za-z0-9]{36}\b/g },
  {
    // Anchored on the T3BlbkFJ marker, with the "sk-…" before it in a lookbehind (the "pre"
    // group): the engine only tries where the marker is, not at every "sk-" of a long run.
    name: "OpenAI key",
    re: /(?<=(?<pre>\bsk-(?:proj-|svcacct-|admin-)?[A-Za-z0-9_-]{20,256}))T3BlbkFJ[A-Za-z0-9_-]{20,256}/g,
  },
  { name: "Anthropic key", re: /\bsk-ant-[a-z]{1,32}\d{0,8}-[A-Za-z0-9_-]{80,256}/g },
  {
    // (?=(…))\1 is an atomic group: the header never contains ".", so giving characters back
    // can't help, and not trying keeps a run of "eyJ-eyJ-…" linear. Headers with x5c chains
    // run long, hence 1024. Two "eyJ" segments identify a JWT, so only the start of the
    // payload is matched: payloads with group claims run to thousands of characters.
    name: "JSON Web Token",
    re: /\beyJ(?=([A-Za-z0-9_-]{10,1024}))\1\.eyJ[A-Za-z0-9_-]{10}/g,
  },
  { name: "Kubernetes client key", re: /\bclient-key-data:[ \t]{0,16}[A-Za-z0-9+/=]{20,256}/g },
  // user:password@ in a URL; a password starting like a placeholder (<…>, ${…}, $VAR, ***) isn't one.
  // Only the scheme is bounded: the user and the password exclude the delimiter that ends
  // them, so a start can't give characters back to a later one.
  { name: "Password in a URL", re: /\b[a-z][a-z0-9+.-]{0,31}:\/\/[^\s:/@]+:(?![<$*{])[^\s/@]{3,}@/g },
];

/** Documentation's example values (AWS's AKIA…EXAMPLE keys) aren't secrets. */
const placeholder = /EXAMPLE/;

/** The secrets text seems to hold, in order of position. */
export function scanSecrets(text: string): SecretFinding[] {
  const found: { at: number; rule: string; hint: string }[] = [];
  for (const { name, re } of rules) {
    for (const m of text.matchAll(re)) {
      const pre = m.groups?.["pre"] ?? ""; // a lookbehind's part of the secret
      const secret = pre + m[0];
      if (placeholder.test(secret)) continue;
      found.push({ at: m.index - pre.length, rule: name, hint: `${secret.slice(0, 4)}…` });
    }
  }
  found.sort((a, b) => a.at - b.at);
  return found.map(({ at, rule, hint }) => ({ rule, line: lineOf(text, at), hint }));
}

export function sameFindings(a: SecretFinding[], b: SecretFinding[]): boolean {
  return a.length === b.length && a.every((f, i) => f.rule === b[i]?.rule && f.line === b[i]?.line && f.hint === b[i]?.hint);
}

function lineOf(text: string, at: number): number {
  let line = 1;
  for (let i = text.indexOf("\n"); i !== -1 && i < at; i = text.indexOf("\n", i + 1)) line++;
  return line;
}
