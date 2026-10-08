// Message bodies. HTML goes into a sandboxed iframe with the app's own theme
// injected; plain text keeps quoted replies collapsed. Remote images are
// stripped by default because a one-pixel image is how senders learn you
// opened the mail.
import { useEffect, useRef, useState } from "preact/hooks";
import { allowImages, allowSenderImages, imageSenders, reader, theme } from "../lib/store";
import { offlineGeneration, generationCurrent } from "../lib/offline";
import { Icon } from "../ui/Icon";

import { parse, serializeOuter, type DefaultTreeAdapterMap } from "parse5";

function parseMailDocument(html: string) {
  const parsed = parse(html);
  const root = parsed.childNodes.find((node) => "tagName" in node && node.tagName === "html") as DefaultTreeAdapterMap["element"];
  const head = root.childNodes.find((node) => "tagName" in node && node.tagName === "head")!;
  const body = root.childNodes.find((node) => "tagName" in node && node.tagName === "body")!;
  const template = document.createElement("template");
  template.innerHTML = serializeOuter(head).replace(/^<head/, '<div data-mail-head=""').replace(/<\/head>$/, "</div>") +
    serializeOuter(body).replace(/^<body/, '<div data-mail-body=""').replace(/<\/body>$/, "</div>");
  const fragment = template.content;
  return {
    head: fragment.querySelector<HTMLElement>("[data-mail-head]")!,
    body: fragment.querySelector<HTMLElement>("[data-mail-body]")!,
    querySelectorAll: <T extends Element = Element>(selector: string) => fragment.querySelectorAll<T>(selector),
    createElement: (tag: string) => template.content.ownerDocument.createElement(tag),
  };
}

const PLACEHOLDER =
  "<span style=\"display:inline-flex;align-items:center;justify-content:center;" +
  "border:1.5px dashed var(--ph-line);border-radius:8px;color:var(--ph-ink);" +
  "font:12px sans-serif;padding:10px 14px\">image blocked</span>";

