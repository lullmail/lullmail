import { render } from "preact";
import { MessageBody, cleanLinks, stripRemoteImages } from "../src/app/reader/Body";

const tracker = location.origin + "/__tracking";
const html = `<body style="background:white"><style>@import url('${tracker}?import');p{background:url('${tracker}?css')}</style><p>Private mail</p><img src="${tracker}?img"><img srcset="${tracker}?srcset 1x"><iframe src="${tracker}?frame"></iframe><svg><image href="${tracker}?svg" /></svg><link rel="preload" href="${tracker}?preload"><script src="${tracker}?script"></script></body>`;
const originalFetch = globalThis.fetch;
(globalThis as any).privacyFetches = [];
globalThis.fetch = async (input, options) => {
 const url = String(input);
 if (url === "/api/messages/privacy-fixture/attachment/inline?account=privacy-account") {
  (globalThis as any).privacyFetches.push({url, credentials: options?.credentials});
  const png = Uint8Array.from(atob("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jZ1kAAAAASUVORK5CYII="), c => c.charCodeAt(0));
  return new Response(png, {headers: {"Content-Type": "application/octet-stream"}});
 }
 return originalFetch(input, options);
};
const safe = stripRemoteImages(cleanLinks(html));
(globalThis as any).privacyResult = safe;
render(<MessageBody html={html + '<img alt="Inline logo" src="cid:logo">'} account="privacy-account" inlineParts={[{part_id:"inline",content_id:"logo",type:"image/png",size:68}]} text="Private mail" messageId="privacy-fixture" sender="sender@example.test" />,document.getElementById("app")!);

requestAnimationFrame(() => { document.documentElement.dataset.privacyReady = "true"; });
