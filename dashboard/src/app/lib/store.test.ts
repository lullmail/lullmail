import { describe, expect, it } from "vitest";
import { rowIdentity } from "./store";
import { worthRestoring } from "./offline";

describe("rowIdentity", () => {
  it("keeps provider-local message ids distinct across accounts", () => {
    const first = rowIdentity({ account: "account-a", message_id: "message-1" });
    const second = rowIdentity({ account: "account-b", message_id: "message-1" });
    expect(first).not.toBe(second);
  });
});

describe("draft restore worth (audit 4 F05, single-record offline-v2)", () => {
  it("keeps attachment-only and cc-only drafts restorable", () => {
    expect(worthRestoring({ to: "", subject: "", body: "", attachments: [{ filename: "f", contentType: "text/plain", dataBase64: "ZGF0YQ==" }] })).toBe(true);
    expect(worthRestoring({ to: "", cc: "someone@example.com", subject: "", body: "" })).toBe(true);
    expect(worthRestoring({ to: "", subject: "", body: "" })).toBe(false);
    expect(worthRestoring({ to: "", subject: "", body: "", attachments: [] })).toBe(false);
  });
});
