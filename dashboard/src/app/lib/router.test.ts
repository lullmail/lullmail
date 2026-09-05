import { describe, expect, it } from "vitest";
import { folderLabel, folderPath, routeFor } from "./router";

describe("folder routes", () => {
  it("resolves a provider mailbox to the folder view", () => {
    const r = routeFor("/folder/sent");
    expect(r.kind).toBe("folder");
    expect(r.folder).toBe("sent");
    expect(r.title).toBe("Sent");
  });

  it("round-trips a name that needs encoding", () => {
    const name = "[gmail]/all mail";
    expect(routeFor(folderPath(name)).folder).toBe(name);
  });

  it("keeps dotted IMAP hierarchies intact", () => {
    expect(routeFor(folderPath("Archive.2024")).folder).toBe("Archive.2024");
  });

  it("does not mistake the table routes for folders", () => {
    expect(routeFor("/receipts").kind).toBe("bucket");
    expect(routeFor("/settings/mail").kind).toBe("settings-mail");
  });

  it("falls back to the inbox for a bare or unknown path", () => {
    expect(routeFor("/folder/").kind).toBe("bucket");
    expect(routeFor("/nope").kind).toBe("bucket");
  });

  it("survives a malformed escape rather than throwing", () => {
    expect(() => routeFor("/folder/%E0%A4%A")).not.toThrow();
  });
});

describe("folderLabel", () => {
  it("capitalises each word without touching separators", () => {
    expect(folderLabel("sent")).toBe("Sent");
    expect(folderLabel("all mail")).toBe("All Mail");
    expect(folderLabel("[gmail]/all mail")).toBe("[Gmail]/All Mail");
  });
});
