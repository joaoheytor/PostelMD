import { describe, expect, it } from "vitest";
import { modeOf } from "./filetype";

describe("modeOf: the editor's language by file name", () => {
  it.each([
    ["notes/a.md", "markdown"],
    ["A.MARKDOWN", "markdown"],
    ["todo.txt", "markdown"], // a note, as before: it keeps its preview
    ["k8s/deploy.yaml", "yaml"],
    ["compose.YML", "yaml"],
    ["package.json", "json"],
    ["scripts/run.sh", "shell"],
    ["x.bash", "shell"],
    [".env", "shell"],
    ["prod.env", "shell"],
    ["Dockerfile", "dockerfile"],
    ["svc/Containerfile", "dockerfile"],
    ["api.dockerfile", "dockerfile"],
    ["pyproject.toml", "toml"],
    ["php.ini", "properties"],
    ["app.properties", "properties"],
    ["setup.cfg", "properties"],
    ["tool.py", "python"],
    ["main.go", "go"],
    ["app.js", "javascript"],
    ["app.mjs", "javascript"],
    ["view.jsx", "jsx"],
    ["app.ts", "typescript"],
    ["view.tsx", "tsx"],
    ["main.tf", "plain"], // no maintained HCL mode
    ["nginx.conf", "plain"],
    ["Makefile", "plain"],
    ["README", "plain"],
    ["dir.md/file", "plain"], // the folder's name doesn't count
    ["constructor", "plain"],
    ["a.constructor", "plain"],
  ])("%s → %s", (path, mode) => {
    expect(modeOf(path)).toBe(mode);
  });
});
