import { describe, expect, it } from "vitest";
import { accounts, mailboxName, rowIdentity } from "./store";
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

describe("mailboxName", () => {
  it("names the mailbox only when there is more than one", () => {
    accounts.value = [{ id: "p1", address: "me@work.test", mirror_account_id: "m1" }];
    expect(mailboxName("m1")).toBeNull();
    accounts.value = [
      { id: "p1", address: "me@work.test", mirror_account_id: "m1" },
      { id: "p2", address: "me@home.test", mirror_account_id: "m2", label: "Home" },
    ];
    expect(mailboxName("m1")).toBe("me@work.test");
    expect(mailboxName("m2")).toBe("Home");
    expect(mailboxName("p1")).toBe("me@work.test");
    expect(mailboxName("unknown")).toBeNull();
    accounts.value = [];
  });
});
