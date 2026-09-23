import { describe, expect, it } from "vitest";
import {
  composeMessage,
  messageLimitError,
  readAsset,
  type ComposerAsset,
} from "./composerAssets";

const asset = (name: string, content: string): ComposerAsset => ({
  id: name,
  name,
  size: content.length,
  content,
});

describe("composer assets", () => {
  it("fences each asset beyond any backtick run it contains", () => {
    expect(
      composeMessage("  Look  ", [
        asset("a.md", "```go\nx\n```\n"),
        asset("b.txt", "plain"),
      ]),
    ).toBe(
      "Look\n\nAttached file: a.md\n````\n```go\nx\n```\n````\n\nAttached file: b.txt\n```\nplain\n```",
    );
  });
  it("measures the limit in bytes, as the daemon does", () => {
    expect(messageLimitError("é".repeat(12_000))).toBe("");
    expect(messageLimitError("é".repeat(12_001))).toContain(
      "come to 24,002 bytes; the limit is 24,000 bytes",
    );
  });
  it("accepts text by extension when the browser gives no type, and refuses binary content", async () => {
    const ok = await readAsset(new File(["package main\n"], "main.go"));
    expect("asset" in ok && ok.asset.content).toBe("package main\n");
    const pdf = await readAsset(
      new File(["%PDF"], "brief.pdf", { type: "application/pdf" }),
    );
    expect("error" in pdf && pdf.error).toContain("application/pdf");
    const nul = await readAsset(new File(["a\0b"], "log.txt"));
    expect("error" in nul && nul.error).toContain("binary data");
  });
});
