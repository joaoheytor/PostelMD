import { defineConfig } from "vite";

// The diagram renderer frame's script (ADR-0021). The frame has an opaque origin, where a
// module script would be a CORS request, so this is one classic script with Mermaid inlined.
export default defineConfig({
  publicDir: false,
  build: {
    outDir: "dist/frames",
    emptyOutDir: false,
    sourcemap: false, // 18 MB, and the image has a 40 MB budget
    lib: { entry: "src/diagram/frame.ts", formats: ["iife"], name: "postelmdDiagram", fileName: () => "diagram.js" },
  },
});
