# PropBook

A trading journal for prop firm futures traders. It runs on your own computer, and nothing
is sent to a server.

- **P&L calendar, equity curve and loss-limit meter** for each account, with tabs to switch
  between them (Lucid, Apex, Tradeify, Topstep, Vest and others).
- **A written journal for every trade**: what you saw, why you took it and how you felt,
  plus setups (custom setups and combinations) and screenshots.
- **Risk : reward**: planned R:R and the result in R for each trade, worked out from entry, stop and target or typed in, with month and all-time averages.
- **Time in the trade**: total time, time in profit and time in the red.
- **Micro futures built in**: MNQ, MES, MYM, M2K, MGC and MCL, with commissions per contract.
- **Trailing or static max loss limit**, an optional daily loss limit, and a profit target.
- **Imports** from Tradovate, NinjaTrader and position-history CSVs. Drop files in the
  `TradesCSV` folder and they're picked up automatically.
- **PropBook Sync**, a Chrome/Brave extension. It reads balances, limits and trade history
  from prop firm dashboards while you look at them, then sends them to the app.
- **Light and dark mode.**
- **Automatic updates** for both the app and the extension.

## Download

Get the latest version from **[Releases](../../releases/latest)**:

| You have | Download | Then |
|---|---|---|
| Windows 10/11 | `PropBook-x.y.z-Windows.zip` | Unzip it and double-click `PropBook.exe` |
| macOS 12+ (Apple Silicon or Intel) | `PropBook-x.y.z-macOS.zip` | Unzip it, move `PropBook.app` to Applications, then right-click → Open the first time |

PropBook needs Chrome, Edge or Brave installed. It uses that browser for its window.
Each zip has a `Read Me.txt` that explains everything, including how to install the extension.

### Install the extension

1. Open PropBook once. It puts the extension in `Documents/PropBook/PropBook Sync`.
   **Settings → Open extension folder** opens that folder.
2. In Chrome or Brave go to `chrome://extensions`, turn on **Developer mode**, click
   **Load unpacked** and pick that folder.
3. Copy the key from **Accounts** in PropBook into the extension's popup.

## Updates

PropBook checks this repository's latest release when it opens, then every 6 hours. When a
new version is out, a banner offers **Update now**. You can also turn on **Install updates
automatically** under **Settings → Updates and startup**.

- The app downloads the zip for your computer, checks it against `SHA256SUMS.txt`, and
  checks that file's ed25519 signature (`SHA256SUMS.txt.sig`) against the public key built
  into PropBook. Then it swaps itself in and restarts. If anything doesn't match, it
  changes nothing.
- The extension is built into the app. Each update rewrites `PropBook Sync` with the new
  copy. The extension notices the newer version and reloads itself from that folder. If it
  was loaded from a different folder, it shows **UPD** on its icon and explains what to do.
- On macOS, automatic updates need `PropBook.app` to be in Applications (or another folder
  you can write to). They don't work when it runs straight from Downloads.
- Your journal (`Documents/PropBook`) is never touched by an update.

## Your data

Everything lives in `Documents/PropBook`: `journal-data.json`, daily `backups/`,
`screenshots/`, `exports/` and the `TradesCSV/` import folder. To move to a new computer,
copy that folder. A fresh download of PropBook starts empty. Accounts and trades are never
part of the app.

If you used an earlier version called "Trade Journal", PropBook moves
`Documents/Trade Journal` to `Documents/PropBook` the first time it opens.

## Building from source

You need Go 1.24+ and Python 3 with Pillow.

```sh
go run .                          # run from source (opens the window)
go test ./...                     # updater tests
release/build.sh 3.3.0            # builds dist/ for Windows and macOS
```

Environment variables:

| Variable | Use |
|---|---|
| `PROPBOOK_DIR` | Use a different data folder |
| `PROPBOOK_BROWSER` | Path of the browser used for the window |
| `PROPBOOK_NO_WINDOW` | Test mode: print the address instead of opening a window |
| `PROPBOOK_SIGNING_KEY` | Release key (base64 ed25519 seed). Used by `build.sh` to sign `SHA256SUMS.txt` |
| `PROPBOOK_REPO` | `owner/name` the build checks for updates (default `TheeBearDad/PropBook`) |
| `PROPBOOK_LINUX=1` | Also build a Linux zip |

The macOS app is a universal binary (Apple Silicon + Intel), ad-hoc signed by
`tools/macsign` (pure Go, so it builds on Linux), and checked during the build.

## Making a release

1. Bump `version` in `extension/manifest.json` if the extension changed.
2. Optional: write the release notes in `release/notes/3.3.1.md`.
3. Tag and push (`git tag v3.3.1 && git push origin v3.3.1`), or open **Actions → Release → Run workflow** and type the version.

The **Release** workflow builds both zips, signs the checksums with the
`PROPBOOK_SIGNING_KEY` repository secret, and publishes the GitHub release. Installed copies
pick it up on their next check.

> Keep the private release key secret and backed up. Anyone who has it can publish updates
> that every copy of PropBook will install. If you lose it, you'll have to ship a new public
> key in `updater.go`, and everyone will need to download that version by hand.

## Layout

| Path | What it is |
|---|---|
| `main.go`, `window.go`, `extension_link.go`, `fatal_*.go` | Desktop app: local server, app window, extension link |
| `updater.go`, `extmanage.go` | Self-update, the built-in extension copy, and the move from "Trade Journal" |
| `web/index.html` | The journal page |
| `extension/` | PropBook Sync browser extension (Manifest V3) |
| `tools/sign/` | Release key: `keygen`, `sign`, `verify` |
| `tools/macsign/` | Universal macOS binary and `.app` signing from any OS |
| `tools/icons/` | Draws the app and extension icons |
| `release/` | Build script, Read Me files and `Info.plist` |

## License

MIT. See [LICENSE](LICENSE). PropBook is a journal, not trading advice.
