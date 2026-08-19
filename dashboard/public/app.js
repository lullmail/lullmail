// email-soft client. Static-preset pages provide shells (#view with a
// data-page); everything else happens here against the JSON API.
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
    var g = document.createElement("div");
    g.className = "gate";
    g.id = "token-gate";
    g.innerHTML = '<h2>Access token</h2><p>Dev auth: enter the EMAILSOFT_TOKEN value.</p>' +
      '<input id="token-input" type="password" placeholder="token" autocomplete="off">' +
      '<button id="token-save" class="primary" type="button">Unlock</button>';
    view.appendChild(g);
    document.getElementById("token-save").addEventListener("click", function () {
      localStorage.setItem("es_token", document.getElementById("token-input").value.trim());
      boot();
    });
  }
  if (!token()) promptToken();

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
      return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
    }
    return d.toLocaleDateString([], { month: "short", day: "numeric" });
  }
  function showToast(msg, actionLabel, actionFn, ms) {
    toast.hidden = false;
    toast.innerHTML = "";
    toast.appendChild(el("span", null, msg));
    if (actionLabel) {
      var b = el("button", "primary", actionLabel);
      b.type = "button";
      b.addEventListener("click", function () { actionFn(); toast.hidden = true; });
      toast.appendChild(b);
    }
    clearTimeout(showToast._t);
    showToast._t = setTimeout(function () { toast.hidden = true; }, ms || 6000);
  }

  // ---- bucket list ----
  var selectedRow = -1, currentRows = [];
  function loadBucket(name) {
    api("/buckets/" + name).then(function (rows) {
      currentRows = rows || [];
      selectedRow = -1;
      view.innerHTML = "";
      if (!rows || !rows.length) {
        view.appendChild(el("div", "empty", "Nothing here. Quiet is good."));
        return;
      }
      var list = el("div", "msg-list");
      rows.forEach(function (row) {
        var item = el("div", "msg-row" + (row.read ? " read" : ""));
        item.dataset.thread = row.thread_id;
        item.dataset.msg = row.message_id;
        item.innerHTML =
          '<div class="msg-from"></div>' +
          '<div class="msg-mid"><div class="msg-subject"></div><div class="msg-preview"></div></div>' +
          '<div class="msg-meta"><span class="msg-date"></span>' +
          (row.has_attachment ? '<span class="attach">attach</span>' : "") +
          (row.thread_len > 1 ? '<span class="threadlen">' + row.thread_len + "</span>" : "") +
          "</div>";
        item.querySelector(".msg-from").textContent = row.from || "(unknown)";
        item.querySelector(".msg-subject").textContent = row.subject || "(no subject)";
        item.querySelector(".msg-preview").textContent = row.preview || "";
        item.querySelector(".msg-date").textContent = fmtDate(row.received_at);
        item.addEventListener("click", function () { openThread(row.thread_id, name); });
        list.appendChild(item);
      });
      view.appendChild(list);
    }).catch(function (e) { if (e.message !== "unauthorized") view.appendChild(el("div", "error", e.message)); });
  }

  // ---- thread overlay ----
  function openThread(threadId, bucket) {
    api("/threads/" + encodeURIComponent(threadId)).then(function (msgs) {
      overlay.innerHTML = "";
      var panel = el("div", "panel thread-panel");
      var close = el("button", "close", "Close");
      close.type = "button";
      close.addEventListener("click", function () { overlay.hidden = true; loadBucket(bucket); });
      var actions = el("div", "thread-actions");
      [["Set Aside", "set_aside"], ["Paper Trail", "paper_trail"], ["Feed", "feed"],
       ["Later", "later"], ["Imbox", "imbox"]].forEach(function (pair) {
        var b = el("button", null, pair[0]);
        b.type = "button";
        b.addEventListener("click", function () {
          api("/messages/" + encodeURIComponent(msgs[msgs.length - 1].id) + "/action", { method: "POST", body: { action: pair[1] } })
            .then(function () { overlay.hidden = true; loadBucket(bucket); });
        });
        actions.appendChild(b);
      });
      var reply = el("button", "primary", "Reply");
      reply.type = "button";
      reply.addEventListener("click", function () {
        var last = msgs[msgs.length - 1];
        compose(last.from.replace(/.*<(.*)>.*/, "$1"), "Re: " + (last.subject || ""), last.id);
      });
      actions.appendChild(reply);
      panel.appendChild(close);
      panel.appendChild(actions);

      msgs.forEach(function (m) {
        var d = el("div", "msg-full");
        var head = el("div", "msg-full-head");
        head.appendChild(el("span", "msg-full-from", m.from));
        head.appendChild(el("span", "msg-full-date", fmtDate(m.received_at)));
        d.appendChild(head);
        d.appendChild(el("div", "msg-full-subject", m.subject));
        var body = el("div", "msg-full-body");
        body.textContent = m.body || "(body not fetched yet — sync in progress)";
        d.appendChild(body);
        panel.appendChild(d);
      });
      overlay.appendChild(panel);
      overlay.hidden = false;

      var last = msgs[msgs.length - 1];
      api("/messages/" + encodeURIComponent(last.id) + "/action", { method: "POST", body: { action: "read" } });
    }).catch(function (e) { showToast("Thread failed: " + e.message); });
  }

  // ---- screener ----
  function loadScreener() {
    api("/screener").then(function (rows) {
      view.innerHTML = "";
      if (!rows || !rows.length) {
        view.appendChild(el("div", "empty", "No new senders waiting. Every sender you see has been screened."));
        return;
      }
      var intro = el("p", "screener-intro",
        "New senders wait here. One decision covers all their mail, past and future.");
      view.appendChild(intro);
      rows.forEach(function (row) {
        var card = el("div", "screener-card");
        var who = el("div", "screener-who");
        who.appendChild(el("div", "screener-sender", row.sender));
        who.appendChild(el("div", "screener-sample", (row.waiting + " waiting") + (row.sample_subject ? " — " + row.sample_subject : "")));
        card.appendChild(who);
        var btns = el("div", "screener-btns");
        [["Allow — Imbox", true, "imbox"], ["Allow — Feed", true, "feed"],
         ["Allow — Paper Trail", true, "paper_trail"], ["Deny", false, "blocked"]].forEach(function (opt) {
          var b = el("button", opt[1] ? null : "danger", opt[0]);
          b.type = "button";
          b.addEventListener("click", function () {
            api("/screener/decide", { method: "POST", body: { sender: row.sender, allow: opt[1], route: opt[2] } })
              .then(function () { loadScreener(); });
          });
          btns.appendChild(b);
        });
        card.appendChild(btns);
        view.appendChild(card);
      });
    }).catch(function (e) { if (e.message !== "unauthorized") view.appendChild(el("div", "error", e.message)); });
  }

  // ---- accounts ----
  function loadAccounts() {
    api("/accounts").then(function (rows) {
      view.innerHTML = "";
      var list = el("div", "accounts");
      (rows || []).forEach(function (acc) {
        var card = el("div", "account-card");
        var line1 = el("div", "account-line");
        line1.appendChild(el("strong", null, acc.address));
        line1.appendChild(el("span", "account-sub", "  " + acc.provider +
          (acc.label ? " — " + acc.label : "")));
        card.appendChild(line1);
        var line2 = el("div", "account-line account-meta");
        line2.appendChild(el("span", null,
          acc.message_count + " mirrored, " + acc.screener_count + " in screener, backfill " + acc.backfill_days + "d"));
        var status = acc.last_error ? "error: " + acc.last_error :
          (acc.last_sync_at ? "synced " + fmtDate(acc.last_sync_at) : "never synced");
        line2.appendChild(el("span", acc.last_error ? "err" : "ok", status));
        card.appendChild(line2);
        var btns = el("div", "account-btns");
        var sync = el("button", null, "Sync now");
        sync.type = "button";
        sync.addEventListener("click", function () {
          syncNote.textContent = "syncing " + acc.address + "…";
          api("/accounts/" + acc.id + "?op=sync", { method: "POST" }).then(function () {
            showToast("Sync running — refresh in a minute");
            setTimeout(loadAccounts, 4000);
          });
        });
        var del = el("button", "danger", "Disconnect");
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
        '<button class="primary" type="submit">Connect</button>' +
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
    }).catch(function (e) { if (e.message !== "unauthorized") view.appendChild(el("div", "error", e.message)); });
  }

  // ---- compose ----
  function compose(to, subject, replyToId) {
    overlay.innerHTML = "";
    var panel = el("div", "panel compose-panel");
    var form = el("form", "compose-form");
    form.innerHTML =
      '<label>To<input name="to" type="email" required></label>' +
      '<label>Subject<input name="subject" required></label>' +
      '<textarea name="text" rows="10" required></textarea>' +
      '<div class="compose-btns"><button class="primary" type="submit">Send</button>' +
      '<button type="button" class="cancel">Cancel</button></div>';
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

  // ---- keyboard: j/k navigate, enter opens ----
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
    // highlight nav
    var nav = view.dataset.bucket || page;
    document.querySelectorAll("[data-nav]").forEach(function (a) {
      a.classList.toggle("active", a.dataset.nav === nav);
    });
    api("/classify", { method: "POST" }).catch(function () {});
  }
  if (token()) boot();
})();
