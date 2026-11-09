// Placeholder entry point. The UI shell is Lit (ADR-0019) and arrives with the first UI work;
// CodeMirror 6 arrives with the editor work in v0.1.
const app = document.querySelector<HTMLElement>("#app");
if (app) {
  app.textContent = "PostelMD";
}
