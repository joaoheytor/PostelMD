// Deterministic synthetic note: 10,000 lines of mixed Markdown, including hostile raw HTML.
export function bigNote(lines = 10_000): string {
  const out: string[] = [];
  let i = 0;
  while (out.length < lines) {
    const k = i % 7;
    if (k === 0) out.push(`## Section ${i}`, "");
    else if (k === 1) out.push(`Paragraph ${i} with **bold**, _italic_, \`code\` and a [link](https://example.invalid/${i}).`, "");
    else if (k === 2) out.push(`- item ${i}`, `- [ ] task ${i}`, `- [x] done ${i}`, "");
    else if (k === 3) out.push("| a | b | c |", "|---|---|---|", `| ${i} | x | y |`, "");
    else if (k === 4) out.push("```yaml", `key: ${i}`, "list: [1, 2, 3]", "```", "");
    else if (k === 5) out.push(`> quote ${i}`, "");
    else out.push(`Raw HTML <kbd>Ctrl</kbd> and <img src=x onerror=alert(${i})> must be sanitized.`, "");
    i++;
  }
  return out.slice(0, lines).join("\n");
}
