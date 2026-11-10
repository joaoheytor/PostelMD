import { sameFindings, scanSecrets, type SecretFinding } from "./secrets";
import { Store } from "./store";

/** The open note's secret warning (ADR-0020): findings from the last scan, and whether hidden. */
export interface SecretWarning {
  findings: SecretFinding[];
  /** The user dismissed these findings; a different set shows again. */
  dismissed: boolean;
}

const noSecrets: SecretWarning = { findings: [], dismissed: false };

/** Scans the open note's text on every save and on paste; it warns and never blocks. */
export class SecretWatch {
  readonly warning = new Store<SecretWarning>(noSecrets);

  check(text: string): void {
    const findings = scanSecrets(text);
    if (sameFindings(findings, this.warning.value.findings)) return; // a dismissed warning stays dismissed
    this.warning.set({ findings, dismissed: false });
  }

  dismiss(): void {
    this.warning.update((w) => ({ ...w, dismissed: true }));
  }

  /** Another note opened: nothing to warn about yet. */
  clear(): void {
    this.warning.set(noSecrets);
  }
}
