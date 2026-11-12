import { afterEach, describe, expect, it, vi } from "vitest";
import { about, versionLabel } from "./about";

describe("about", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("reads the build from the logged-in endpoint", async () => {
    const fetch = vi.fn(() => Promise.resolve(Response.json({ version: "0.1.0-rc.1", commit: "e3fd8bb" })));
    vi.stubGlobal("fetch", fetch);
    expect(await about()).toEqual({ version: "0.1.0-rc.1", commit: "e3fd8bb" });
    expect(fetch).toHaveBeenCalledWith("/api/v1/about", expect.objectContaining({ method: "GET" }));
  });

  it("names the commit once", () => {
    expect(versionLabel({ version: "0.1.0-rc.1", commit: "e3fd8bb" })).toBe("0.1.0-rc.1 (e3fd8bb)");
    expect(versionLabel({ version: "main-e3fd8bb", commit: "e3fd8bb" })).toBe("main-e3fd8bb");
    expect(versionLabel({ version: "dev", commit: "none" })).toBe("dev (none)");
  });
});
