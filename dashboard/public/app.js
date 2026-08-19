// email-soft client. Static pages provide shells (#view[data-page]); this
// file renders everything against the JSON API.
//
// Design language: editorial serif subjects over a quiet sans chrome, light
// and dark, split-pane reading on wide screens, keyboard-first.
(function () {
  "use strict";

  var view = document.getElementById("view");
  var overlay = document.getElementById("overlay");
  var toast = document.getElementById("toast");
  var syncNote = document.getElementById("sync-note");
  var reader = document.getElementById("reader");

  var WIDE = window.matchMedia("(min-width: 1100px)");
  function wide() { return WIDE.matches && reader; }

  // ---- theme ----
  var themeBtn = document.getElementById("theme-btn");
  function applyThemeIcons() {
    var t = document.documentElement.getAttribute("data-theme") || "light";
    if (themeBtn) {
      themeBtn.querySelector(".icon-moon").style.display = t === "dark" ? "none" : "";
      themeBtn.querySelector(".icon-sun").style.display = t === "dark" ? "" : "none";
    }
  }
  if (themeBtn) {
    applyThemeIcons();
    themeBtn.addEventListener("click", function () {
      var cur = document.documentElement.getAttribute("data-theme") === "dark" ? "light" : "dark";
      document.documentElement.setAttribute("data-theme", cur);
      try { localStorage.setItem("es-theme", cur); } catch (e) {}
      applyThemeIcons();
    });
  }

  // ---- token gate (dev auth v0) ----
  function token() { return localStorage.getItem("es_token") || ""; }
  function api(path, opts) {
    opts = opts || {};
    opts.headers = Object.assign({ "Authorization": "Bearer " + token() }, opts.headers || {});
    if (opts.body && typeof opts.body !== "string") {
      opts.headers["Content-Type"] = "application/json";
      opts.body = JSON.stringify(opts.body);
    }
    return fetch("/api" + path, opts).then(function (r) {
      if (r.status === 401) { promptToken(); throw new Error("unauthorized"); }
      if (!r.ok) return r.json().then(function (p) { throw new Error(p.detail || p.title || r.status); });
      return r.status === 204 ? null : r.json();
    });
  }
  function promptToken() {
    if (document.getElementById("token-gate")) return;
    view.innerHTML = "";
    var g = el("div", "gate");
    g.id = "token-gate";
    g.innerHTML = '<div class="gate-kicker">email-soft</div>' +
      '<h2>Who goes there</h2>' +
      '<input id="token-input" type="password" placeholder="access token" autocomplete="off">' +
      '<button id="token-save" class="btn-primary" type="button">Let me in</button>';
    view.appendChild(g);
    document.getElementById("token-save").addEventListener("click", function () {
      localStorage.setItem("es_token", document.getElementById("token-input").value.trim());
      boot();
    });
  }

  // ---- helpers ----
  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }
  function fmtDate(iso) {
    if (!iso) return "";
    var d = new Date(iso), now = new Date();
    if (d.toDateString() === now.toDateString()) {
      return d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
    }
    return d.toLocaleDateString([], { month: "short", day: "numeric" });
  }
  function pop(node) {
    node.classList.remove("pop");
    void node.offsetWidth; // reflow so the animation restarts
    node.classList.add("pop");
  }
  function showToast(msg, actionLabel, actionFn, ms) {
    toast.hidden = false;
    toast.innerHTML = "";
    toast.appendChild(el("span", null, msg));
    if (actionLabel) {
      var b = el("button", "btn-primary btn-sm", actionLabel);
      b.type = "button";
      b.addEventListener("click", function () { actionFn(); toast.hidden = true; });
      toast.appendChild(b);
    }
    clearTimeout(showToast._t);
    showToast._t = setTimeout(function () { toast.hidden = true; }, ms || 6000);
  }

  // ---- contact colors: curated palette, stable per address ----
  var HUES = [4, 24, 42, 88, 152, 172, 194, 218, 246, 276, 310, 340];
  function hueFor(s) {
    var h = 0;
    for (var i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) >>> 0;
    return HUES[h % HUES.length];
  }
  function splitFrom(str) {
    var m = /^([^<]*)<(.+)>$/.exec(str || "");
    if (m) return { name: m[1].trim(), email: m[2].trim() };
    var s = (str || "").trim();
    return { name: s, email: s };
  }
  function avatar(email, name, size) {
    var hue = hueFor(email || "?");
    var d = el("div", "avatar" + (size === "lg" ? " avatar-lg" : size === "sm" ? " avatar-sm" : ""));
    d.textContent = (name || email || "?").charAt(0).toUpperCase() || "?";
    // Saturated fill, white initial — bold contact color is a HEY signature.
    d.style.background = "hsl(" + hue + ", 62%, 47%)";
    d.style.color = "#fff";
    return d;
  }

  var CLIP_SVG = '<svg viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21.44 11.05l-9.19 9.19a6 6 0 0 1-8.49-8.49l9.19-9.19a4 4 0 0 1 5.66 5.66l-9.2 9.19a2 2 0 0 1-2.83-2.83l8.49-8.48"/></svg>';

  // ---- empty states, in the product's voice ----
  var EMPTY = {
    imbox: ["All quiet.", "Nothing needs you right now. Enjoy it."],
    screener: ["Nobody's waiting.", "New senders land here first. Screen them once — they're sorted forever."],
    feed: ["Feed's empty.", "Newsletters and periodic mail will gather here."],
    paper_trail: ["No receipts.", "Receipts, notifications and confirmations file themselves here."],
    set_aside: ["Nothing set aside.", "Park a thread here and it stays out of sight until you want it."],
    later: ["Nothing for later.", "Threads you defer wait here, out of the way."]
  };
  function emptyState(key) {
    var copy = EMPTY[key] || ["Nothing here.", ""];
    var w = el("div", "empty");
    w.appendChild(el("div", "empty-big", copy[0]));
    if (copy[1]) w.appendChild(el("div", "empty-sub", copy[1]));
    return w;
  }
  function showError(msg) {
    view.innerHTML = "";
    var w = el("div", "empty");
    w.appendChild(el("div", "empty-big", msg));
    view.appendChild(w);
  }

  function bucketHead(title, sub) {
    var h = el("div", "bucket-head");
    h.appendChild(el("h1", "bucket-title", title));
    if (sub) h.appendChild(el("div", "bucket-sub", sub));
    return h;
  }

  // ---- nav badges + tab badge ----
  function refreshCounts() {
    if (!token()) return;
    api("/counts").then(function (c) {
      var total = 0;
      document.querySelectorAll("[data-nav]").forEach(function (a) {
        var n = c[a.dataset.nav] || 0;
        if (a.dataset.nav !== "screener" && a.dataset.nav !== "set_aside") total += n;
        var b = a.querySelector(".nav-count");
        if (n > 0) {
          if (!b) { b = el("span", "nav-count"); a.appendChild(b); b.textContent = n; }
          else if (b.textContent !== String(n)) { b.textContent = n; }
          pop(b);
        } else if (b) b.remove();
      });
      updateTabBadge(total);
    }).catch(function () {});
  }
  var faviconLink = null;
  function updateTabBadge(n) {
    document.title = n > 0 ? "(" + n + ") email-soft" : "email-soft";
    if (!faviconLink) {
      faviconLink = document.createElement("link");
      faviconLink.rel = "icon";
      document.head.appendChild(faviconLink);
    }
    var bg = document.documentElement.getAttribute("data-theme") === "dark" ? "%23ecedf1" : "%2317181c";
    var fg = document.documentElement.getAttribute("data-theme") === "dark" ? "%23101114" : "%23ffffff";
    var svg = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">' +
      '<rect width="64" height="64" rx="14" fill="' + bg + '"/>' +
      (n > 0
        ? '<text x="32" y="42" font-family="system-ui,sans-serif" font-size="' + (n > 99 ? 26 : 34) +
          '" font-weight="700" text-anchor="middle" fill="' + fg + '">' + (n > 99 ? "99" : n) + "</text>"
        : '<text x="32" y="42" font-family="Georgia,serif" font-size="34" font-weight="700" text-anchor="middle" fill="' + fg + '">\u2709</text>') +
      "</svg>";
    faviconLink.href = "data:image/svg+xml," + svg.replace(/#/g, "%23").replace(/"/g, "'");
  }

  // ---- quick actions on hover (workflow without opening) ----
  var ACT_ICONS = {
    set_aside: '<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 3"/></svg>',
    paper_trail: '<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 2h9l5 5v15H6z"/><path d="M15 2v5h5"/><path d="M9 13h6M9 17h6"/></svg>',
    feed: '<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 11a9 9 0 0 1 9 9M4 4a16 16 0 0 1 16 16"/><circle cx="5" cy="19" r="1.5" fill="currentColor"/></svg>'
  };
  var ACT_TITLES = { set_aside: "Set Aside", paper_trail: "Paper Trail", feed: "Feed" };
  function quickAction(msgId, action, rowEl) {
    rowEl.classList.add("bye");
    setTimeout(function () {
      api("/messages/" + encodeURIComponent(msgId) + "/action", { method: "POST", body: { action: action } })
        .then(function () { reloadCurrent(); });
    }, 180);
  }
  function addHoverActions(item, row) {
    var actions = el("div", "row-actions");
    [["set_aside", "s"], ["paper_trail", "p"], ["feed", "f"]].forEach(function (pair) {
      var b = el("button", "row-act");
      b.type = "button";
      b.title = ACT_TITLES[pair[0]];
      b.innerHTML = ACT_ICONS[pair[0]];
      b.addEventListener("click", function (ev) {
        ev.stopPropagation();
        quickAction(row.message_id, pair[0], item);
      });
      actions.appendChild(b);
    });
    item.appendChild(actions);
  }
  // ---- date grouping ----
  function dayLabel(iso) {
    if (!iso) return "Earlier";
    var d = new Date(iso), now = new Date();
    var today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
    var that = new Date(d.getFullYear(), d.getMonth(), d.getDate());
    var days = Math.round((today - that) / 86400000);
    if (days <= 0) return "Today";
    if (days === 1) return "Yesterday";
    if (days < 7) return "This week";
    if (days < 31) return "This month";
    return "Earlier";
  }

  // ---- search ----
  var searchInput = document.getElementById("search");
  var searchTimer = null, searchQ = "";
  if (searchInput) {
    searchInput.addEventListener("input", function () {
      clearTimeout(searchTimer);
      var q = searchInput.value.trim();
      searchTimer = setTimeout(function () {
        if (q) { searchQ = q; loadSearch(q); }
        else if (searchQ) {
          searchQ = "";
          var p = view.dataset.page;
          if (p === "bucket") loadBucket(view.dataset.bucket);
          else if (p === "screener") loadScreener();
          else if (p === "accounts") loadAccounts();
        }
      }, 220);
    });
  }
  function loadSearch(q) {
    refreshCounts();
    api("/search?q=" + encodeURIComponent(q)).then(function (rows) {
      currentRows = rows || [];
      selectedRow = -1;
      view.innerHTML = "";
      var head = bucketHead("Search", currentRows.length
        ? currentRows.length + (currentRows.length === 1 ? " result" : " results") + " for \u201C" + q + "\u201D"
        : "");
      view.appendChild(head);
      if (!currentRows.length) {
        var w = el("div", "empty");
        w.appendChild(el("div", "empty-big", "Nothing matched."));
        w.appendChild(el("div", "empty-sub", "Try a sender, a subject, or a word from a preview."));
        view.appendChild(w);
        return;
      }
      var list = el("div", "msg-list");
      currentRows.forEach(function (row, i) {
        var item = buildRow(row, i, q);
        list.appendChild(item);
      });
      view.appendChild(list);
      if (wide()) clearReader();
    }).catch(function (e) { if (e.message !== "unauthorized") showError(e.message); });
  }
  function markHit(text, q) {
    var span = el("span", null);
    var lower = text.toLowerCase(), needle = q.toLowerCase(), idx = lower.indexOf(needle);
    if (idx < 0 || !q) { span.textContent = text; return span; }
    span.appendChild(document.createTextNode(text.slice(0, idx)));
    var mark = el("mark", "hit", text.slice(idx, idx + needle.length));
    span.appendChild(mark);
    span.appendChild(document.createTextNode(text.slice(idx + needle.length)));
    return span;
  }

  // ---- row builder shared by buckets and search ----
  function buildRow(row, i, q) {
    var item = el("div", "msg-row row-in" + (row.read ? " read" : ""));
    item.style.animationDelay = Math.min(i, 12) * 22 + "ms";
    item.dataset.thread = row.thread_id;
    var who = splitFrom(row.from || "");
    item.appendChild(avatar(who.email, who.name));

    var main = el("div", "row-main");
    var top = el("div", "row-top");
    top.appendChild(el("span", "row-sender", who.name || who.email));
    var meta2 = el("span", "row-meta");
    if (row.has_attachment) {
      var chip = el("span", "chip");
      chip.innerHTML = CLIP_SVG;
      meta2.appendChild(chip);
    }
    if (row.thread_len > 1) meta2.appendChild(el("span", "chip", String(row.thread_len)));
    meta2.appendChild(el("span", "row-date", fmtDate(row.received_at)));
    top.appendChild(meta2);
    main.appendChild(top);
    var subj = el("div", "row-subject");
    if (q) subj.appendChild(markHit(row.subject || "(no subject)", q));
    else subj.textContent = row.subject || "(no subject)";
    main.appendChild(subj);
    if (row.preview) main.appendChild(el("div", "row-preview", row.preview));
    item.appendChild(main);
    addHoverActions(item, row);

    item.addEventListener("click", function () {
      openThread(row.thread_id, searchQ ? null : view.dataset.bucket, item);
    });
    return item;
  }

  // ---- bucket list ----
  var selectedRow = -1, currentRows = [];
  var BUCKET_TITLES = {
    imbox: ["Imbox", "The people you chose to hear from."],
    feed: ["Feed", "Periodic mail worth scanning, not answering."],
    paper_trail: ["Paper Trail", "Receipts, notifications, confirmations."],
    set_aside: ["Set Aside", "Parked. It comes back when you say."],
    later: ["Later", "Deferred, not deleted."]
  };
  function loadBucket(name) {
    refreshCounts();
    api("/buckets/" + name).then(function (rows) {
      currentRows = rows || [];
      selectedRow = -1;
      view.innerHTML = "";
      var meta = BUCKET_TITLES[name] || [name, ""];
      view.appendChild(bucketHead(meta[0], currentRows.length
        ? meta[1] : ""));

      if (!currentRows.length) {
        view.appendChild(emptyState(name));
        return;
      }
      var list = el("div", "msg-list");
      var lastLabel = "";
      currentRows.forEach(function (row, i) {
        var label = dayLabel(row.received_at);
        if (label !== lastLabel) {
          lastLabel = label;
          var rule = el("div", "date-rule");
          rule.appendChild(el("span", null, label));
          list.appendChild(rule);
        }
        list.appendChild(buildRow(row, i));
      });
      view.appendChild(list);
      if (wide()) clearReader();
    }).catch(function (e) { if (e.message !== "unauthorized") showError(e.message); });
  }

  function reloadCurrent() {
    if (searchQ) { loadSearch(searchQ); return; }
    var p = view.dataset.page;
    if (p === "bucket") loadBucket(view.dataset.bucket);
    else if (p === "screener") loadScreener();
    else if (p === "accounts") loadAccounts();
  }

  // ---- thread rendering (shared by overlay + reader pane) ----
  function renderThread(panel, msgs, onClose, bucket) {
    var head = el("div", "thread-head");
    head.appendChild(el("h2", "thread-subject", msgs[msgs.length - 1].subject || "(no subject)"));
    var close = el("button", "btn-ghost btn-sm", "Close");
    close.type = "button";
    close.addEventListener("click", onClose);
    head.appendChild(close);
    panel.appendChild(head);

    var stream = el("div", "thread-stream");
    msgs.forEach(function (m, i) {
      var who = splitFrom(m.from || "");
      var block = el("div", "thread-msg row-in");
      block.style.animationDelay = Math.min(i, 8) * 30 + "ms";
      var mh = el("div", "thread-msg-head");
      var idrow = el("div", "thread-msg-id");
      idrow.appendChild(avatar(who.email, who.name, "sm"));
      var names = el("div", "thread-msg-names");
      names.appendChild(el("span", "thread-msg-from", who.name || who.email));
      names.appendChild(el("span", "thread-msg-to", "to " + (m.to || "you")));
      idrow.appendChild(names);
      mh.appendChild(idrow);
      mh.appendChild(el("span", "thread-msg-date", fmtDate(m.received_at)));
      block.appendChild(mh);
      var body = el("div", "thread-msg-body");
      if (m.html) {
        body.appendChild(htmlFrame(m.html));
      } else {
        body.appendChild(textBody(m.body || "(body not fetched yet — sync in progress)"));
      }
      block.appendChild(body);
      stream.appendChild(block);
    });
    panel.appendChild(stream);

    var actions = el("div", "thread-actions");
    [["Set Aside", "set_aside"], ["Paper Trail", "paper_trail"], ["Feed", "feed"],
     ["Later", "later"], ["Imbox", "imbox"]].forEach(function (pair) {
      var b = el("button", "btn-ghost btn-sm", pair[0]);
      b.type = "button";
      b.addEventListener("click", function () {
        api("/messages/" + encodeURIComponent(msgs[msgs.length - 1].id) + "/action",
          { method: "POST", body: { action: pair[1] } })
          .then(function () { onClose(); reloadCurrent(); });
      });
      actions.appendChild(b);
    });
    var reply = el("button", "btn-primary btn-sm", "Reply");
    reply.type = "button";
    reply.addEventListener("click", function () {
      var last = msgs[msgs.length - 1];
      compose(splitFrom(last.from || "").email, "Re: " + (last.subject || ""), last.id);
    });
    actions.appendChild(reply);
    panel.appendChild(actions);
  }

  // ---- message body rendering: HTML in a sandboxed, theme-injected iframe;
  // plain text with quoted replies collapsed behind a toggle ----
  function cssVar(name) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  }
  function htmlFrame(html) {
    var frame = document.createElement("iframe");
    frame.className = "mail-frame";
    frame.setAttribute("sandbox", "allow-same-origin allow-popups allow-popups-to-escape-sandbox");
    var doc = "<!doctype html><html><head><meta charset='utf-8'>" +
      "<base target='_blank'>" +
      "<style>" +
      "html,body{margin:0;padding:0 6px;background:transparent;}" +
      "body{font-family:" + cssVar("--sans") + ";color:" + cssVar("--ink") + ";" +
      "font-size:15px;line-height:1.6;padding:4px 0 8px;word-wrap:break-word;}" +
      "img{max-width:100%;height:auto;}" +
      "a{color:#4a72d8;}" +
      "blockquote{border-left:3px solid " + cssVar("--line-strong") + ";" +
      "margin:8px 0;padding:2px 12px;color:" + cssVar("--ink-2") + ";}" +
      "pre{white-space:pre-wrap;}" +
      "table{max-width:100%;}" +
      "</style></head><body>" + html + "</body></html>";
    frame.srcdoc = doc;
    frame.addEventListener("load", function () {
      try {
        var h = frame.contentDocument.body.scrollHeight;
        frame.style.height = Math.min(Math.max(h + 12, 40), 600) + "px";
      } catch (e) { frame.style.height = "300px"; }
    });
    return frame;
  }

  function textBody(text) {
    var wrap = el("div", "msg-text");
    var lines = (text || "").split("\n");
    var segs = [];
    var cur = null;
    lines.forEach(function (line) {
      var isQuote = /^\s*>/.test(line) || /^On .+ wrote:\s*$/.test(line) ||
        /^\s*-----Original Message-----/.test(line) || /^\s*_{5,}/.test(line);
      if (isQuote && cur !== "q") { cur = "q"; segs.push({ t: "q", lines: [line] }); }
      else if (!isQuote && cur !== "t") { cur = "t"; segs.push({ t: "t", lines: [line] }); }
      else segs[segs.length - 1].lines.push(line);
    });
    segs.forEach(function (seg) {
      if (seg.t === "t") {
        var p = el("div", null);
        p.textContent = seg.lines.join("\n");
        wrap.appendChild(p);
      } else {
        var q = el("div", "quote");
        q.textContent = seg.lines.join("\n");
        var btn = el("button", "quote-toggle", "Show quoted text");
        btn.type = "button";
        btn.addEventListener("click", function () {
          var open = !q.hidden;
          q.hidden = open;
          btn.textContent = open ? "Show quoted text" : "Hide quoted text";
        });
        wrap.appendChild(btn);
        wrap.appendChild(q);
        q.hidden = true;
      }
    });
    return wrap;
  }

  function clearReader() {
    if (!reader) return;
    reader.innerHTML = "";
    var empty = el("div", "reader-empty");
    empty.appendChild(el("div", "reader-empty-mark", "\u2709"));
    empty.appendChild(el("div", "reader-empty-big", "Pick a thread"));
    empty.appendChild(el("div", "reader-empty-sub", "It opens here, beside the list."));
    reader.appendChild(empty);
  }

  function openThread(threadId, bucket, rowEl) {
    api("/threads/" + encodeURIComponent(threadId)).then(function (msgs) {
      var last = msgs[msgs.length - 1];

      if (wide()) {
        document.querySelectorAll(".msg-row.sel").forEach(function (r) { r.classList.remove("sel"); });
        if (rowEl) rowEl.classList.add("sel");
        reader.innerHTML = "";
        var panel = el("div", "panel reader-panel");
        renderThread(panel, msgs, function () { clearReader(); }, bucket);
        reader.appendChild(panel);
      } else {
        overlay.innerHTML = "";
        var panel2 = el("div", "panel thread-panel");
        renderThread(panel2, msgs, function () { overlay.hidden = true; reloadCurrent(); }, bucket);
        overlay.appendChild(panel2);
        overlay.hidden = false;
      }

      api("/messages/" + encodeURIComponent(last.id) + "/action", { method: "POST", body: { action: "read" } })
        .then(refreshCounts);
    }).catch(function (e) { showToast("Thread failed: " + e.message); });
  }

  // ---- screener ----
  function loadScreener() {
    refreshCounts();
    api("/screener").then(function (rows) {
      view.innerHTML = "";
      view.appendChild(bucketHead("The Screener",
        "New senders wait here. Decide once — every message they ever send goes where you say."));
      if (!rows || !rows.length) {
        view.appendChild(emptyState("screener"));
        return;
      }
      rows.forEach(function (row, i) {
        var card = el("div", "screener-card row-in");
        card.style.animationDelay = Math.min(i, 10) * 26 + "ms";
        var who = splitFrom(row.sender);
        var idrow = el("div", "screener-id");
        idrow.appendChild(avatar(who.email, who.name, "lg"));
        var idmain = el("div", "screener-idmain");
        idmain.appendChild(el("div", "screener-sender",
          who.name === who.email ? who.email : who.name + "  ·  " + who.email));
        var sample = el("div", "screener-sample");
        sample.appendChild(el("span", "chip", row.waiting + " waiting"));
        if (row.sample_subject) sample.appendChild(el("span", "screener-subject", row.sample_subject));
        idmain.appendChild(sample);
        idrow.appendChild(idmain);
        card.appendChild(idrow);

        var btns = el("div", "screener-btns");
        [["Imbox", "imbox", "btn-primary"], ["Paper Trail", "paper_trail", "btn-outline"],
         ["Feed", "feed", "btn-outline"]].forEach(function (r) {
          var b = el("button", r[2], r[0]);
          b.type = "button";
          b.addEventListener("click", function () { decide(row.sender, true, r[1]); });
          btns.appendChild(b);
        });
        var block = el("button", "btn-danger-ghost", "Block");
        block.type = "button";
        block.addEventListener("click", function () { decide(row.sender, false, "blocked"); });
        btns.appendChild(block);
        card.appendChild(btns);
        view.appendChild(card);
      });
    }).catch(function (e) { if (e.message !== "unauthorized") showError(e.message); });

    function decide(sender, allow, route) {
      api("/screener/decide", { method: "POST", body: { sender: sender, allow: allow, route: route } })
        .then(loadScreener);
    }
  }

  // ---- accounts ----
  function loadAccounts() {
    api("/accounts").then(function (rows) {
      view.innerHTML = "";
      view.appendChild(bucketHead("Accounts", "Mailboxes this app mirrors. Credentials are sealed at rest."));
      var list = el("div", "accounts");
      (rows || []).forEach(function (acc) {
        var card = el("div", "account-card");
        var line1 = el("div", "account-line");
        line1.appendChild(el("strong", null, acc.address));
        if (acc.label) line1.appendChild(el("span", "account-sub", acc.label));
        card.appendChild(line1);
        var line2 = el("div", "account-line account-meta");
        var dot = el("span", "dot " + (acc.last_error ? "dot-red" : "dot-green"));
        line2.appendChild(dot);
        line2.appendChild(el("span", null,
          acc.last_error ? acc.last_error :
          (acc.last_sync_at ? "synced " + fmtDate(acc.last_sync_at) : "connecting…") +
          " — " + acc.message_count + " mirrored, " + acc.screener_count + " to screen"));
        card.appendChild(line2);
        var btns = el("div", "account-btns");
        var sync = el("button", "btn-outline btn-sm", "Sync now");
        sync.type = "button";
        sync.addEventListener("click", function () {
          syncNote.textContent = "syncing " + acc.address + "…";
          api("/accounts/" + acc.id + "?op=sync", { method: "POST" }).then(function () {
            showToast("Sync running — refresh in a minute");
            setTimeout(loadAccounts, 4000);
          });
        });
        var del = el("button", "btn-danger-ghost btn-sm", "Disconnect");
        del.type = "button";
        del.addEventListener("click", function () {
          if (!confirm("Disconnect " + acc.address + " and delete its mirror?")) return;
          api("/accounts/" + acc.id, { method: "DELETE" }).then(loadAccounts);
        });
        btns.appendChild(sync); btns.appendChild(del);
        card.appendChild(btns);
        list.appendChild(card);
      });
      view.appendChild(list);

      var form = el("form", "account-form");
      form.innerHTML =
        "<h3>Connect a mailbox</h3>" +
        '<div class="form-grid">' +
        '<label>Provider<select name="provider"><option value="imap">IMAP</option><option value="jmap">JMAP</option></select></label>' +
        '<label>Address<input name="address" type="email" required placeholder="you@example.com"></label>' +
        '<label>Username<input name="username" placeholder="defaults to address"></label>' +
        '<label>App password<input name="password" type="password" required></label>' +
        '<label>Host<input name="host" required placeholder="imap.example.com"></label>' +
        '<label>Port<input name="port" type="number" placeholder="993"></label>' +
        '<label>SMTP host<input name="smtp_host" placeholder="defaults to host"></label>' +
        '<label>SMTP port<input name="smtp_port" type="number" placeholder="587"></label>' +
        '<label>Backfill days<input name="backfill_days" type="number" value="90"></label>' +
        "</div>" +
        '<button class="btn-primary" type="submit">Connect</button>' +
        '<p class="form-note">The password is encrypted (AES-256-GCM) before storage and validated by dialing the server first.</p>';
      form.addEventListener("submit", function (ev) {
        ev.preventDefault();
        var f = new FormData(form), body = {};
        f.forEach(function (v, k) { body[k] = v; });
        ["port", "smtp_port", "backfill_days"].forEach(function (k) { if (body[k] === "") delete body[k]; else body[k] = parseInt(body[k], 10); });
        if (body.username === "") delete body.username;
        form.querySelector("button").disabled = true;
        api("/accounts", { method: "POST", body: body }).then(function (res) {
          showToast("Connected — " + res.mailboxes + " mailboxes found, initial sync running");
          loadAccounts();
        }).catch(function (e) {
          showToast("Connect failed: " + e.message, null, null, 8000);
          form.querySelector("button").disabled = false;
        });
      });
      view.appendChild(form);
    }).catch(function (e) { if (e.message !== "unauthorized") showError(e.message); });
  }

  // ---- compose ----
  function compose(to, subject, replyToId) {
    overlay.innerHTML = "";
    var panel = el("div", "panel compose-panel");
    var form = el("form", "compose-form");
    form.innerHTML =
      '<div class="compose-kicker">New message</div>' +
      '<input name="to" type="email" required class="compose-to" placeholder="To">' +
      '<input name="subject" required class="compose-subject" placeholder="Subject">' +
      '<textarea name="text" required class="compose-body" placeholder="Write something worth reading."></textarea>' +
      '<div class="compose-btns"><button type="button" class="btn-ghost cancel">Discard</button>' +
      '<button class="btn-primary" type="submit">Send</button></div>';
    form.to.value = to || "";
    form.subject.value = subject || "";
    if (replyToId) form.dataset.reply = replyToId;
    form.addEventListener("submit", function (ev) {
      ev.preventDefault();
      var body = { to: form.to.value, subject: form.subject.value, text: form.text.value };
      if (form.dataset.reply) body.reply_to_message_id = form.dataset.reply;
      api("/send", { method: "POST", body: body }).then(function (res) {
        overlay.hidden = true;
        showToast("Sending in 5s", "Undo", function () {
          api("/outbox/" + res.queued, { method: "DELETE" })
            .then(function () { showToast("Send cancelled"); })
            .catch(function () { showToast("Too late to cancel"); });
        }, 5500);
      }).catch(function (e) { showToast("Send failed: " + e.message, null, null, 8000); });
    });
    form.querySelector(".cancel").addEventListener("click", function () { overlay.hidden = true; });
    panel.appendChild(form);
    overlay.appendChild(panel);
    overlay.hidden = false;
    form.text.focus();
  }
  document.getElementById("compose-btn").addEventListener("click", function () { compose(); });

  // ---- shortcuts palette ----
  function shortcutsPanel() {
    overlay.innerHTML = "";
    var panel = el("div", "panel shortcuts-panel");
    panel.appendChild(el("h2", "shortcuts-title", "Shortcuts"));
    var rows = [
      ["j / k", "Move down / up"],
      ["Enter", "Open selected thread"],
      ["c", "Compose"],
      ["/", "Search"],
      ["Esc", "Close panel / clear selection"],
      ["?", "This list"]
    ];
    var grid = el("div", "shortcut-grid");
    rows.forEach(function (r) {
      var k = el("span", "kbd", r[0]);
      var d = el("span", "shortcut-desc", r[1]);
      grid.appendChild(k); grid.appendChild(d);
    });
    panel.appendChild(grid);
    overlay.appendChild(panel);
    overlay.hidden = false;
  }
  document.getElementById("shortcuts-btn").addEventListener("click", shortcutsPanel);

  // ---- backdrop + keyboard ----
  if (overlay) {
    overlay.addEventListener("click", function (ev) {
      if (ev.target === overlay) overlay.hidden = true;
    });
  }
  document.addEventListener("keydown", function (ev) {
    var typing = ev.target && ev.target.closest && ev.target.closest("input, textarea, select");
    if (overlay && !overlay.hidden) {
      if (ev.key === "Escape") overlay.hidden = true;
      return;
    }
    if (typing) return;
    if (ev.key === "/") { ev.preventDefault(); if (searchInput) searchInput.focus(); return; }
    if (ev.key === "c") { ev.preventDefault(); compose(); return; }
    if (ev.key === "?") { ev.preventDefault(); shortcutsPanel(); return; }
    if (ev.key === "Escape" && wide()) {
      document.querySelectorAll(".msg-row.sel").forEach(function (r) { r.classList.remove("sel"); });
      clearReader();
      return;
    }
    var page = view && view.dataset.page;
    if (page !== "bucket" || !currentRows.length) return;
    if (ev.key === "j" || ev.key === "k") {
      ev.preventDefault();
      var rows = Array.prototype.slice.call(document.querySelectorAll(".msg-row"));
      if (!rows.length) return;
      if (rows[selectedRow]) rows[selectedRow].classList.remove("sel");
      selectedRow = ev.key === "j"
        ? Math.min(selectedRow + 1, rows.length - 1)
        : Math.max(selectedRow - 1, 0);
      rows[selectedRow].classList.add("sel");
      rows[selectedRow].scrollIntoView({ block: "nearest" });
    } else if (ev.key === "Enter" && selectedRow >= 0) {
      var row = currentRows[selectedRow];
      var rowEl = document.querySelectorAll(".msg-row")[selectedRow];
      if (row) openThread(row.thread_id, searchQ ? null : view.dataset.bucket, rowEl);
    }
  });

  // ---- router ----
  function boot() {
    if (!token() || !view) return;
    applyThemeIcons();
    var page = view.dataset.page;
    if (page === "bucket") loadBucket(view.dataset.bucket);
    else if (page === "screener") loadScreener();
    else if (page === "accounts") loadAccounts();
    var nav = view.dataset.bucket || page;
    document.querySelectorAll("[data-nav]").forEach(function (a) {
      a.classList.toggle("active", a.dataset.nav === nav);
    });
    api("/classify", { method: "POST" }).then(refreshCounts).catch(function () {});
    setInterval(refreshCounts, 45000);
  }
  if (token()) boot();
})();
