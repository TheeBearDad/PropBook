const $ = id => document.getElementById(id);
const say = t => { $("msg").textContent = t; };
const KNOWN = /(^|\.)(lucidtrading\.com|apextraderfunding\.com|tradeify\.co|topstepx?\.com|myfundedfutures\.com|takeprofittrader\.com|bulenox\.com|earn2trade\.com|tradeday\.com|elitetraderfunding\.com|vestmarkets\.com|vest\.exchange)$/i;

function ago(ms) {
  const s = Math.round((Date.now() - ms) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return Math.round(s / 60) + " min ago";
  if (s < 86400) return Math.round(s / 3600) + " h ago";
  return new Date(ms).toLocaleDateString();
}
async function activeTab() { const [t] = await chrome.tabs.query({ active: true, currentWindow: true }); return t; }
function siteOf(t) { try { const u = new URL(t.url); return /^https?:$/.test(u.protocol) ? { host: u.hostname, origin: u.origin } : null; } catch (e) { return null; } }

async function refresh() {
  const s = await chrome.runtime.sendMessage({ type: "status" });
  const map = {
    connected: ["ok", "Connected to PropBook", s.queued ? s.queued + " update(s) waiting to send" : "Everything is delivered"],
    closed: ["warn", "PropBook app isn't open", s.queued ? s.queued + " update(s) will send when you open it" : "Open the app to receive updates"],
    badkey: ["bad", "Key doesn't match the app", "Copy the key again from PropBook's Settings window"],
    nokey: ["warn", "Not paired yet", "Paste the key from PropBook's Settings window"]
  }[s.app] || ["", "Unknown", ""];
  $("dot").className = "dot " + map[0]; $("st").textContent = map[1]; $("sub").textContent = map[2];
  $("last").textContent = s.last ? "Last read " + ago(s.last.at) + ": " + (s.last.summary || "dashboard") : "Open a prop firm account page and it syncs on its own.";
  $("route").checked = s.routeDownloads;
  $("ver").textContent = "PropBook Sync " + s.version;
  $("stale").hidden = !s.stale;
  if (s.stale) $("stale-msg").textContent = "Version " + s.stale.want + " is ready, but this copy was loaded from a different folder. On the extensions page, remove this one, then use Load unpacked on " + (s.stale.dir || "PropBook's PropBook Sync folder") + ".";
  const { key } = await chrome.storage.local.get({ key: "" });
  if (key && !$("key").value) $("key").value = key;

  // Offer to read the current site if it isn't one of the built-in firm sites.
  const t = await activeTab(), site = t && siteOf(t), btn = $("site");
  btn.hidden = true;
  if (site && !KNOWN.test(site.host) && site.host !== "127.0.0.1" && site.host !== "localhost") {
    const added = (s.sites || []).includes(site.host);
    btn.hidden = false;
    btn.textContent = added ? "Stop reading " + site.host : "Read this site too (" + site.host + ")";
    btn.dataset.mode = added ? "remove" : "add";
  }
}

$("save").addEventListener("click", async () => {
  const key = $("key").value.trim().toUpperCase();
  await chrome.storage.local.set({ key });
  const r = await chrome.runtime.sendMessage({ type: "flush" });
  say(key ? "Key saved." + (r.sent ? " Sent " + r.sent + " waiting update(s)." : "") : "Key cleared.");
  refresh();
});

$("route").addEventListener("change", e => chrome.storage.local.set({ routeDownloads: e.target.checked }));

$("site").addEventListener("click", async () => {
  const t = await activeTab(), site = t && siteOf(t);
  if (!site) return;
  const id = "site-" + site.host.replace(/[^a-z0-9.-]/gi, "_");
  const { sites = [] } = await chrome.storage.local.get("sites");
  if ($("site").dataset.mode === "remove") {
    try { await chrome.scripting.unregisterContentScripts({ ids: [id] }); } catch (e) {}
    try { await chrome.permissions.remove({ origins: [site.origin + "/*"] }); } catch (e) {}
    await chrome.storage.local.set({ sites: sites.filter(h => h !== site.host) });
    say("Stopped reading " + site.host + ".");
    refresh(); return;
  }
  const ok = await chrome.permissions.request({ origins: [site.origin + "/*"] });
  if (!ok) { say("Permission wasn't granted, so the extension can't read this site."); return; }
  try {
    await chrome.scripting.registerContentScripts([{ id, matches: [site.origin + "/*"], js: ["reader.js"], runAt: "document_idle", persistAcrossSessions: true }]);
  } catch (e) { /* already registered */ }
  if (!sites.includes(site.host)) sites.push(site.host);
  await chrome.storage.local.set({ sites });
  try { await chrome.scripting.executeScript({ target: { tabId: t.id }, files: ["reader.js"] }); } catch (e) {}
  say("Now reading " + site.host + ". Open an account page there to sync it.");
  refresh();
});

$("scan").addEventListener("click", async () => {
  const t = await activeTab();
  try {
    const r = await chrome.tabs.sendMessage(t.id, { type: "scan" });
    say(r && r.ok ? "Read: " + (r.summary || "page") : (r && r.reason) || "Nothing to read on this page.");
  } catch (e) { say("This page isn't being read. Open a prop firm account page, or use \"Read this site too\"."); }
  setTimeout(refresh, 800);
});

$("dump").addEventListener("click", async () => {
  const t = await activeTab();
  try {
    let r;
    try { r = await chrome.tabs.sendMessage(t.id, { type: "dump" }); }
    catch (e) { const [x] = await chrome.scripting.executeScript({ target: { tabId: t.id }, func: () => ({ url: location.href, text: document.body.innerText.slice(0, 40000) }) }); r = x.result; }
    await navigator.clipboard.writeText("URL: " + r.url + "\n\n" + r.text);
    say("Page text copied. Include it when you report the problem, so the reader can be adjusted for this site.");
  } catch (e) { say("Couldn't read this page. Use \"Read this site too\" first, then try again."); }
});

refresh();
