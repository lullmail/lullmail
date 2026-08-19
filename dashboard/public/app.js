// email-soft client. Static pages provide shells (#view[data-page]); this
// file renders everything against the JSON API. Design language borrowed
// deliberately from HEY: oversized subjects, color-coded senders, quiet
// chrome, black-pill actions.
(function () {
  "use strict";

  var view = document.getElementById("view");
  var overlay = document.getElementById("overlay");
  var toast = document.getElementById("toast");
  var syncNote = document.getElementById("sync-note");

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

  // ---- contact colors: a stable hue per address (HEY contact colors) ----
  function hueFor(s) {
    var h = 0;
    for (var i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) % 360;
    return h;
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
    d.style.background = "hsl(" + hue + ", 65%, 93%)";
    d.style.color = "hsl(" + hue + ", 50%, 36%)";
    return d;
  }
  function senderName(row) {
    var who = splitFrom(row.from || "");
    return who.name || who.email || "(unknown)";
  }

  var CLIP_SVG = '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21.44 11.05l-9.19 9.19a6 6 0 0 1-8.49-8.49l9.19-9.19a4 4 0 0 1 5.66 5.66l-9.2 9.19a2 2 0 0 1-2.83-2.83l8.49-8.48"/></svg>';

  // ---- empty states, in the product's voice ----
  var EMPTY = {
    imbox: ["All quiet.", "Nothing needs you right now."],
    screener: ["Nobody's waiting.", "New senders will show up here first. Screen them once — they're sorted forever."],
    feed: ["Feed's empty.", "Newsletters and periodic mail will gather here."],
    paper_trail: ["No receipts.", "Receipts, notifications and confirmations will file themselves here."],
    set_aside: ["Nothing set aside.", "Parking a thread here hides it until you want it back."],
    later: ["Nothing for later.", "Threads you defer will wait here, out of the way."]
  };
  function emptyState(key) {
    var copy = EMPTY[key] || ["Nothing here.", ""];
    var w = el("div", "empty");
    w.appendChild(el("div", "empty-big", copy[0]));
    if (copy[1]) w.appendChild(el("div", "empty-sub", copy[1]));
    return w;
  }

  // ---- nav badges ----
  function refreshCounts() {
    if (!token()) return;
    api("/counts").then(function (c) {
      document.querySelectorAll("[data-nav]").forEach(function (a) {
        var n = c[a.dataset.nav] || 0;
        var b = a.querySelector(".nav-count");
        if (n > 0) {
          if (!b) { b = el("span", "nav-count"); a.appendChild(b); }
          b.textContent = n > 99 ? "99+" : n;
        } else if (b) b.remove();
      });
    }).catch(function () {});
  }

  // ---- bucket list ----
  var selectedRow = -1, currentRows = [];
  function loadBucket(name) {
    refreshCounts();
    api("/buckets/" + name).then(function (rows) {
      currentRows = rows || [];
      selectedRow = -1;
      view.innerHTML = "";
      if (!currentRows.length) {
        view.appendChild(emptyState(name));
        return;
      }
      var list = el("div", "msg-list");
      currentRows.forEach(function (row) {
        var item = el("div", "msg-row" + (row.read ? " read" : ""));
        item.dataset.thread = row.thread_id;
        var who = splitFrom(row.from || "");
        item.appendChild(avatar(who.email, who.name));

        var main = el("div", "row-main");
        var top = el("div", "row-top");
        top.appendChild(el("span", "row-sender", who.name || who.email));
        var meta = el("span", "row-meta");
        if (row.has_attachment) {
          var chip = el("span", "chip");
          chip.innerHTML = CLIP_SVG;
          meta.appendChild(chip);
        }
        if (row.thread_len > 1) meta.appendChild(el("span", "chip", String(row.thread_len)));
        meta.appendChild(el("span", "row-date", fmtDate(row.received_at)));
        top.appendChild(meta);
        main.appendChild(top);
        main.appendChild(el("div", "row-subject", row.subject || "(no subject)"));
        if (row.preview) main.appendChild(el("div", "row-preview", row.preview));
        item.appendChild(main);

        item.addEventListener("click", function () { openThread(row.thread_id, name); });
        list.appendChild(item);
      });
      view.appendChild(list);
    }).catch(function (e) { if (e.message !== "unauthorized") showError(e.message); });
  }
  function showError(msg) {
    view.innerHTML = "";
    view.appendChild(el("div", "empty empty-big", msg));
  }

  // ---- thread overlay ----
  function openThread(threadId, bucket) {
    api("/threads/" + encodeURIComponent(threadId)).then(function (msgs) {
      overlay.innerHTML = "";
      var panel = el("div", "panel thread-panel");

      var head = el("div", "thread-head");
      head.appendChild(el("h2", "thread-subject", msgs[msgs.length - 1].subject || "(no subject)"));
      var close = el("button", "btn-ghost btn-sm", "Close");
      close.type = "button";
      close.addEventListener("click", function () { overlay.hidden = true; loadBucket(bucket); });
      head.appendChild(close);
      panel.appendChild(head);

      var stream = el("div", "thread-stream");
      msgs.forEach(function (m) {
        var who = splitFrom(m.from || "");
        var block = el("div", "thread-msg");
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
        body.textContent = m.body || "(body not fetched yet — sync in progress)";
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
            .then(function () { overlay.hidden = true; loadBucket(bucket); });
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

      overlay.appendChild(panel);
      overlay.hidden = false;

      var last = msgs[msgs.length - 1];
      api("/messages/" + encodeURIComponent(last.id) + "/action", { method: "POST", body: { action: "read" } })
        .then(refreshCounts);
    }).catch(function (e) { showToast("Thread failed: " + e.message); });
  }

  // ---- screener ----
  function loadScreener() {
    refreshCounts();
    api("/screener").then(function (rows) {
      view.innerHTML = "";
      if (!rows || !rows.length) {
        view.appendChild(emptyState("screener"));
        return;
      }
      view.appendChild(el("div", "page-kicker", "The Screener"));
      view.appendChild(el("p", "page-sub",
        "New senders wait here. Decide once — every message they ever send goes where you say."));
      rows.forEach(function (row) {
        var card = el("div", "screener-card");
        var who = splitFrom(row.sender);
        var idrow = el("div", "screener-id");
        idrow.appendChild(avatar(who.email, who.name, "lg"));
        var idmain = el("div", "screener-idmain");
        idmain.appendChild(el("div", "screener-sender", who.name === who.email ? who.email : who.name + "  ·  " + who.email));
        var sample = el("div", "screener-sample");
        sample.appendChild(el("span", "chip", row.waiting + " waiting"));
        if (row.sample_subject) sample.appendChild(el("span", "screener-subject", row.sample_subject));
        idmain.appendChild(sample);
        idrow.appendChild(idmain);
        card.appendChild(idrow);

        var btns = el("div", "screener-btns");
        var routes = [["Imbox", "imbox", "btn-primary"], ["Paper Trail", "paper_trail", "btn-outline"],
          ["Feed", "feed", "btn-outline"]];
        routes.forEach(function (r) {
          var b = el("button", r[2], r[0]);
          b.type = "button";
          b.addEventListener("click", function () {
            decide(row.sender, true, r[1]);
          });
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
      view.appendChild(el("div", "page-kicker", "Accounts"));
      view.appendChild(el("p", "page-sub", "Mailboxes this app mirrors. Credentials are sealed at rest."));
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

  // ---- backdrop + keyboard ----
  if (overlay) {
    overlay.addEventListener("click", function (ev) {
      if (ev.target === overlay) overlay.hidden = true;
    });
  }
  document.addEventListener("keydown", function (ev) {
    if (overlay && !overlay.hidden) {
      if (ev.key === "Escape") overlay.hidden = true;
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
      if (row) openThread(row.thread_id, view.dataset.bucket);
    }
  });

  // ---- router ----
  function boot() {
    if (!token() || !view) return;
    var page = view.dataset.page;
    if (page === "bucket") loadBucket(view.dataset.bucket);
    else if (page === "screener") loadScreener();
    else if (page === "accounts") loadAccounts();
    var nav = view.dataset.bucket || page;
    document.querySelectorAll("[data-nav]").forEach(function (a) {
      a.classList.toggle("active", a.dataset.nav === nav);
    });
    api("/classify", { method: "POST" }).then(refreshCounts).catch(function () {});
  }
  if (token()) boot();
})();
