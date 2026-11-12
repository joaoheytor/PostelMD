/** The running server's build (#152), beside api.ts. Served only to a logged-in user. */
import { ok, request } from "./api";

export interface About {
  /** "0.1.0-rc.1" for a release, "main-1a2b3c4" for a candidate, "dev" for a plain build. */
  version: string;
  commit: string;
}

export async function about(): Promise<About> {
  return (await (await ok(await request("GET", "/api/v1/about"))).json()) as About;
}

/** "0.1.0-rc.1 (1a2b3c4)"; just the version when it already names the commit. */
export function versionLabel({ version, commit }: About): string {
  return version.includes(commit) ? version : `${version} (${commit})`;
}
