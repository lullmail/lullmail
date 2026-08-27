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
  it("advances from the token to owner and passkey setup", () => {
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
    expect(host.querySelector("#setup-email")).not.toBeNull();
  });
});
