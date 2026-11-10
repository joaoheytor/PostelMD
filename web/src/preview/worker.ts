// The preview Worker (ADR-0007): Markdown to HTML blocks off the main thread. Its output is
// untrusted until the main thread sanitizes it.
import { type Block, renderBlocks } from "./render";

export interface Request {
  id: number;
  text: string;
}
export interface Reply {
  id: number;
  blocks: Block[];
}

self.onmessage = (e: MessageEvent<Request>) => {
  const reply: Reply = { id: e.data.id, blocks: renderBlocks(e.data.text) };
  self.postMessage(reply);
};
