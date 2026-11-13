// Reads a prop firm account page and hands what it finds to the background worker.
// Works from the page's visible text (labels such as "Account Balance", "Minimum Balance",
// "Liquidation Threshold", "Profit Target", a P&L calendar and a trade history table),
// so it doesn't depend on any firm's internal class names.
(() => {
  "use strict";
  if (window.__propbookReader) return; // already running (e.g. injected twice)
  window.__propbookReader = true;

  const FIRMS = [
    [/(^|\.)lucidtrading\.com$/, "Lucid"],
    [/(^|\.)apextraderfunding\.com$/, "Apex"],
    [/(^|\.)tradeify\.co$/, "Tradeify"],
    [/(^|\.)topstep(x)?\.com$/, "Topstep"],
    [/(^|\.)myfundedfutures\.com$/, "My Funded Futures"],
    [/(^|\.)takeprofittrader\.com$/, "Take Profit Trader"],
    [/(^|\.)bulenox\.com$/, "Bulenox"],
    [/(^|\.)earn2trade\.com$/, "Earn2Trade"],
    [/(^|\.)tradeday\.com$/, "TradeDay"],
    [/(^|\.)elitetraderfunding\.com$/, "Elite Trader Funding"],
    [/(^|\.)alpha-?futures\.(com|io)$/, "Alpha Futures"],
    [/(^|\.)fundednext\.com$/, "FundedNext Futures"],
    [/(^|\.)vestmarkets\.com$|(^|\.)vest\.exchange$|(^|\.)vest\.xyz$/, "Vest Markets"]
  ];
  function firmName() {
    const h = location.hostname.toLowerCase();
    for (const [re, n] of FIRMS) if (re.test(h)) return n;
    const parts = h.replace(/^www\./, "").split(".");
    const base = parts.length > 1 ? parts[parts.length - 2] : parts[0];
    return base.charAt(0).toUpperCase() + base.slice(1);
  }

  const MONTHS = ["JANUARY","FEBRUARY","MARCH","APRIL","MAY","JUNE","JULY","AUGUST","SEPTEMBER","OCTOBER","NOVEMBER","DECEMBER"];
  const norm = s => String(s || "").replace(/[−–]/g, "-").replace(/ /g, " ").replace(/\s+/g, " ").trim();
  const MONEY = /^\(?[-+]?\$\s?[-+]?[\d,]+(?:\.\d+)?\)?$/;
  const MONEY_ANY = /\(?[-+]?\$\s?[-+]?[\d,]+(?:\.\d+)?\)?/;

  function money(s) {
    s = norm(s);
    if (!MONEY.test(s)) return null;
    const neg = /-|\(/.test(s);
    const n = parseFloat(s.replace(/[^\d.]/g, ""));
    return isNaN(n) ? null : (neg ? -n : n);
  }
  function moneyIn(s) { const m = norm(s).match(MONEY_ANY); return m ? money(m[0]) : null; }
  function pageLines() { return (document.body ? document.body.innerText : "").split("\n").map(norm).filter(Boolean); }

  // Value printed under (or beside) a label. Tries each label in order.
  function valueAfter(L, labels) {
    for (const label of labels) {
      const U = label.toUpperCase();
      for (let i = 0; i < L.length; i++) {
        const u = L[i].toUpperCase();
        if (u === U || u === U + ":") {
          for (let j = i + 1; j <= i + 3 && j < L.length; j++) { const v = money(L[j]); if (v != null) return v; }
        } else if (u.startsWith(U + " ") || u.startsWith(U + ":")) {
          const v = moneyIn(L[i].slice(label.length)); if (v != null) return v;
        }
      }
    }
    return null;
  }
  function numberAfter(L, label) {
    const U = label.toUpperCase();
    for (let i = 0; i < L.length - 1; i++) if (L[i].toUpperCase() === U && /^\d+$/.test(L[i + 1])) return +L[i + 1];
    return null;
  }

  const BALANCE = ["ACCOUNT BALANCE", "CURRENT BALANCE", "BALANCE", "NET LIQUIDATION VALUE", "NET LIQUIDATION", "NET LIQ", "EQUITY"];
  // "MLL" alone is not used as a label: on Lucid it sits under a progress bar next to the target, so the
  // number after it is often the wrong one.
  const FLOOR = ["MINIMUM BALANCE", "MAX LOSS LIMIT", "LIQUIDATION THRESHOLD", "TRAILING THRESHOLD", "DRAWDOWN THRESHOLD",
    "TRAILING DRAWDOWN LEVEL", "MAX DRAWDOWN LEVEL", "LIQUIDATION LEVEL", "LIQUIDATION BALANCE"];
  const TARGET = ["PROFIT TARGET", "PROFIT GOAL", "TARGET BALANCE"];
  const ID_PATTERNS = [
    /^([A-Z]{2,5}\d{8,})$/i,                                    // Lucid style, e.g. LFE01234567890123
    /\b((?:PA-?)?APEX-?\d{4,}(?:-\d{1,3})?)\b/i,                 // Apex: APEX-123456-01
    /^(?:ACCOUNT(?:\s*(?:ID|#|NUMBER|NO\.?))?[:\s]+)([A-Z][A-Z0-9]*[-_]?\d{4,}[-\w]*)$/i
  ];

  // Numbers printed next to a label: the value right after it, and the one right before it.
  function nearLabel(L, i) {
    const after = [], before = [];
    for (let j = i + 1; j <= i + 3 && j < L.length; j++) { const v = money(L[j]); if (v != null) { after.push(v); break; } }
    if (i > 0) { const v = money(L[i - 1]); if (v != null) before.push(v); }
    return { after, before };
  }

  function idIn(text) {
    const t = norm(text);
    for (const re of ID_PATTERNS.slice(0, 2)) { const m = t.match(re); if (m) return m[1].toUpperCase(); }
    const m = t.match(/\b([A-Z]{2,5}\d{8,})\b/i) || t.match(/\b((?:PA-?)?APEX-?\d{4,}(?:-\d{1,3})?)\b/i);
    return m ? m[1].toUpperCase() : null;
  }
  // An account picker (a dropdown of every account you have) lists other accounts' numbers. Those are set
  // aside, and the one the picker has selected is taken as the account this page is about.
  function pickerInfo() {
    const ids = new Set(); let selected = null;
    document.querySelectorAll("select").forEach(sel => [...sel.options].forEach(o => {
      const id = idIn(o.text) || idIn(o.value); if (!id) return; ids.add(id); if (o.selected) selected = id;
    }));
    document.querySelectorAll('[role="listbox"],[role="menu"],[role="option"],[role="menuitem"],datalist').forEach(el =>
      String(el.innerText || el.textContent || "").split("\n").forEach(t => { const id = idIn(t); if (id) ids.add(id); }));
    if (!selected) document.querySelectorAll('[role="combobox"],[aria-haspopup="listbox"],[aria-haspopup="menu"],[aria-haspopup="true"]').forEach(el => {
      const id = idIn(el.value || el.innerText || el.textContent || ""); if (id && !selected) selected = id;
    });
    return { ids, selected };
  }

  function accountIdFromUrl() {
    const parts = decodeURIComponent(location.pathname + " " + location.search + " " + location.hash).split(/[\/?&=#\s]+/);
    for (const p of parts) for (const re of ID_PATTERNS.slice(0, 2)) { const m = p.match(re); if (m) return m[1].toUpperCase(); }
    return null;
  }

  function readAccount(L) {
    const ids = new Map(), names = [];          // ids: id -> first line index
    L.forEach((l, i) => {
      if (l.length > 60) return;
      for (const re of ID_PATTERNS) { const m = l.match(re); if (m && !ids.has(m[1].toUpperCase())) ids.set(m[1].toUpperCase(), i); }
      const lm = l.match(/\b(LUCID\s?[A-Z]*\s*\d+\s?K)\b/i);
      if (lm) names.push({ name: lm[1].toUpperCase().replace(/\s+/g, " "), i });
      else if (i < 150 && /\b\d{2,3}\s?K\b/i.test(l) && /[A-Z]{2,}/i.test(l.replace(/\d+\s?K/i, "")) && l.length <= 40 && !MONEY_ANY.test(l))
        names.push({ name: l.toUpperCase().replace(/\s+/g, " "), i });
    });
    const urlId = accountIdFromUrl(), pick = pickerInfo();
    const pageIds = [...ids.keys()].filter(x => !pick.ids.has(x) || x === pick.selected);
    let id = urlId || pick.selected || (pageIds.length === 1 ? pageIds[0] : null);
    if (!id && pageIds.length > 1) {
      // Several numbers on the page: take the one printed beside the plan name (e.g. "LUCIDFLEX 25K  LFE…").
      const near = pageIds.filter(x => names.some(n => Math.abs(n.i - ids.get(x)) <= 3));
      if (near.length === 1) id = near[0];
    }

    // The account's plan name is the one printed beside its number. Without a number, a page that shows
    // several plan names (a plans or checkout page, an account switcher) names no single account.
    let name = null;
    const distinct = [...new Set(names.map(n => n.name))];
    if (id && ids.has(id)) {
      const at = ids.get(id), near = names.filter(n => Math.abs(n.i - at) <= 6).sort((a, b) => Math.abs(a.i - at) - Math.abs(b.i - at));
      name = near.length ? near[0].name : (distinct.length === 1 ? distinct[0] : null);
    } else if (distinct.length === 1) name = distinct[0];

    const top = L.slice(0, 150).join(" ").toUpperCase();
    const type = /\bEVAL(UATION)?\b/.test(top) ? "Evaluation" : /\b(FUNDED|PA|PERFORMANCE ACCOUNT|EXPRESS|MASTER)\b/.test(top) ? "Funded" : /\bLIVE\b/.test(top) ? "Live" : "";
    const balance = valueAfter(L, BALANCE);
    let mll = valueAfter(L, FLOOR);

    // Profit target: a labelled value, or a "$26,250 TARGET" style progress-bar label. Of the numbers
    // around the label, keep the first that makes sense as a target (above the loss limit and the balance).
    const okTarget = v => v != null && (mll == null || v - mll >= 250) && (balance == null || v >= balance * 0.8);
    let target = valueAfter(L, TARGET);
    if (!okTarget(target)) {
      target = null;
      for (let i = 0; i < L.length && target == null; i++) {
        if (!/\bTARGET\b/i.test(L[i]) || L[i].length > 40) continue;
        const inline = L[i].match(/(\$\s?[\d,]+(?:\.\d+)?)\s*(?:PROFIT\s+)?TARGET\b/i) || L[i].match(/TARGET\s*:?\s*(\$\s?[\d,]+(?:\.\d+)?)/i);
        const cands = inline ? [money(inline[1])] : (() => { const n = nearLabel(L, i); return [...n.before, ...n.after]; })();
        target = cands.find(okTarget) ?? null;
      }
    }
    const size = name && name.match(/(\d{2,3})\s?K/) ? +name.match(/(\d{2,3})\s?K/)[1] * 1000 : null;
    // Some dashboards show the drawdown or target as an amount ($2,500) rather than a balance level.
    if (balance != null && mll != null && mll < balance * 0.3) mll = Math.round((balance - mll) * 100) / 100;
    if (balance != null && target != null && target < balance * 0.3) target = size ? size + target : null;
    if (balance != null && mll != null && mll >= balance) mll = null;   // a loss limit is always below the balance
    return {
      ids: pageIds, ambiguous: !id && pageIds.length > 1,
      account: { name, id, type, balance, mll, target,
        totalPnl: valueAfter(L, ["TOTAL P&L", "TOTAL PNL", "TOTAL PROFIT"]), tradingDays: numberAfter(L, "TRADING DAYS") }
    };
  }

  function section(L, startRe, endRe) {
    const s = L.findIndex(l => startRe.test(l));
    if (s < 0) return null;
    let e = L.length;
    if (endRe) { const k = L.slice(s + 1).findIndex(l => endRe.test(l)); if (k >= 0) e = s + 1 + k; }
    return L.slice(s + 1, e);
  }
  const CAL_RE = /^(TRADING |P&L |PNL |PROFIT )?CALENDAR\b/i, HIST_RE = /^(TRADING|TRADE) HISTORY\b|^TRADES$|^CLOSED TRADES\b/i;

  function readCalendar(L) {
    const sec = section(L, CAL_RE, HIST_RE);
    if (!sec) return null;
    let year = null, month = null, start = 0;
    for (let i = 0; i < sec.length; i++) {
      const m = sec[i].toUpperCase().match(new RegExp("\\b(" + MONTHS.join("|") + ")\\s+(\\d{4})\\b"));
      if (m) { month = MONTHS.indexOf(m[1]) + 1; year = +m[2]; start = i + 1; break; }
    }
    if (!month) return null;
    const dim = new Date(year, month, 0).getDate();
    const toks = sec.slice(start).join(" ").split(" ");
    const days = []; let cur = null, last = null;
    for (const t of toks) {
      if (/^\d{1,2}$/.test(t) && +t >= 1 && +t <= dim) { cur = { day: +t }; continue; }
      const v = money(t);
      if (v != null && cur && cur.net == null) { cur.net = v; cur.winPct = null; days.push(cur); last = cur; cur = null; continue; }
      const p = t.match(/^(\d{1,3})%$/);
      if (p && last && last.winPct == null) { last.winPct = +p[1]; last = null; }
    }
    const seen = new Set();
    return { year, month, days: days.filter(d => !seen.has(d.day) && seen.add(d.day)) };
  }

  function readHistory(L) {
    const sec = section(L, HIST_RE, null);
    if (!sec) return [];
    const text = sec.join(" ");
    const parts = text.split(/\b(\d{1,2}\/\d{1,2}\/\d{4})\b/);
    const rows = [];
    for (let i = 1; i < parts.length; i += 2) {
      const [mm, dd, yy] = parts[i].split("/");
      const seg = parts[i + 1] || "";
      const toks = seg.trim().split(" ");
      let symbol = null, nums = [], qty = null, commission = null;
      for (const t of toks) {
        if (!symbol) { if (/^[A-Z][A-Z0-9]{1,9}$/.test(t)) symbol = t; continue; }
        const v = money(t);
        if (nums.length < 3) { if (v != null) nums.push(v); continue; }
        if (qty == null) { if (/^\d+$/.test(t)) qty = +t; continue; }
        if (commission == null && v != null) { commission = Math.abs(v); break; }
      }
      if (!symbol || nums.length < 1) continue;
      const durs = [...seg.matchAll(/(\d+)\s*min\s*(\d+)\s*sec/gi)].map(m => m[1] + " min " + m[2] + " sec");
      const lastSec = seg.toLowerCase().lastIndexOf("sec");
      const pm = seg.slice(lastSec >= 0 ? lastSec : 0).match(/(\d{1,3})\s*%/);
      rows.push({
        date: yy + "-" + mm.padStart(2, "0") + "-" + dd.padStart(2, "0"), symbol,
        net: nums[0], high: nums[1] ?? null, low: nums[2] ?? null, qty: qty || 1, commission: commission ?? 0,
        winDur: durs[0] || "", lossDur: durs[1] || "", winPct: pm ? +pm[1] : null
      });
    }
    return rows;
  }

  function scan() {
    const L = pageLines();
    const acct = readAccount(L);
    if (acct.ambiguous) return { ok: false, reason: "This page lists several accounts. Open a single account to sync it." };
    const calendar = readCalendar(L);
    const history = readHistory(L);
    const a = acct.account;
    // Only a page about one account is sent: it shows that account's balance or its account number.
    if (a.balance == null && !a.id)
      return { ok: false, reason: "This page doesn't show one account's balance or account number, so there's nothing to sync." };
    return {
      ok: true,
      payload: { source: "dashboard", firm: firmName(), host: location.hostname, url: location.href, capturedAt: new Date().toISOString(),
        account: a, calendar: calendar && calendar.days.length ? calendar : null, history }
    };
  }

  function summarize(p) {
    const bits = [p.firm];
    if (p.account && (p.account.name || p.account.id)) bits.push(p.account.name || p.account.id);
    if (p.account && p.account.balance != null) bits.push("balance $" + p.account.balance.toLocaleString("en-US"));
    if (p.calendar) bits.push(p.calendar.days.length + " calendar days");
    if (p.history.length) bits.push(p.history.length + " trades");
    return bits.join(" · ");
  }

  let lastSent = "";
  function run(force) {
    const r = scan();
    if (!r.ok) return r;
    const sig = JSON.stringify(Object.assign({}, r.payload, { capturedAt: 0 }));
    if (!force && sig === lastSent) return { ok: true, unchanged: true, summary: summarize(r.payload) };
    lastSent = sig;
    try { chrome.runtime.sendMessage({ type: "dashboard", payload: r.payload, summary: summarize(r.payload) }); } catch (e) {}
    return { ok: true, summary: summarize(r.payload) };
  }

  let timer = null;
  const later = () => { clearTimeout(timer); timer = setTimeout(() => run(false), 2500); };
  new MutationObserver(later).observe(document.documentElement, { childList: true, subtree: true, characterData: true });
  later();

  chrome.runtime.onMessage.addListener((m, _s, send) => {
    if (m && m.type === "scan") { send(run(true)); return; }
    if (m && m.type === "dump") { send({ url: location.href, text: (document.body ? document.body.innerText : "").slice(0, 40000) }); return; }
    if (m && m.type === "ping") { send({ ok: true }); return; }
  });
})();
