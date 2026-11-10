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
// or structure, so ordinary prose doesn't match.
const rules: Rule[] = [
  { name: "AWS access key ID", re: /\b(?:A3T[A-Z0-9]|AKIA|ASIA|ABIA|ACCA)[A-Z2-7]{16}\b/g },
  { name: "GitHub token", re: /\b(?:gh[pousr]_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{82})\b/g },
  { name: "GitLab token", re: /\bglpat-[A-Za-z0-9_-]{20}\b/g },
  { name: "Slack token", re: /\bxox[baprs]-[A-Za-z0-9-]{10,}/g },
  { name: "Slack webhook", re: /hooks\.slack\.com\/(?:services|workflows|triggers)\/[A-Za-z0-9+/]{43,}/g },
  { name: "Private key", re: /-----BEGIN[ A-Z0-9_-]{0,100}PRIVATE KEY(?: BLOCK)?-----/g },
  { name: "Google API key", re: /\bAIza[0-9A-Za-z_-]{35}\b/g },
  { name: "Stripe key", re: /\b(?:sk|rk)_(?:test|live|prod)_[A-Za-z0-9]{10,99}\b/g },
  { name: "npm token", re: /\bnpm_[A-Za-z0-9]{36}\b/g },
  { name: "OpenAI key", re: /\bsk-(?:proj-|svcacct-|admin-)?[A-Za-z0-9_-]{20,}T3BlbkFJ[A-Za-z0-9_-]{20,}/g },
  { name: "Anthropic key", re: /\bsk-ant-[a-z]+\d*-[A-Za-z0-9_-]{80,}/g },
  { name: "JSON Web Token", re: /\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}/g },
  { name: "Kubernetes client key", re: /\bclient-key-data:\s*[A-Za-z0-9+/=]{20,}/g },
  // user:password@ in a URL; a password starting like a placeholder (<…>, ${…}, $VAR, ***) isn't one.
  { name: "Password in a URL", re: /\b[a-z][a-z0-9+.-]*:\/\/[^\s:/@]+:(?![<$*{])[^\s/@]{3,}@/g },
];

/** Documentation's example values (AWS's AKIA…EXAMPLE keys) aren't secrets. */
const placeholder = /EXAMPLE/;

/** The secrets text seems to hold, in order of position. */
export function scanSecrets(text: string): SecretFinding[] {
  const found: { at: number; rule: string; hint: string }[] = [];
  for (const { name, re } of rules) {
    for (const m of text.matchAll(re)) {
      if (placeholder.test(m[0])) continue;
      found.push({ at: m.index, rule: name, hint: `${m[0].slice(0, 4)}…` });
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
