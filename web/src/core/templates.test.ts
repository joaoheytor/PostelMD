import { describe, expect, it } from "vitest";
import { fill, localDate, placeholders, starterPack } from "./templates";

describe("templates", () => {
  it("lists a template's placeholders once each, in order", () => {
    expect(placeholders("# {{title}}\n{{ service }} by {{owner}}, {{service}}")).toEqual(["title", "service", "owner"]);
  });

  it("fills placeholders by plain substitution, and nothing else", () => {
    expect(fill("# {{title}} ({{ date }})", { title: "Disk full", date: "2026-11-10" })).toBe("# Disk full (2026-11-10)");
    // A value is text, never a template: no recursion, no logic.
    expect(fill("{{a}}", { a: "{{b}}", b: "x" })).toBe("{{b}}");
    expect(fill("{{#if a}}{{/if}} {{a.b}}", { a: "x" })).toBe("{{#if a}}{{/if}} {{a.b}}");
  });

  it("keeps a placeholder with no value, as a reminder to fill it", () => {
    expect(fill("Owner: {{owner}}", { owner: "" })).toBe("Owner: {{owner}}");
    expect(fill("Owner: {{owner}}", {})).toBe("Owner: {{owner}}");
    expect(fill("{{constructor}} {{toString}}", {})).toBe("{{constructor}} {{toString}}");
  });

  it("dates in the local calendar, as YYYY-MM-DD", () => {
    expect(localDate(new Date(2026, 0, 5, 23, 30))).toBe("2026-01-05");
  });

  it("ships the runbook, postmortem, ADR and on-call handoff templates, each titled", () => {
    expect(starterPack.map((t) => t.name)).toEqual(["Runbook", "Postmortem", "ADR", "On-call handoff"]);
    for (const t of starterPack) {
      expect(t.body.startsWith("# {{title}}\n"), t.name).toBe(true);
      expect(placeholders(t.body), t.name).toContain("date");
    }
  });
});
