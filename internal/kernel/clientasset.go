package kernel

// client_js.go embeds the ~15KB reactive-layer client (ADR-11 §3): five duties and
// nothing more — (a) hold the SSE connection and reconnect with the Last-Event-ID
// cursor; (b) apply patch ops by data-slot (morph text/attr/value, splice keyed
// lists) preserving focus/selection/scroll; (c) capture events on interactive
// primitives and POST them; (d) maintain the slot-value map + an incremental
// FNV-1a-64 snapshot digest (mirroring internal/ui byte-for-byte) and POST /resync
// on a frame-hash mismatch; (e) nothing else. No framework, no build step.

const clientJS = `"use strict";
(function () {
  var root = document.getElementById("rg-root");
  if (!root) return;
  var sessionId = root.getAttribute("data-session");
  var eventSeq = parseInt(root.getAttribute("data-seq") || "0", 10);

  // ---- (d) slot-value map + incremental FNV-1a-64 digest (mirror internal/ui) ----
  var MASK = (1n << 64n) - 1n;
  var FNV_OFFSET = 14695981039346656037n;
  var FNV_PRIME = 1099511628211n;
  var slots = Object.create(null); // slotId -> display value
  var digest = 0n;

  function enc(s) { return new TextEncoder().encode(s); }
  function slotTerm(id, val) {
    var h = FNV_OFFSET;
    var b = enc(id);
    for (var i = 0; i < b.length; i++) { h ^= BigInt(b[i]); h = (h * FNV_PRIME) & MASK; }
    h ^= 0n; h = (h * FNV_PRIME) & MASK; // the 0x00 separator
    var v = enc(val);
    for (var j = 0; j < v.length; j++) { h ^= BigInt(v[j]); h = (h * FNV_PRIME) & MASK; }
    return h;
  }
  function setSlot(id, val) {
    var old = slots[id];
    if (old !== undefined) digest = (digest - slotTerm(id, old)) & MASK;
    slots[id] = val;
    digest = (digest + slotTerm(id, val)) & MASK;
  }
  function removeSlot(id) {
    var old = slots[id];
    if (old !== undefined) { digest = (digest - slotTerm(id, old)) & MASK; delete slots[id]; }
  }
  // Seed the slot map + digest from the first-paint DOM (display values).
  function seedFromDOM(el) {
    var nodes = el.querySelectorAll("[data-slot]");
    for (var i = 0; i < nodes.length; i++) {
      var n = nodes[i], id = n.getAttribute("data-slot");
      if (n.hasAttribute("data-list")) continue;
      setSlot(id, slotDisplay(n));
    }
  }
  function slotDisplay(n) {
    var tag = n.tagName.toLowerCase();
    if (tag === "input" || tag === "select" || tag === "textarea") return n.value || "";
    return n.textContent || "";
  }
  function bySlot(id) { return root.querySelector('[data-slot="' + cssEsc(id) + '"]'); }
  function cssEsc(s) { return s.replace(/["\\]/g, "\\$&"); }

  // ---- (b) apply ops, preserving focus / selection / scroll ----
  function morphText(n, val) {
    if (n.textContent !== val) n.textContent = val;
  }
  function morphValue(n, val) {
    var active = document.activeElement === n;
    var ss = active ? n.selectionStart : null, se = active ? n.selectionEnd : null;
    if (n.value !== val) n.value = val;
    if (active && ss !== null) { try { n.setSelectionRange(ss, se); } catch (e) {} }
  }
  function applyOp(op) {
    if (op.kind === 4) { applySplice(op); return; }
    var n = bySlot(op.slotId);
    if (!n) { setSlot(op.slotId, op.payload); return; }
    if (op.kind === 1) { morphText(n, op.payload); setSlot(op.slotId, op.payload); }
    else if (op.kind === 3) { morphValue(n, op.payload); setSlot(op.slotId, op.payload); }
    else if (op.kind === 2) { n.setAttribute(op.attr, op.payload); }
  }
  function applySplice(op) {
    var list = bySlot(op.slotId);
    if (!list) return;
    for (var i = 0; i < op.splices.length; i++) {
      var s = op.splices[i];
      var existing = list.querySelector('[data-key="' + cssEsc(s.key) + '"]');
      if (s.kind === 2) { // remove
        if (existing) { foldRow(existing, -1); existing.parentNode.removeChild(existing); }
      } else if (s.kind === 1) { // add at index
        var tmp = document.createElement("tbody");
        tmp.innerHTML = s.html;
        var frag = tmp.firstElementChild;
        if (frag) {
          var ref = list.children[s.index] || null;
          list.insertBefore(frag, ref);
          foldRow(frag, +1);
        }
      } else if (s.kind === 3) { // move to index
        if (existing) { var ref2 = list.children[s.index] || null; list.insertBefore(existing, ref2); }
      }
    }
  }
  function foldRow(rowEl, sign) {
    var cells = rowEl.querySelectorAll("[data-slot]");
    for (var i = 0; i < cells.length; i++) {
      var id = cells[i].getAttribute("data-slot");
      if (sign > 0) setSlot(id, slotDisplay(cells[i])); else removeSlot(id);
    }
  }

  // ---- (a) SSE + Last-Event-ID reconnect ----
  var es = null;
  function connect() {
    es = new EventSource("/session/" + sessionId + "/events");
    es.addEventListener("resync", function () { doResync(); });
    es.onmessage = function (ev) {
      var frame = decodeFrame(ev.data);
      if (!frame) return;
      for (var i = 0; i < frame.ops.length; i++) applyOp(frame.ops[i]);
      eventSeq = frame.eventSeq;
      if ((digest & MASK) !== (frame.snapshotHash & MASK)) doResync();
    };
    es.onerror = function () { /* EventSource auto-reconnects with Last-Event-ID */ };
  }

  // ---- divergence recovery: POST /resync, adopt fresh snapshot ----
  var resyncing = false;
  function doResync() {
    if (resyncing) return;
    resyncing = true;
    fetch("/session/" + sessionId + "/resync", { method: "POST" })
      .then(function (r) { return r.json(); })
      .then(function (res) {
        var host = root;
        host.innerHTML = res.html;
        slots = Object.create(null); digest = 0n;
        var keys = Object.keys(res.snapshot || {});
        for (var i = 0; i < keys.length; i++) setSlot(keys[i], res.snapshot[keys[i]]);
        eventSeq = res.eventSeq;
      })
      .finally(function () { resyncing = false; });
  }

  // ---- (c) capture events on interactive primitives, POST up ----
  function slotIdOf(el) {
    var n = el;
    while (n && n !== root) { if (n.hasAttribute && n.hasAttribute("data-slot")) return n.getAttribute("data-slot"); n = n.parentNode; }
    return "";
  }
  function post(slotId, event, value) {
    fetch("/session/" + sessionId + "/event", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ slotId: slotId, event: event, value: value, eventSeq: eventSeq })
    });
  }
  root.addEventListener("input", function (e) {
    var id = slotIdOf(e.target); if (id) post(id, "input", e.target.value);
  });
  root.addEventListener("change", function (e) {
    var id = slotIdOf(e.target); if (id) post(id, "blur", e.target.value);
  });
  root.addEventListener("click", function (e) {
    var t = e.target.closest ? e.target.closest("button,[data-submit]") : null;
    if (!t) return;
    var ev = t.hasAttribute("data-submit") || t.getAttribute("type") === "submit" ? "submit" : "click";
    post(slotIdOf(t), ev, t.getAttribute("data-value") || "");
  });

  // ---- binary frame codec decode (mirror internal/ui codec.go) ----
  function decodeFrame(b64) {
    var raw = atob(b64), buf = new Uint8Array(raw.length);
    for (var i = 0; i < raw.length; i++) buf[i] = raw.charCodeAt(i);
    var p = { b: buf, i: 0 };
    if (rbyte(p) !== 1) return null; // codec version
    var eventSeqF = ru64(p), hash = ru64(p), n = ruvarint(p), ops = [];
    for (var k = 0; k < n; k++) {
      var kind = rbyte(p), slotId = rstr(p), op = { kind: kind, slotId: slotId };
      if (kind === 1 || kind === 3) op.payload = rstr(p);
      else if (kind === 2) { op.attr = rstr(p); op.payload = rstr(p); }
      else if (kind === 4) {
        var sn = ruvarint(p); op.splices = [];
        for (var j = 0; j < sn; j++) op.splices.push({ kind: rbyte(p), key: rstr(p), index: Number(ruvarint(p)), html: rstr(p) });
      }
      ops.push(op);
    }
    return { eventSeq: Number(eventSeqF), snapshotHash: hash, ops: ops };
  }
  function rbyte(p) { return p.b[p.i++]; }
  function ru64(p) { var v = 0n; for (var i = 0; i < 8; i++) v = (v << 8n) | BigInt(p.b[p.i++]); return v; }
  function ruvarint(p) { var x = 0n, s = 0n; for (;;) { var c = p.b[p.i++]; x |= BigInt(c & 0x7f) << s; if ((c & 0x80) === 0) break; s += 7n; } return x; }
  function rstr(p) { var n = Number(ruvarint(p)), s = p.b.subarray(p.i, p.i + n); p.i += n; return new TextDecoder().decode(s); }

  // ---- boot ----
  seedFromDOM(root);
  connect();
})();
`
