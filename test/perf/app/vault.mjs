// The PRD's synthetic vault, from a fixed seed: 10,000 notes in 100 folders, median about
// 4 KB, every 100th note 200 KB (the p99), plus one 10,000-line note (big.md).
import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { bigNote } from "../editor/src/gen.ts";

export async function writeVault(dir, notes = 10_000) {
  let seed = 42;
  const rand = () => ((seed = (seed * 1103515245 + 12345) % 2 ** 31) / 2 ** 31);
  const para = "Check the dashboard, then restart the deployment and watch the error rate. ";
  for (let i = 0; i < notes; i++) {
    const folder = join(dir, `team-${String(i % 100).padStart(2, "0")}`);
    if (i < 100) await mkdir(folder, { recursive: true });
    const size = i % 100 === 99 ? 200_000 : Math.round(4096 * (0.5 + rand()));
    const body = `# Note ${i}\n\n${para.repeat(Math.ceil(size / para.length))}`.slice(0, size);
    await writeFile(join(folder, `note-${i}.md`), body);
  }
  await writeFile(join(dir, "big.md"), bigNote());
}
