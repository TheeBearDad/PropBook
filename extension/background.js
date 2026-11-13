// Queues Lucid data and delivers it to the PropBook app on this computer.
// If the app is closed, data waits here and is sent the next time the app is open.
const APP = "http://127.0.0.1:47321";

async function getState() {
  return chrome.storage.local.get({ key: "", queue: [], last: null, routeDownloads: true });
}

async function setBadge() {
  const { queue, key } = await getState();
  const { stale } = await chrome.storage.local.get("stale");
  const text = !key ? "KEY" : stale ? "UPD" : queue.length ? String(queue.length) : "";
  chrome.action.setBadgeText({ text });
  chrome.action.setBadgeBackgroundColor({ color: !key || stale ? "#d8b35b" : "#f0605a" });
}

async function enqueue(payload, summary) {
  const { queue } = await getState();
  queue.push(payload);
  while (queue.length > 20) queue.shift();
  await chrome.storage.local.set({ queue, last: { at: Date.now(), summary, url: payload.url } });
}

const MY_VERSION = chrome.runtime.getManifest().version;

function newerVersion(a, b) { // true when version a is newer than b ("2.10.0" > "2.9.1")
  const pa = String(a || "").split(".").map(Number), pb = String(b || "").split(".").map(Number);
  if (pa.length !== 3 || pb.length !== 3 || pa.concat(pb).some(isNaN)) return false;
  for (let i = 0; i < 3; i++) if (pa[i] !== pb[i]) return pa[i] > pb[i];
  return false;
}

// PropBook keeps the newest copy of this extension in its PropBook Sync folder. When the app says it has
// a newer one, reload once to pick up the new files. If this copy was loaded from somewhere else, the
// reload changes nothing, so the popup asks to load it from PropBook's folder instead.
async function checkAppCopy(info) {
  if (!info || !info.extVersion) return;
  const { reloadedFor } = await chrome.storage.local.get("reloadedFor");
  if (!newerVersion(info.extVersion, MY_VERSION)) {
    const { stale } = await chrome.storage.local.get("stale");
    if (stale) { await chrome.storage.local.set({ stale: null }); await setBadge(); }
    return;
  }
  if (reloadedFor !== info.extVersion) {
    await chrome.storage.local.set({ reloadedFor: info.extVersion });
    chrome.runtime.reload();
    return;
  }
  await chrome.storage.local.set({ stale: { want: info.extVersion, dir: info.extDir || "" } });
  await setBadge();
}

async function ping(key) {
  try {
    const r = await fetch(APP + "/ext/status", { headers: { "X-Journal-Key": key, "X-Ext-Version": MY_VERSION } });
    if (r.status === 403) return "badkey";
    if (!r.ok) return "closed";
    try { await checkAppCopy(await r.json()); } catch (e) {}
    return "connected";
  } catch (e) { return "closed"; }
}

async function flush() {
  const { queue, key } = await getState();
  let result = { sent: 0, left: queue.length, app: key ? "unknown" : "nokey" };
  if (key && queue.length) {
    let sent = 0;
    for (const p of queue) {
      try {
        const r = await fetch(APP + "/ext/lucid", {
          method: "POST", headers: { "Content-Type": "application/json", "X-Journal-Key": key }, body: JSON.stringify(p)
        });
        if (r.status === 403) { result.app = "badkey"; break; }
        if (!r.ok) { result.app = "closed"; break; }
        sent++; result.app = "connected";
      } catch (e) { result.app = "closed"; break; }
    }
    const fresh = (await getState()).queue;
    fresh.splice(0, sent);
    await chrome.storage.local.set({ queue: fresh });
    result = Object.assign(result, { sent, left: fresh.length });
    if (sent) await chrome.storage.local.set({ lastDelivered: Date.now() });
  }
  await setBadge();
  return result;
}

chrome.runtime.onMessage.addListener((m, _sender, send) => {
  if (m.type === "dashboard" || m.type === "lucid") { enqueue(m.payload, m.summary).then(flush).then(send); return true; }
  if (m.type === "flush") { flush().then(send); return true; }
  if (m.type === "status") {
    (async () => {
      const s = await getState();
      const app = s.key ? await ping(s.key) : "nokey";
      const { lastDelivered = null } = await chrome.storage.local.get("lastDelivered");
      const { sites = [] } = await chrome.storage.local.get("sites");
      const { stale = null } = await chrome.storage.local.get("stale");
      send({ app, queued: s.queue.length, last: s.last, lastDelivered, routeDownloads: s.routeDownloads, hasKey: !!s.key, sites, stale, version: MY_VERSION });
    })();
    return true;
  }
});

chrome.alarms.create("flush", { periodInMinutes: 1 });
chrome.alarms.onAlarm.addListener(async a => {
  if (a.name !== "flush") return;
  await flush();
  const { key } = await getState();
  if (key) await ping(key); // also notices a newer extension from the app
});
chrome.runtime.onInstalled.addListener(setBadge);
chrome.runtime.onStartup.addListener(setBadge);

// Tradovate (and prop firm dashboard) CSV exports go to a "PropBook" folder in Downloads, which the app watches.
chrome.downloads.onDeterminingFilename.addListener((item, suggest) => {
  const where = [item.url, item.finalUrl, item.referrer].join(" ").toLowerCase();
  const isCsv = /\.csv$/i.test(item.filename || "") || /csv/i.test(item.mime || "");
  if (!isCsv || !/tradovate\.com|lucidtrading\.com|apextraderfunding\.com|tradeify\.co|topstepx?\.com|myfundedfutures\.com|takeprofittrader\.com|vestmarkets\.com|vest\.exchange/.test(where)) return;
  chrome.storage.local.get({ routeDownloads: true }).then(({ routeDownloads }) => {
    if (!routeDownloads) { suggest(); return; }
    const name = (item.filename || "trades.csv").split(/[\\/]/).pop();
    suggest({ filename: "PropBook/" + name, conflictAction: "uniquify" });
  });
  return true;
});
