import DOMPurify from "dompurify";

/**
 * A blob URL for an SVG the diagram frame rendered (ADR-0021). The frame is sandboxed, but the
 * blob it becomes has the app's origin: an <img> runs none of its script, yet the same URL
 * opened as a page would be an app-origin document. So the SVG is sanitized here, in the app:
 * no script, no event handlers, no javascript: links, no frames. Mermaid draws its labels
 * as HTML in <foreignObject>, so that HTML is kept, sanitized the same way. DOMPurify parses
 * with the HTML parser (its XML mode drops camel-case names such as viewBox), and the result
 * is serialized back as XML, which an <img> needs. Revoke the URL when it is no longer shown.
 */
export function svgBlobURL(svg: string): string {
  const fragment = DOMPurify.sanitize(svg, {
    USE_PROFILES: { svg: true, svgFilters: true, html: true },
    ADD_TAGS: ["foreignObject"],
    // DOMPurify allows HTML under SVG nowhere by default; Mermaid's labels need it here.
    HTML_INTEGRATION_POINTS: { foreignobject: true, "annotation-xml": true },
    FORBID_TAGS: ["iframe", "frame", "object", "embed", "form"],
    RETURN_DOM_FRAGMENT: true,
  });
  const root = fragment.querySelector("svg");
  const clean = root ? new XMLSerializer().serializeToString(root) : "";
  return URL.createObjectURL(new Blob([clean], { type: "image/svg+xml" }));
}
