// @vitest-environment jsdom
import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, describe, expect, it } from "vitest";
import { authStatus } from "../lib/api";
import { Gate } from "./Gate";

const host = document.createElement("div");

afterEach(() => {
  render(null, host);
  authStatus.value = null;
});

describe("first-run setup", () => {
  it("advances from the token to owner and password setup", () => {
    authStatus.value = {
      configured: false,
      authenticated: false,
      email: "",
      bootstrap_available: true,
      passkey_supported: true,
      detected_origin: "https://mail.example.test",
    };
    render(<Gate />, host);

    act(() => host.querySelector<HTMLButtonElement>("button")!.click());
    expect(host.textContent).toContain("Enter your setup code");

    const token = host.querySelector<HTMLInputElement>("#setup-token")!;
    act(() => {
      token.value = "one-time-token";
      token.dispatchEvent(new Event("input", { bubbles: true }));
    });
    act(() => host.querySelector<HTMLFormElement>("form")!.requestSubmit());

    expect(host.textContent).toContain("Who's this mailbox for?");
    expect(host.querySelector("#setup-name")).not.toBeNull();
    expect(host.querySelector("#setup-password")).not.toBeNull();
    expect(host.textContent).toContain("Create account");
    expect(host.textContent).toContain("Create a passkey instead");
  });
});

describe("signed-out gate", () => {
  it("shows the password form first; passkeys are behind Other ways to sign in", () => {
    authStatus.value = {
      configured: true,
      authenticated: false,
      email: "",
      bootstrap_available: false,
      passkey_supported: true,
    };
    render(<Gate />, host);

    expect(host.querySelector<HTMLInputElement>("#gate-email")).not.toBeNull();
    expect(host.querySelector<HTMLInputElement>("#gate-password")).not.toBeNull();
    expect(host.textContent).not.toContain("Continue with a passkey");

    act(() => host.querySelector<HTMLButtonElement>(".gate-link")!.click());
    expect(host.textContent).toContain("Continue with a passkey");
    expect(host.textContent).toContain("Use a recovery code");
  });
});

describe("unreachable server", () => {
  it("names a connection problem instead of echoing the browser's fetch error", async () => {
    const { describeAuthError } = await import("./Gate");
    expect(describeAuthError(new TypeError("Failed to fetch"), "Sign-in failed")).toContain("Can't reach Lull Mail");
    expect(describeAuthError(new DOMException("cancelled", "NotAllowedError"), "x")).toContain("dismissed");
    expect(describeAuthError(new Error("bad code"), "x")).toBe("bad code");
  });

  it("offers a retry rather than a passkey prompt", async () => {
    const { Unreachable } = await import("./Gate");
    render(<Unreachable />, host);
    expect(host.textContent).toContain("Can't reach your mailbox");
    expect(host.textContent).not.toContain("Continue with a passkey");
    expect(host.querySelector("button")!.textContent).toContain("Try again");
  });
});