function imageUrlAllowed(value: string, allowRemote: boolean): boolean {
	const url = value.trim().replace(/^(?:"([\s\S]*)"|'([\s\S]*)')$/, "$1$2");
	if (!url) return true;
	if (/^(?:data:|cid:|blob:|#)/i.test(url)) return true;
	if (!allowRemote) return false;
	try {
		const parsed = new URL(url, "https://mail.invalid/");
		return parsed.protocol === "http:" || parsed.protocol === "https:";
	} catch {
		return false;
	}
}

function srcsetUrls(value: string): string[] {
	const urls: string[] = [];
	let offset = 0;
	while (offset < value.length) {
		while (/[\s,]/.test(value[offset] || "")) offset++;
		if (offset >= value.length) break;
		const start = offset;
		if (/^data:/i.test(value.slice(offset))) {
			while (offset < value.length && !/\s/.test(value[offset])) offset++;
		} else {
			while (offset < value.length && !/[\s,]/.test(value[offset])) offset++;
		}
		urls.push(value.slice(start, offset));
		while (offset < value.length && value[offset] !== ",") offset++;
		offset++;
	}
	return urls;
}

function cssHasBlockedResource(css: string, allowRemote: boolean): boolean {
	if (/@\s*import\b/i.test(css)) return true;
	const urls = [...css.matchAll(/url\(\s*((?:"[^"]*"|'[^']*'|[^)]*))\s*\)/gi)].map((match) => match[1]);
	if (urls.some((url) => !imageUrlAllowed(url, allowRemote))) return true;
	for (const match of css.matchAll(/(?:-webkit-)?image-set\s*\(([^)]*)\)/gi)) {
		const candidates = [...match[1].matchAll(/(?:^|,)\s*(?:url\(\s*)?("[^"]*"|'[^']*'|[^\s,)]+)/gi)].map((candidate) => candidate[1]);
		if (!candidates.length || candidates.some((url) => !imageUrlAllowed(url, allowRemote))) return true;
	}
	return false;
}

function sanitizeImageResources(doc: ReturnType<typeof parseMailDocument>, allowRemote: boolean): number {
	let blocked = 0;
	doc.querySelectorAll<HTMLImageElement>("img").forEach((image) => {
		const src = image.getAttribute("src");
		const srcset = image.getAttribute("srcset");
		if ((src !== null && !imageUrlAllowed(src, allowRemote)) ||
			(srcset !== null && srcsetUrls(srcset).some((url) => !imageUrlAllowed(url, allowRemote)))) {
			const holder = doc.createElement("span");
			holder.innerHTML = PLACEHOLDER;
			image.replaceWith(holder.firstElementChild!);
			blocked++;
		}
	});
	doc.querySelectorAll<SVGElement>("svg [href], svg [xlink\\:href]").forEach((node) => {
		const href = node.getAttribute("href") || node.getAttribute("xlink:href") || "";
		const imageElement = /^(?:image|feimage)$/i.test(node.localName);
		if ((!imageElement && !href.startsWith("#")) || (imageElement && !imageUrlAllowed(href, allowRemote))) {
			node.remove();
			blocked++;
		}
	});
	doc.querySelectorAll<SVGElement>("svg *").forEach((node) => {
		for (const attr of [...node.attributes]) {
			if (attr.name === "style" || !/url\s*\(/i.test(attr.value) || !cssHasBlockedResource(attr.value, false)) continue;
			node.removeAttribute(attr.name);
			blocked++;
		}
	});
	doc.querySelectorAll<HTMLElement>("[background]").forEach((node) => {
		if (!imageUrlAllowed(node.getAttribute("background") || "", allowRemote)) {
			node.removeAttribute("background");
			blocked++;
		}
	});
	doc.querySelectorAll<HTMLElement>("[style]").forEach((node) => {
		if (cssHasBlockedResource(node.getAttribute("style") || "", allowRemote)) {
			node.removeAttribute("style");
			blocked++;
		}
	});
	doc.querySelectorAll("style").forEach((node) => {
		if (cssHasBlockedResource(node.textContent || "", allowRemote)) {
			node.remove();
			blocked++;
		}
	});
	return blocked;
}

/** Strips every network-backed image source; `data:` and `cid:` stay local. */
export function stripRemoteImages(html: string): { html: string; blocked: number } {
	if (typeof document !== "undefined") {
		const doc = parseMailDocument(html);
		const blocked = sanitizeImageResources(doc, false);
		return { html: doc.body.innerHTML, blocked };
	}
  let blocked = 0;
  const out = html.replace(
    // Quoted and unquoted remote srcs alike; `data:` and `cid:` are untouched
    // because they carry no request back to the sender.
    /<img\b[^>]*\bsrc\s*=\s*(?:(["'])https?:\/\/[^"']*\1|https?:\/\/[^\s>]+)[^>]*>/gi,
    () => {
      blocked++;
      return PLACEHOLDER;
    }
  );
  return { html: out, blocked };
}

const TRACKING_PARAMS = /^(utm_.+|mc_cid|mc_eid|mkt_tok|vero_id|oly_anon_id|oly_enc_id|rb_clickid)$/i;

/** Removes common click-measurement parameters. Parsing is deliberately
    browser-native: malformed mail remains unchanged.

    Generic redirect unwrapping is gone (audit WEB-08): treating query
    parameters like `to`, `url`, `u` or `target` as redirects on arbitrary
    hosts rewrote legitimate links (`?to=alice` became `/alice`), breaking
    signed action links. Redirect targets are attacker-influencable and a
    parameter name is not evidence of redirect semantics; a specific
    tracker unwrapper would need per-hostname tests. */
export function cleanLinks(html: string): string {
  if (typeof document === "undefined") return html;
  const doc = parseMailDocument(html);
  doc.querySelectorAll("script,object,embed,form,meta[http-equiv],base,iframe,frame,link,video,audio,source,track,input,button,textarea,select").forEach((node) => node.remove());
  doc.querySelectorAll<HTMLElement>("*").forEach((node) => {
    for (const attr of [...node.attributes]) if (/^on/i.test(attr.name)) node.removeAttribute(attr.name);
		for (const name of ["ping", "srcdoc", "action", "formaction"]) node.removeAttribute(name);
  });
	// Imported stylesheets are never an image permission and stay blocked in both modes.
	doc.querySelectorAll<HTMLElement>("[style]").forEach((node) => {
		if (/@\s*import\b/i.test(node.getAttribute("style") || "")) node.removeAttribute("style");
	});
	doc.querySelectorAll("style").forEach((node) => {
		if (/@\s*import\b/i.test(node.textContent || "")) node.remove();
	});
	sanitizeImageResources(doc, true);
  doc.querySelectorAll<HTMLAnchorElement>("a[href]").forEach((anchor) => {
    try {
      const target = new URL(anchor.href);
			if (!["http:", "https:", "mailto:"].includes(target.protocol)) { anchor.removeAttribute("href"); return; }
			if (target.protocol === "http:" || target.protocol === "https:") {
				for (const key of [...target.searchParams.keys()]) if (TRACKING_PARAMS.test(key)) target.searchParams.delete(key);
			}
      anchor.href = target.toString();
      anchor.rel = "noopener noreferrer";
      anchor.target = "_blank";
    } catch { anchor.removeAttribute("href"); }
  });
  // Head <style> rules are vetted presentation, not executable content;
  // carrying them keeps legitimate email layout instead of discarding it
  // with the rest of the head (audit WEB-09). They move ahead of the body
  // content inside the same sanitized document the caller serializes.
  const headStyles = [...doc.head.querySelectorAll("style")]
    .filter((node) => !/@\s*import\b/i.test(node.textContent || ""))
    .map((node) => node.cloneNode(true));
  for (const style of headStyles.reverse()) doc.body.prepend(style);
  // Body-level presentation survives serialization: cleanLinks returns
  // body.innerHTML, which drops the body element's own (already sanitized)
  // style and bgcolor — so a white-canvas email lost its canvas to the
  // dark theme one decision later, in emailHasOwnColors (audit 4 F21).
  // The vetted attributes move onto a wrapper inside the document; no
  // event handlers, network-backed backgrounds, or unsanitized values
  // are introduced — sanitizeImageResources above already cleaned the
  // body's style/background attributes in place.
  const wrapper = doc.createElement("div");
  const bodyStyle = doc.body.getAttribute("style");
  if (bodyStyle) wrapper.setAttribute("style", bodyStyle);
  const bgcolor = doc.body.getAttribute("bgcolor");
  if (bgcolor && typeof CSS !== "undefined" && CSS.supports("color", bgcolor)) {
    wrapper.style.backgroundColor = bgcolor;
  }
  while (doc.body.firstChild) wrapper.appendChild(doc.body.firstChild);
  doc.body.appendChild(wrapper);
  return doc.body.innerHTML;
}

function cssVar(name: string): string {
  if (typeof document === "undefined") return "";
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

/** An email "brings its own colors" when it paints a background anywhere —
    bgcolor or inline background styles. Those get to keep their palette; we
    never force dark-theme ink onto a white-shipping email. Everything else
    is themed by us, ink and canvas both, so contrast is guaranteed. */
export function emailHasOwnColors(html: string): boolean {
  if (typeof document === "undefined") return false;
  const doc = parseMailDocument(html);
  // The body element itself is the classic canvas: <body bgcolor> merges its
  // attributes onto the frame body at srcdoc parse time, so it paints too.
  const paint = "[bgcolor], [background], [style*='background']";
  return doc.body.matches(paint) || !!doc.body.querySelector(paint);
}

export function frameDoc(html: string, themed: boolean, allowRemoteImages = false): string {
  // Themed mail wears our canvas and our ink. Self-colored mail gets a white
  // canvas instead: its own backgrounds and default-black text then read
  // exactly as authored. Without this, a reply with one styled quote or
  // signature renders black-on-dark in a dark theme.
  const bg = themed ? (cssVar("--bg") || "transparent") : "#ffffff";
  const ink = themed ? cssVar("--ink") : "";
  const quoteInk = themed ? cssVar("--ink-2") : "";
	const policy = "default-src 'none'; img-src data: blob:" + (allowRemoteImages ? " http: https:" : "") +
		"; style-src 'unsafe-inline'; script-src 'none'; connect-src 'none'; font-src 'none'; media-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'";
  return (
    "<!doctype html><html><head><meta charset='utf-8'><meta http-equiv='Content-Security-Policy' content=\"" + policy + "\">" +
    "<style>" +
    ":root{--ph-line:" + (cssVar("--line-strong") || "#ccc") + ";--ph-ink:" + (cssVar("--ink-3") || "#999") + "}" +
    "html{margin:0;padding:0;background:" + bg + "}" +
    "body{margin:0;padding:0;font-family:" + cssVar("--sans") + (ink ? ";color:" + ink : "") +
    ";font-size:15px;line-height:1.62;padding:2px 0 8px;word-wrap:break-word;overflow-wrap:anywhere}" +
    "img{max-width:100%;height:auto}" +
    "a{color:" + (cssVar("--accent") || "#4a72d8") + "}" +
    "blockquote{border-left:3px solid " + cssVar("--line-strong") +
    ";margin:8px 0;padding:2px 12px" + (quoteInk ? ";color:" + quoteInk : "") + "}" +
    "pre{white-space:pre-wrap}table{max-width:100%}" +
    "</style></head><body>" + html + "</body></html>"
  );
}

export type InlinePart = { part_id: string; content_id: string; type: string; size: number };
export const inlineImageCap = 10 * 1024 * 1024;
export const inlineAggregateCap = 20 * 1024 * 1024;
// MIME IDs remain case sensitive. Both metadata and references use this exact
// normalization; malformed percent escapes and unmatched brackets are refused.
export function normalizeCID(value: string): string | undefined {
  let cid = value.trim();
  try { cid = decodeURIComponent(cid); } catch { return undefined; }
  if (cid.startsWith("<") && cid.endsWith(">")) cid = cid.slice(1, -1);
  if (!cid || /[\s<>\u0000-\u001f\u007f]/.test(cid)) return undefined;
  return cid;
}
export function referencedInlineParts(html: string, parts: InlinePart[]): InlinePart[] {
  const references = new Set<string>();
  parseMailDocument(cleanLinks(html)).querySelectorAll<HTMLImageElement>("img[src]").forEach(image => {
    const src = image.getAttribute("src") || "";
    if (/^cid:/i.test(src)) { const cid = normalizeCID(src.slice(4)); if (cid) references.add(cid); }
  });
  const selected = new Map<string, InlinePart>();
  for (const part of parts) {
    const cid = normalizeCID(part.content_id);
    if (!cid || !references.has(cid) || selected.has(cid) || !part.part_id ||
        !/^image\/(png|jpeg|gif|webp|avif)$/i.test(part.type) ||
        !Number.isFinite(part.size) || part.size < 0 || part.size > inlineImageCap) continue;
    // First eligible MIME part wins; later duplicate IDs are never fetched.
    selected.set(cid, {...part, content_id: cid});
    if (selected.size === 16) break;
  }
  return [...selected.values()];
}
export function replaceInlineImages(html: string, urls: Map<string, string>): string {
  const doc = parseMailDocument(html);
  doc.querySelectorAll<HTMLImageElement>("img[src]").forEach(image => {
    const src = image.getAttribute("src") || "";
    if (!/^cid:/i.test(src)) return;
    const cid = normalizeCID(src.slice(4));
    const url = cid ? urls.get(cid) : undefined;
    if (url) image.setAttribute("src", url);
    else { const holder = doc.createElement("span"); holder.textContent = "Inline image unavailable"; image.replaceWith(holder); }
  });
  return doc.body.innerHTML;
}

// Count actual delivered chunks, even rejected ones. A stream may overshoot by
// one delivered chunk; no further read or request starts after aggregate exhaustion.
export async function fetchInlineImages(parts: InlinePart[], messageId: string, account: string,
  signal: AbortSignal, current: () => boolean, publish: (cid: string, blob: Blob) => void): Promise<void> {
  let total = 0;
  for (const part of parts) {
    if (signal.aborted || !current() || total >= inlineAggregateCap) return;
    try {
      const response = await fetch("/api/messages/" + encodeURIComponent(messageId) + "/attachment/" + encodeURIComponent(part.part_id) + "?account=" + encodeURIComponent(account), {credentials: "same-origin", signal});
      if (signal.aborted || !current()) { await response.body?.cancel(); return; }
      if (!response.ok || !response.body) { await response.body?.cancel(); continue; }
      const stream = response.body.getReader();
      const chunks: Uint8Array<ArrayBuffer>[] = [];
      let size = 0;
      try {
        while (true) {
          if (signal.aborted || !current()) { await stream.cancel(); return; }
          const result = await stream.read();
          if (signal.aborted || !current()) { await stream.cancel(); return; }
          if (result.done) break;
          size += result.value.length;
          total += result.value.length;
          if (size > inlineImageCap || total > inlineAggregateCap) { await stream.cancel(); break; }
          chunks.push(result.value);
        }
        if (size <= inlineImageCap && total <= inlineAggregateCap && !signal.aborted && current()) {
          publish(part.content_id, new Blob(chunks, {type: part.type}));
        }
      } finally { stream.releaseLock(); }
    } catch { if (signal.aborted || !current()) return; }
  }
}
const noInlineParts: InlinePart[] = [];
function HtmlBody({ html, messageId, sender, account, inlineParts = noInlineParts }: { html: string; messageId: string; sender: string; account?: string; inlineParts?: InlinePart[] }) {
  const ref = useRef<HTMLIFrameElement>(null);
  const senderKey = sender.trim().toLowerCase();
  const ok = reader.value.imagesOk.has(messageId) || imageSenders.value.has(senderKey);
  const [inlineUrls, setInlineUrls] = useState<Map<string, string>>(new Map());
  useEffect(() => {
    const context = offlineGeneration();
    const abort = new AbortController();
    const urls = new Map<string, string>();
    let disposed = false;
    setInlineUrls(new Map());
    const allocated = new Set<string>();
    const revokeAllocated = () => { for (const url of allocated) URL.revokeObjectURL(url); allocated.clear(); };
    const current = () => {
      if (disposed) return false;
      if (!generationCurrent(context)) { abort.abort(); revokeAllocated(); return false; }
      return true;
    };
    if (account) void fetchInlineImages(referencedInlineParts(html, inlineParts), messageId, account, abort.signal, current, (cid, blob) => {
      if (!current()) return;
      const url = URL.createObjectURL(blob);
      allocated.add(url);
      if (!current()) { URL.revokeObjectURL(url); allocated.delete(url); return; }
      const previous = urls.get(cid);
      if (previous) { URL.revokeObjectURL(previous); allocated.delete(previous); }
      urls.set(cid, url);
      setInlineUrls(new Map(urls));
    });
    return () => {
      disposed = true;
      abort.abort();
      revokeAllocated();
    };
  }, [account, messageId, html, inlineParts, offlineGeneration()]);
  const cleaned = replaceInlineImages(cleanLinks(html), inlineUrls);
  const { html: safe, blocked } = ok ? { html: cleaned, blocked: 0 } : stripRemoteImages(cleaned);
  const themed = !emailHasOwnColors(safe);
  // Read as a signal, not off the DOM: the injected colours are literals baked
  // into srcdoc, so this component has to re-render when the theme changes.
  const themeKey = theme.value;

  useEffect(() => {
    const frame = ref.current;
    if (!frame) return;
    const fit = () => {
      try {
        const content = (frame.contentDocument?.body?.scrollHeight || 300) + 12;
        // In the Classic reader pane the message owns the pane: fill the
        // remaining height so short mail doesn't hug the top, and let longer
        // mail extend past it so the pane scrolls as one document. The 900px
        // content cap is a document-mode rule; in a pane it strands space.
        const pane = frame.closest(".reader-pane");
        if (pane) {
          const fill = pane.clientHeight - (frame.getBoundingClientRect().top - pane.getBoundingClientRect().top) - 16;
          frame.style.height = Math.max(content, fill, 200) + "px";
          return;
        }
        frame.style.height = Math.min(Math.max(content, 40), 900) + "px";
      } catch {
        frame.style.height = "300px";
      }
    };
    frame.addEventListener("load", fit);
    window.addEventListener("resize", fit);
    // Mail bodies keep growing after load — web fonts, images, lazy scripts.
    // A single load-time measurement strands the tail of the message in an
    // internally-scrolling box, which reads as "only the top shows".
    let observer: ResizeObserver | null = null;
    const observe = () => {
      const body = frame.contentDocument?.body;
      if (!body || typeof ResizeObserver === "undefined") return;
      observer?.disconnect();
      observer = new ResizeObserver(fit);
      observer.observe(body);
    };
    frame.addEventListener("load", observe);
    observe();
    return () => {
      frame.removeEventListener("load", fit);
      frame.removeEventListener("load", observe);
      window.removeEventListener("resize", fit);
      observer?.disconnect();
    };
  }, [safe, themeKey]);

  return (
    <>
      {blocked > 0 && (
        <div class="img-notice">
          <Icon name="shield" size={15} />
          <span>
            {blocked === 1 ? "1 remote image blocked" : blocked + " remote images blocked"} — loading
            them tells the sender you opened this.
          </span>
          <button class="btn btn-outline btn-sm" type="button" onClick={() => allowImages(messageId)}>
            Load once
          </button>
          {senderKey && <button class="btn btn-ghost btn-sm" type="button" onClick={() => allowSenderImages(senderKey)}>Always for sender</button>}
        </div>
      )}
      <iframe
        ref={ref}
        class="mail-frame"
        title="Message body"
        sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox"
        srcdoc={frameDoc(safe, themed, ok)}
      />
    </>
  );
}

interface Segment { quoted: boolean; text: string }

/** Groups a plain-text body into spoken and quoted runs. */
export function segmentText(text: string): Segment[] {
  const segs: Segment[] = [];
  for (const line of (text || "").split("\n")) {
    const quoted =
      /^\s*>/.test(line) ||
      /^On .+ wrote:\s*$/.test(line) ||
      /^\s*-{2,}\s*Original Message\s*-{2,}/i.test(line) ||
      /^\s*_{5,}/.test(line);
    const last = segs[segs.length - 1];
    if (last && last.quoted === quoted) last.text += "\n" + line;
    else segs.push({ quoted, text: line });
  }
  return segs;
}

function QuotedRun({ text }: { text: string }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button class="quote-toggle" type="button" onClick={() => setOpen((v) => !v)}>
        {open ? "Hide quoted text" : "Show quoted text"}
      </button>
      {open && <div class="quote">{text}</div>}
    </>
  );
}

function TextBody({ text }: { text: string }) {
  return (
    <div class="msg-text">
      {segmentText(text).map((seg, i) =>
        seg.quoted ? <QuotedRun text={seg.text} key={i} /> : <div key={i}>{seg.text}</div>
      )}
    </div>
  );
}

export function MessageBody({ html, text, messageId, sender, account, inlineParts, bodyStatus = "ready", onRetry }: {
  html?: string; text: string; messageId: string; sender: string; account?: string; inlineParts?: InlinePart[];
  bodyStatus?: "ready" | "missing" | "failed"; onRetry?: () => void;
}) {
  // body_status decides before the content does: a failed fetch offers a
  // retry, an unfetched body offers to load, and only a READY empty body is
  // an authoritative empty message. The old fallback labelled all three
  // "sync in progress" (audit 4 F19).
  if (bodyStatus === "failed") {
    return (
      <div class="msg-empty" role="alert">
        <div class="empty-big">Message content could not be fetched.</div>
        <div class="empty-sub">
          <button class="btn btn-outline btn-sm" type="button" onClick={onRetry}>Retry</button>
        </div>
      </div>
    );
  }
  if (bodyStatus === "missing") {
    return (
      <div class="msg-empty">
        <div class="empty-big">Message content has not been loaded on this device.</div>
        <div class="empty-sub">
          <button class="btn btn-outline btn-sm" type="button" onClick={onRetry}>Load message</button>
        </div>
      </div>
    );
  }
  if (html) return <HtmlBody html={html} messageId={messageId} sender={sender} account={account} inlineParts={inlineParts} />;
  if (!text) return <div class="msg-empty"><div class="empty-big">This message has an empty body.</div></div>;
  return <TextBody text={text} />;
}
