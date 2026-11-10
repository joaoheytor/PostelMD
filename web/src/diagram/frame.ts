import mermaid from "mermaid";
import type { DiagramReply, DiagramRequest } from "./protocol";

/**
 * The diagram renderer (ADR-0021). It runs in a sandboxed frame with an opaque origin and its
 * own CSP, so Mermaid's raw HTML and inline styles never touch the app's document. It takes
 * one MessagePort from its parent, once, and answers only on it.
 */

let port: MessagePort | undefined;
let queue: Promise<void> = Promise.resolve();
let seq = 0;

addEventListener("message", (e: MessageEvent) => {
  const offered = e.ports[0];
  if (port || e.source !== parent || !offered) return;
  port = offered;
  port.onmessage = (m: MessageEvent<DiagramRequest>) => {
    const req = m.data;
    queue = queue.then(() => render(req)); // Mermaid renders one diagram at a time
  };
});

async function render(req: DiagramRequest): Promise<void> {
  let reply: DiagramReply;
  try {
    mermaid.initialize({ startOnLoad: false, securityLevel: "strict", theme: req.theme });
    const { svg, diagramType } = await mermaid.render(`pm-diagram-${++seq}`, req.source);
    const root = forImage(svg);
    const [, , width = 0, height = 0] = (root.getAttribute("viewBox") ?? "").split(/[\s,]+/).map(Number);
    const title = root.querySelector(":scope > title")?.textContent?.trim();
    reply = {
      id: req.id,
      ok: true,
      svg: new XMLSerializer().serializeToString(root),
      width: Number.isFinite(width) ? Math.ceil(width) : 0,
      height: Number.isFinite(height) ? Math.ceil(height) : 0,
      label: title || `Diagram: ${diagramType.replace(/-v\d+$/, "")}`,
    };
  } catch (err) {
    reply = { id: req.id, ok: false, error: err instanceof Error ? err.message : String(err) };
  }
  port?.postMessage(reply);
}

/**
 * Prepares Mermaid's SVG for an <img>. Its output is HTML, not always well-formed XML (a label
 * can hold an unclosed <img>), and an <img> needs XML: a broken one shows nothing and makes
 * the app's CSP report the attempt. So it is parsed by the HTML parser in an inert template,
 * where nothing loads or runs, and the caller serializes it back as XML. As an image it runs
 * no script and loads nothing, whatever it contains.
 */
function forImage(svg: string): SVGSVGElement {
  const tpl = document.createElement("template");
  tpl.innerHTML = svg;
  const root = tpl.content.querySelector("svg");
  if (!root) throw new Error("Mermaid returned no SVG");
  return root;
}
