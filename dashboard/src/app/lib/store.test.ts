import { describe, expect, it } from "vitest";
import { draftMetadata, rowIdentity, worthRestoring } from "./store";

describe("rowIdentity", () => {
  it("keeps provider-local message ids distinct across accounts", () => {
    const first = rowIdentity({ account: "account-a", message_id: "message-1" });
    const second = rowIdentity({ account: "account-b", message_id: "message-1" });
    expect(first).not.toBe(second);
  });
});

describe("draft ring persistence (audit 4 F05)", () => {
  it("strips attachment payloads and marks their presence", () => {
    const stored = draftMetadata({
      id: "d1", to: "a@b.c", subject: "s", body: "b",
      attachments: [{ filename: "f", contentType: "text/plain", dataBase64: "ZGF0YQ==" }],
    });
    expect(stored.hasAttachments).toBe(true);
    expect(stored).not.toHaveProperty("attachments");
    expect(JSON.stringify(stored)).not.toContain("ZGF0YQ==");
  });
  it("keeps attachment-only and cc-only drafts restorable", () => {
    expect(worthRestoring({ id: "d1", to: "", subject: "", body: "", hasAttachments: true })).toBe(true);
    expect(worthRestoring({ id: "d2", to: "", cc: "someone@example.com", subject: "", body: "" })).toBe(true);
    expect(worthRestoring({ id: "d3", to: "", subject: "", body: "" })).toBe(false);
    expect(worthRestoring({ id: "d4", to: "", subject: "", body: "", hasAttachments: false })).toBe(false);
  });
});
