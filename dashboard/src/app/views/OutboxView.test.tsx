// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { h, render } from "preact";
import { act } from "preact/test-utils";
vi.mock("../lib/api", () => ({ api: vi.fn() }));
vi.mock("../lib/store", () => ({ accounts: { value: [{ id: "account", address: "sender@example.test" }] }, openCompose: vi.fn(), setList: vi.fn(), showError: vi.fn(), showToast: vi.fn() }));
import { api } from "../lib/api";
import { openCompose, setList } from "../lib/store";
import { OutboxView, type OutboxEntry } from "./OutboxView";
let host: HTMLDivElement;
const row = (status: OutboxEntry["status"]): OutboxEntry => ({ id: "job", account_id: "account", status, filing_status: "not_started", created_at: new Date().toISOString(), undo_until: new Date(Date.now() + 5000).toISOString(), recoverable: true, saved_sent_copy: false });
const flush = async () => { await act(async () => { await Promise.resolve(); await Promise.resolve(); }); };
const click = async (label: string) => { const button = [...host.querySelectorAll("button")].find((b) => b.textContent === label); expect(button).toBeTruthy(); await act(async () => { button!.click(); }); await flush(); };
beforeEach(() => { vi.clearAllMocks(); vi.mocked(api).mockReset(); host = document.createElement("div"); document.body.append(host); });
afterEach(() => { act(() => render(null, host)); host.remove(); vi.restoreAllMocks(); });
describe("durable outbox", () => {
  it("fences mail keyboard actions and cancels only the specified pending job", async () => {
    vi.mocked(api).mockResolvedValue([row("pending")] as never);
    await act(async () => render(h(OutboxView, {}), host)); await flush();
    expect(setList).toHaveBeenCalledWith(expect.objectContaining({ kind: "none" }));
    vi.mocked(api).mockResolvedValue({ cancelled: "job" } as never);
    await click("Cancel send"); expect(api).toHaveBeenCalledWith("/outbox/job", { method: "DELETE" });
    expect(host.textContent).toContain("cancelled");
  });
  it("requires deliberate review before restoring an ambiguous composition with all fields", async () => {
    vi.mocked(api).mockResolvedValueOnce([row("ambiguous")] as never);
    await act(async () => render(h(OutboxView, {}), host)); await flush();
    expect(openCompose).not.toHaveBeenCalled(); expect(host.textContent).toContain("may already have sent");
    vi.mocked(api).mockResolvedValueOnce({ status: "ambiguous", request: { to: "to@example.test", cc: "cc@example.test", bcc: "bcc@example.test", subject: "Saved", text: "", html: "<img src=x onerror=alert(1)>", account_id: "account", reply_to_message_id: "parent", attachments: [{ filename: "a.bin", content_type: "application/octet-stream", data_base64: "AA==" }] } } as never);
    await click("Review saved composition"); expect(host.querySelector("img")).toBeNull(); expect(openCompose).not.toHaveBeenCalled();
    await click("Create a new draft from this copy");
    expect(openCompose).toHaveBeenCalledWith(expect.objectContaining({ cc: "cc@example.test", bcc: "bcc@example.test", htmlMode: true, accountId: "account", replyToId: "parent", attachments: [{ filename: "a.bin", contentType: "application/octet-stream", dataBase64: "AA==" }] }));
    expect(vi.mocked(api).mock.calls.every(([path]) => path !== "/send")).toBe(true);
  });
  it("does not remove a recovery copy when destructive confirmation is declined", async () => {
    vi.mocked(api).mockResolvedValue([row("failed")] as never);
    vi.spyOn(window, "confirm").mockReturnValue(false);
    await act(async () => render(h(OutboxView, {}), host)); await flush();
    await click("Remove saved composition");
    expect(api).toHaveBeenCalledTimes(1);
  });
  it("labels Sent-copy uncertainty separately from provider acceptance", async () => {
    vi.mocked(api).mockResolvedValue([{ ...row("submitted"), filing_status: "ambiguous", recoverable: false, saved_sent_copy: true }] as never);
    await act(async () => render(h(OutboxView, {}), host)); await flush();
    expect(host.textContent).toContain("Do not resend");
    expect(host.querySelector("a[download]")?.getAttribute("href")).toBe("/api/outbox/job?format=eml");
    expect(host.textContent).not.toContain("Create a new draft");
    vi.spyOn(window, "confirm").mockReturnValue(true); vi.mocked(api).mockResolvedValueOnce(null as never);
    await click("Remove saved Sent copy"); expect(api).toHaveBeenCalledWith("/outbox/job/payload", { method: "DELETE" });
    expect(host.querySelector("a[download]")).toBeNull(); expect(host.textContent).toContain("submitted");
  });
  it("removes the polling timer on unmount", async () => {
    vi.mocked(api).mockResolvedValue([] as never); const clear = vi.spyOn(window, "clearInterval");
    await act(async () => render(h(OutboxView, {}), host)); await flush();
    act(() => render(null, host)); expect(clear).toHaveBeenCalled();
  });
});
