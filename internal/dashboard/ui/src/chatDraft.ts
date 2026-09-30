import { useState } from "react";
import type { ComposerAsset } from "./composerAssets";

// Owned by the shell so hiding the chat does not discard work in progress.
// Each tab keeps its own draft, only until the page is reloaded.
export function useChatDraft() {
  const [message, setMessage] = useState("");
  const [assets, setAssets] = useState<ComposerAsset[]>([]);
  return { message, setMessage, assets, setAssets };
}

export type ChatDraft = ReturnType<typeof useChatDraft>;
