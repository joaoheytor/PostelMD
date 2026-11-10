/** Messages between the preview and the diagram renderer frame (ADR-0021), over a MessagePort. */
export interface DiagramRequest {
  id: number;
  source: string;
  theme: "default" | "dark";
}

export type DiagramReply =
  | { id: number; ok: true; svg: string; width: number; height: number; label: string }
  | { id: number; ok: false; error: string };
