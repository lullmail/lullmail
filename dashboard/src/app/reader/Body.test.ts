// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { cleanLinks, emailHasOwnColors, stripRemoteImages, replaceInlineImages, frameDoc } from "./Body";

describe("body-level presentation survives sanitization (audit 4 F21)", () => {
  it("carries a sanitized body background onto a wrapper the color check can see", () => {
    const html = '<body style="background:#ffffff; color:#111111"><p>Canvas mail</p></body>';
    const cleaned = cleanLinks(html);
    expect(cleaned).toContain("background");
    expect(emailHasOwnColors(cleaned)).toBe(true);
  });

  it("carries a body bgcolor that names a real color", () => {
    const html = '<body bgcolor="white"><p>Old-school mail</p></body>';
    const cleaned = cleanLinks(html);
    expect(emailHasOwnColors(cleaned)).toBe(true);
  });

  it("leaves unstyled mail to the app theme", () => {
    const cleaned = cleanLinks("<p>Plain mail</p>");
    expect(emailHasOwnColors(cleaned)).toBe(false);
  });

  it("does not smuggle a network-backed background through the wrapper", () => {
    const html = '<body style=\'background:url("https://tracker.example/pixel.png")\'><p>Mail</p></body>';
    // cleanLinks alone intentionally permits remote resources (the allow
    // decision is the reader's); the blocking pipeline is cleanLinks
    // followed by stripRemoteImages, which must also clean the wrapper.
    const blocked = stripRemoteImages(cleanLinks(html)).html;
    expect(blocked).not.toContain("tracker.example");
  });

  it("keeps body content inside the wrapper", () => {
    const cleaned = cleanLinks('<body style="background:#fff"><p>Kept</p></body>');
    expect(cleaned).toContain("<p>Kept</p>");
  });
});

it("never asks DOMParser to parse sender HTML and keeps head/body presentation", () => {
  const previous = globalThis.DOMParser;
  globalThis.DOMParser = class { constructor() { throw new Error("network-capable parser forbidden"); } } as unknown as typeof DOMParser;
  try {
    const cleaned = cleanLinks('<html><head><style>p{color:red}</style><link href="https://trap.test/preload"></head><body bgcolor="white"><p>Visible mail</p><img src="https://trap.test/image"><iframe src="https://trap.test/frame"></iframe></body></html>');
    const safe = stripRemoteImages(cleaned);
    expect(safe.html).toContain("Visible mail");
    expect(safe.html).toContain("p{color:red}");
    expect(safe.html).not.toContain("trap.test");
    expect(safe.blocked).toBe(1);
  } finally { globalThis.DOMParser = previous; }
});

it("resolves exact MIME content ids only to authenticated local images", () => {
 const html = replaceInlineImages('<img src="cid:logo%40example"><img src="cid:unknown">',new Map([["logo@example","blob:http://localhost/local-image"]]));
 expect(html).toContain('src="blob:http://localhost/local-image"');
 expect(html).not.toContain("cid:");
 expect(frameDoc(html, false)).toContain("img-src data: blob:");
});
