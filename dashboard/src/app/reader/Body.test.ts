// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { cleanLinks, emailHasOwnColors, stripRemoteImages } from "./Body";
import { canQueue } from "../lib/offline";

describe("mail privacy", () => {
  it("blocks quoted and unquoted remote images without touching inline images", () => {
    const result = stripRemoteImages('<img src="https://track.test/p.gif"><img srcset="https://track.test/2x.gif 2x"><img src=https://track.test/b.gif><img src="cid:logo"><img src="data:image/png,x">');
    expect(result.blocked).toBe(3);
    expect(result.html).not.toContain("track.test");
    expect(result.html).toContain("cid:logo");
    expect(result.html).toContain("data:image/png");
  });

  it("unwraps measured links, drops campaign parameters, and strips active content", () => {
    const nested = encodeURIComponent("https://example.com/story?utm_source=newsletter&keep=yes");
    const result = cleanLinks(`<script>alert(1)</script><iframe src="https://track.test"></iframe><a onclick="steal()" href="https://click.test/r?url=${nested}">Read</a><a id="bad" href="javascript:alert(1)">Bad</a>`);
    const doc = new DOMParser().parseFromString(result, "text/html");
    const link = doc.querySelector("a")!;
    expect(doc.querySelector("script")).toBeNull();
		expect(doc.querySelector("iframe")).toBeNull();
		expect(doc.querySelector("#bad")?.hasAttribute("href")).toBe(false);
    expect(link.hasAttribute("onclick")).toBe(false);
    expect(link.href).toBe("https://example.com/story?keep=yes");
    expect(link.rel).toContain("noreferrer");
  });
});

describe("offline queue safety", () => {
  it("queues reversible filing but never send, account, or credential changes", () => {
    expect(canQueue("/messages/m1/action", "POST")).toBe(true);
    expect(canQueue("/screener/decide", "POST")).toBe(true);
    expect(canQueue("/send", "POST")).toBe(false);
    expect(canQueue("/account", "DELETE")).toBe(false);
    expect(canQueue("/security/totp", "DELETE")).toBe(false);
  });
});

describe("email theming", () => {
  it("detects mail that paints its own background, in several authoring styles", () => {
    expect(emailHasOwnColors(`<body bgcolor="#ffffff">hi</body>`)).toBe(true);
    expect(emailHasOwnColors(`<div style="background:#fff">hi</div>`)).toBe(true);
    expect(emailHasOwnColors(`<table style="background-color:#f6f6f6"><tr><td>x</td></tr></table>`)).toBe(true);
    // Color-only styling does NOT count: without a painted background the
    // text sits on our themed canvas, where forced colors would clash.
    expect(emailHasOwnColors(`<span style="color:#333">hi</span>`)).toBe(false);
    expect(emailHasOwnColors(`<p>Plain personal mail.</p>`)).toBe(false);
  });
});
