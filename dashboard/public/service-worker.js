const VERSION = "lull-shell-v1";
const SHELL = [
  "/", "/today", "/board", "/calendar", "/notes", "/people",
  "/reading", "/receipts", "/screener", "/snoozed", "/settings/accounts", "/settings/security",
  "/styles.css", "/manifest.webmanifest", "/icon.svg", "/icon-180.png",
  "/icon-192.png", "/icon-512.png"
];

self.addEventListener("install", (event) => {
  event.waitUntil((async () => {
    const cache = await caches.open(VERSION);
    await Promise.all(SHELL.map(async (url) => {
      try {
        const response = await fetch(url, { cache: "reload" });
        if (response.ok) await cache.put(url, response);
      } catch (_) {
        // One optional route must not prevent the whole shell installing.
      }
    }));

    // Neutron fingerprints the hydrated client bundle. Discover the current
    // names from the built page so a new build needs no handwritten asset list.
    try {
      const page = await fetch("/", { cache: "reload" });
      const html = await page.text();
      const assets = [...html.matchAll(/(?:src|href)=["'](\/assets\/[^"']+)["']/g)]
        .map((match) => match[1]);
      await Promise.all([...new Set(assets)].map(async (url) => {
        const response = await fetch(url, { cache: "reload" });
        if (response.ok) await cache.put(url, response);
      }));
    } catch (_) {
      // Runtime caching fills this on the next successful visit.
    }
    await self.skipWaiting();
  })());
});

self.addEventListener("activate", (event) => {
  event.waitUntil((async () => {
    const keys = await caches.keys();
    // "email-soft-shell-" is the pre-rename prefix; purge those legacy
    // caches too so they do not linger forever.
    await Promise.all(keys.filter((key) => (key.startsWith("lull-shell-") || key.startsWith("email-soft-shell-")) && key !== VERSION)
      .map((key) => caches.delete(key)));
    await self.clients.claim();
  })());
});

self.addEventListener("fetch", (event) => {
  const request = event.request;
  const url = new URL(request.url);
  if (request.method !== "GET" || url.origin !== self.location.origin || url.pathname.startsWith("/api/")) {
    return;
  }

  if (request.mode === "navigate") {
    event.respondWith((async () => {
      try {
        const fresh = await fetch(request);
        if (fresh.ok) {
          const cache = await caches.open(VERSION);
          await cache.put(url.pathname, fresh.clone());
        }
        return fresh;
      } catch (_) {
        const cache = await caches.open(VERSION);
        return (await cache.match(url.pathname)) || (await cache.match("/")) || Response.error();
      }
    })());
    return;
  }

  if (url.pathname === "/styles.css") {
    event.respondWith((async () => {
      try {
        const fresh = await fetch(request, { cache: "reload" });
        if (fresh.ok) {
          const cache = await caches.open(VERSION);
          await cache.put(request, fresh.clone());
        }
        return fresh;
      } catch (_) {
        return (await caches.match(request)) || (await caches.match("/styles.css")) || Response.error();
      }
    })());
    return;
  }

  event.respondWith((async () => {
    const cached = await caches.match(request);
    if (cached) return cached;
    const fresh = await fetch(request);
    if (fresh.ok) {
      const cache = await caches.open(VERSION);
      await cache.put(request, fresh.clone());
    }
    return fresh;
  })());
});

self.addEventListener("push", (event) => {
  let data = { title: "New mail", body: "Open Lull Mail to read it.", path: "/today" };
  try { data = { ...data, ...(event.data ? event.data.json() : {}) }; } catch { /* generic notification */ }
  event.waitUntil(self.registration.showNotification(data.title, {
    body: data.body, icon: "/icon-192.png", badge: "/icon-192.png",
    tag: "lull-new-mail", renotify: true, data: { path: data.path || "/today" },
  }));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const path = event.notification.data?.path || "/today";
  event.waitUntil(clients.matchAll({ type: "window", includeUncontrolled: true }).then((windows) => {
    for (const client of windows) {
      if ("focus" in client) { client.navigate(path); return client.focus(); }
    }
    return clients.openWindow(path);
  }));
});
