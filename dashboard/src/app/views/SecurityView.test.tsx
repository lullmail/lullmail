// @vitest-environment jsdom
import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SecurityView } from "./SecurityView";
import { resetSelection, setList } from "../lib/store";

const host = document.createElement("div");

// Preact's act suppresses deferred re-renders while its callback runs, so
// assertions wait on short settle sleeps inside act rather than polling.
const settle = (ms = 30) => new Promise((r) => setTimeout(r, ms));

const jsonResponse = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": status === 428 ? "application/problem+json" : "application/json" },
  });

let totpBeginCalls = 0;
let reauthenticateCalls = 0;

const installFetch = () =>
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = (init?.method || (init?.body ? "POST" : "GET")).toUpperCase();
    if (url.endsWith("/api/security") && method === "GET") {
      return jsonResponse(200, { email: "owner@example.test", passkeys: [], totp_enabled: false, password_set: true, recovery_codes_remaining: 10 });
    }
    if (url.endsWith("/api/security/sessions")) return jsonResponse(200, []);
    if (url.endsWith("/api/push")) return jsonResponse(200, { configured: false, subscribed: false, public_key: "" });
    if (url.endsWith("/api/security/agent-tokens")) return jsonResponse(200, []);
    if (url.endsWith("/api/security/totp/begin")) {
      totpBeginCalls++;
      if (totpBeginCalls === 1) {
        return jsonResponse(428, { title: "Fresh Confirmation Required", detail: "confirm your password to continue" });
      }
      return jsonResponse(200, { secret: "JBSWY3DPEHPK3PXP", uri: "otpauth://totp/lullmail:owner" });
    }
    if (url.endsWith("/api/security/reauthenticate")) {
      reauthenticateCalls++;
      return jsonResponse(200, { ok: true });
    }
    return jsonResponse(404, { title: "Not Found" });
  }));

beforeEach(() => {
  totpBeginCalls = 0;
  reauthenticateCalls = 0;
  installFetch();
});

afterEach(() => {
  render(null, host);
  vi.unstubAllGlobals();
});

describe("re-authentication prompt", () => {
  it("opens the password dialog on 428, confirms, and retries the parked action", async () => {
    resetSelection();
    setList({ kind: "none", key: "security", loading: false, error: null, rows: [], senders: [], origin: null });
    render(<SecurityView />, host);
    await act(async () => { await settle(); });
    if (!host.textContent!.includes("Authenticator app")) throw new Error("view did not load: " + host.textContent!.slice(0, 120));

    // The gated enrollment is refused with 428 and becomes a dialog, not
    // an error (audit AUTH-02).
    await act(async () => {
      const setUp = [...host.querySelectorAll("button")].find((b) => b.textContent === "Set up")!;
      setUp.click();
      await settle();
    });
    const dialog = host.querySelector('[role="dialog"]');
    expect(dialog).not.toBeNull();
    expect(dialog?.textContent).toContain("Confirm your password to continue");

    await act(async () => {
      const input = host.querySelector<HTMLInputElement>("#reauth-password")!;
      input.value = "correct horse";
      input.dispatchEvent(new Event("input", { bubbles: true }));
      await new Promise((r) => setTimeout(r, 0));
    });
    await act(async () => {
      const confirm = [...host.querySelectorAll("button")].find((b) => b.textContent === "Confirm")!;
      confirm.click();
      await settle(50);
    });

    expect(reauthenticateCalls).toBe(1);
    expect(totpBeginCalls).toBe(2);
    expect(host.querySelector('[role="dialog"]')).toBeNull();
    // The retried enrollment finished: the setup panel with the secret is
    // showing now.
    expect(host.textContent).toContain("JBSWY3DPEHPK3PXP");
  });

  it("surfaces a wrong confirmation password without losing the parked action", async () => {
    resetSelection();
    setList({ kind: "none", key: "security", loading: false, error: null, rows: [], senders: [], origin: null });
    vi.unstubAllGlobals();
    totpBeginCalls = 0;
    reauthenticateCalls = 0;
    const wrongFetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = (init?.method || (init?.body ? "POST" : "GET")).toUpperCase();
      if (url.endsWith("/api/security") && method === "GET") {
        return jsonResponse(200, { email: "owner@example.test", passkeys: [], totp_enabled: false, password_set: true, recovery_codes_remaining: 10 });
      }
      if (url.endsWith("/api/security/totp/begin")) {
        totpBeginCalls++;
        return jsonResponse(428, { title: "Fresh Confirmation Required", detail: "confirm your password to continue" });
      }
      if (url.endsWith("/api/security/reauthenticate")) {
        reauthenticateCalls++;
        return jsonResponse(403, { title: "Password Incorrect", detail: "enter your current password to confirm" });
      }
      if (url.endsWith("/api/security/sessions")) return jsonResponse(200, []);
      if (url.endsWith("/api/security/agent-tokens")) return jsonResponse(200, []);
      if (url.endsWith("/api/push")) return jsonResponse(200, { configured: false, subscribed: false, public_key: "" });
      return jsonResponse(200, {});
    });
    vi.stubGlobal("fetch", wrongFetch);

    render(<SecurityView />, host);
    await act(async () => { await settle(); });
    if (!host.textContent!.includes("Authenticator app")) throw new Error("view did not load: " + host.textContent!.slice(0, 120));
    await act(async () => {
      const setUp = [...host.querySelectorAll("button")].find((b) => b.textContent === "Set up")!;
      setUp.click();
      await settle();
    });
    expect(host.querySelector('[role="dialog"]')).not.toBeNull();

    await act(async () => {
      const input = host.querySelector<HTMLInputElement>("#reauth-password")!;
      input.value = "wrong";
      input.dispatchEvent(new Event("input", { bubbles: true }));
      await new Promise((r) => setTimeout(r, 0));
    });
    await act(async () => {
      const confirm = [...host.querySelectorAll("button")].find((b) => b.textContent === "Confirm")!;
      confirm.click();
      await settle();
    });

    expect(reauthenticateCalls).toBe(1);
    expect(totpBeginCalls).toBe(1); // the action was not retried
    const dialog = host.querySelector('[role="dialog"]');
    expect(dialog).not.toBeNull(); // still parked
    expect(dialog?.textContent).toContain("enter your current password to confirm");
  });
});
