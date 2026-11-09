// @ts-check
import js from "@eslint/js";
import tseslint from "typescript-eslint";

export default tseslint.config(
  { ignores: ["dist/", "node_modules/"] },
  js.configs.recommended,
  ...tseslint.configs.strict,
  {
    // core/ is framework-free: it never imports UI code (CLAUDE.md layout and rule 3).
    files: ["src/core/**/*.ts"],
    rules: {
      "no-restricted-imports": [
        "error",
        {
          patterns: [
            { group: ["**/ui/**", "../ui", "../ui/*"], message: "core/ must not import ui/ (CLAUDE.md)." },
            { group: ["**/editor/**"], message: "core/ must not import editor/ (CLAUDE.md)." },
          ],
        },
      ],
    },
  },
);
