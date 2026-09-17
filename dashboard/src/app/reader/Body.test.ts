// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { cleanLinks, emailHasOwnColors, frameDoc, stripRemoteImages } from "./Body";
import { canQueue } from "../lib/offline";

describe("mail privacy", () => {
  it("blocks quoted and unquoted remote images without touching inline images", () => {
    const result = stripRemoteImages('<img src="https://track.test/p.gif"><img srcset="https://track.test/2x.gif 2x"><img src=https://track.test/b.gif><img src="//track.test/protocol.gif"><img src="cid:logo"><img src="data:image/png,x">');
		expect(result.blocked).toBe(4);
    expect(result.html).not.toContain("track.test");
    expect(result.html).toContain("cid:logo");
    expect(result.html).toContain("data:image/png");
  });

	it("blocks remote CSS and SVG references while retaining local embedded resources", () => {
		const result = stripRemoteImages(`
			<style>.remote{background:image-set(url(https://track.test/a.png) 1x)}</style>
			<div style="background:url(//track.test/b.png)">remote</div>
			<div style="background:url(data:image/png;base64,eA==)">embedded</div>
			<div style="background:url(cid:paper)">local</div>
			<svg><image href="https://track.test/c.png"/><use href="https://track.test/icons.svg#x"/><use href="#local"/><rect filter="url(https://track.test/filter.svg#x)"/></svg>
		`);
		expect(result.html).not.toContain("track.test");
		expect(result.html).toContain("data:image/png");
		expect(result.html).toContain("cid:paper");
		expect(result.html).toContain('href="#local"');
	});

  it("keeps wrapper links intact, drops campaign parameters, and strips active content", () => {
    const nested = encodeURIComponent("https://example.com/story?utm_source=newsletter&keep=yes");
    const result = cleanLinks(`<script>alert(1)</script><iframe src="https://track.test"></iframe><a onclick="steal()" href="https://click.test/r?url=${nested}">Read</a><a id="bad" href="javascript:alert(1)">Bad</a><a id="legit" href="https://example.com/report?to=alice">Report</a><a id="utm" href="https://example.com/x?utm_source=n&amp;keep=1">U</a>`);
    const doc = new DOMParser().parseFromString(result, "text/html");
    const link = doc.querySelector("a")!;
    expect(doc.querySelector("script")).toBeNull();
		expect(doc.querySelector("iframe")).toBeNull();
		expect(doc.querySelector("#bad")?.hasAttribute("href")).toBe(false);
    expect(link.hasAttribute("onclick")).toBe(false);
    // Generic redirect unwrapping is gone: a `url` query parameter is not
    // evidence of redirect semantics, and rewriting broke signed links
    // (audit WEB-08). The wrapper survives untouched.
    expect(link.href).toBe("https://click.test/r?url=" + nested);
    expect(link.rel).toContain("noreferrer");
    // A legitimate `to` parameter is a value, not a redirect.
    expect(doc.querySelector("#legit")?.getAttribute("href")).toBe("https://example.com/report?to=alice");
    // Campaign parameters on ordinary links still scrub.
    expect(doc.querySelector("#utm")?.getAttribute("href")).toBe("https://example.com/x?keep=1");
  });

  it("carries vetted head styles into the sanitized body (audit WEB-09)", () => {
    const result = cleanLinks(`<html><head><style>.card{color:red}</style><style>@import "https://track.test/x.css"</style></head><body><p class="card">x</p></body></html>`);
    expect(result).toContain(".card{color:red}");
    expect(result).not.toContain("@import");
    expect(result).toContain('<p class="card">x</p>');
  });

	it("removes imports, event handlers, and resource-capable active elements", () => {
		const result = cleanLinks(`<style>@import "https://track.test/mail.css";</style><link rel="stylesheet" href="//track.test/x.css"><video poster="//track.test/p.jpg"></video><p onanimationstart="steal()">Safe</p>`);
		expect(result).not.toContain("track.test");
		expect(result).not.toContain("onanimationstart");
		expect(result).toContain("Safe");
	});

	it("uses a deny-by-default iframe CSP and relaxes images only after permission", () => {
		const blocked = frameDoc("<p>mail</p>", true, false);
		const allowed = frameDoc("<p>mail</p>", true, true);
		expect(blocked).toContain("default-src 'none'; img-src data: cid:; style-src 'unsafe-inline'; script-src 'none'");
		expect(blocked).not.toContain("img-src data: cid: http:");
		expect(allowed).toContain("img-src data: cid: http: https:");
		expect(allowed).toContain("script-src 'none'");
		expect(allowed).toContain("connect-src 'none'");
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
