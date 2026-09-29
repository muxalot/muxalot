# Security analysis

Static review of `agent/` (Go) and `app/` (Android), plus the install and release path in `deploy/`, `Makefile` and `.github/`. To report a vulnerability, see [SECURITY.md](SECURITY.md).

- **Date:** 2026-09-29
- **Revision reviewed:** `f9a1be9`. Fixes for most findings shipped in v0.2.0; each finding carries its status.
- **Method:** manual code review, `go vet`, `staticcheck`, `govulncheck` (binary mode, built with the Go version pinned in `agent/go.mod`), existing agent test suite (passes). A static review is not a guarantee: it can miss issues, and it does not replace testing or an independent audit.
- **Not covered:** dynamic testing or fuzzing, a CVE scan of the Gradle dependencies, `site/`, the Play Store listing, the server's reverse proxy configuration beyond the shipped snippets.
- **Confidence levels:** high = confirmed in code; moderate = follows from the code but depends on deployment; low = plausible, not confirmed.

## Threat model

| Actor | Can do | Goal |
|---|---|---|
| Remote attacker | Reach the public HTTPS endpoint, send arbitrary requests | Get a shell, read or write files, deny service |
| Person with the phone | Hold or unlock a lost or stolen phone; see the screen | Use the paired key, read terminal contents |
| Malicious or compromised server | Control every byte the app receives | Attack the phone: clipboard, WebView, phishing |
| Supply-chain attacker | Tamper with the release, the install script or dependencies | Run code as root on servers, or in the app |

A paired device is a full shell as the agent's Unix user. Everything the agent protects is therefore "who is allowed to pair and sign", not what a paired device can do.

## Summary

| ID | Severity | Finding |
|---|---|---|
| M1 | Medium | Release binary built with a Go toolchain that has many known stdlib vulnerabilities |
| M2 | Medium | Revoking a device does not end its open terminal sessions |
| M3 | Medium | Release integrity relies on a same-origin checksum; nothing is signed or pinned |
| M4 | Medium | The device key has no user-presence or biometric gate |
| M5 | Medium | Any terminal output can silently overwrite the phone clipboard (OSC 52) |
| L1 | Low | Unauthenticated requests cost a file read and a global lock |
| L2 | Low | `/pair` has no body read deadline |
| L3 | Low | Pairing rate limiter: IPv6 keying, O(n) cleanup, shared bucket behind a non-loopback proxy |
| L4 | Low | Bracketed paste can be escaped by clipboard content |
| L5 | Low | `muxalot://pair` deep link pre-fills a pairing target from any app or web page |
| L6 | Low | `--files-root` is not a security boundary; uploads are created `0644` |
| L7 | Low | WebView and screen hardening gaps (`allowFileAccess`, no `FLAG_SECURE`, no navigation lock) |
| L8 | Low | App trusts server responses: unbounded reads, redirects followed |
| L9 | Low | Replay window reopens on agent restart (in-memory nonce cache) |
| L10 | Low | A TLS-terminating middlebox (for example a proxying CDN) can hijack sessions |
| L11 | Low | systemd unit is unhardened; default service user is the invoking admin |
| L12 | Info | Aging or unmaintained dependencies |

No critical or high findings were identified. This review did not find an authentication bypass, path traversal, command injection or signature-verification flaw.

## Findings

### M1. Release binary carries known Go standard library vulnerabilities

> **Status: fixed in v0.2.0.** `go.mod` now requires Go 1.26.8 (`govulncheck` reports none) and CI runs `govulncheck`. The text below describes the state at review time.

- **Where:** `agent/go.mod` (`go 1.24.7`), `.github/workflows/release.yml` (`go-version-file: agent/go.mod`).
- **What:** the release job builds with exactly Go 1.24.7. `govulncheck -mode=binary` on a binary built that way reports more than 40 advisories across `net/http`, `net/url`, `net/textproto`, `encoding/asn1`, `mime`, `os`, `net`, `crypto/tls` and `crypto/x509`. Several are fixed only in 1.25.x. Binary mode cannot decide reachability. The agent serves plain HTTP behind a proxy, so the `crypto/tls` and `crypto/x509` items are probably not reachable. `net/http`, `net/url`, `net/textproto` and `encoding/asn1` (used by `ecdsa.VerifyASN1` on unauthenticated input) are on the request path.
- **Confidence:** high that the advisories apply to the toolchain; moderate on which are reachable.
- **Fix:** raise the `go` directive to a currently supported release (the advisories name 1.25.13 as the newest fix), and run `govulncheck ./...` in CI so a stale toolchain fails the build. Dependabot does not bump the Go toolchain.

### M2. Revocation does not end live sessions

> **Status: fixed in v0.2.0.** An open terminal re-checks its device every 5 s and closes when it is revoked (`TestRevokeClosesLiveTerminal`). The tmux session itself keeps running, as with any disconnect. The text below describes the state at review time.

- **Where:** `agent/server.go` `auth()` and `handleWS`; `agent/store.go` `Revoke`.
- **What:** a signature is checked once, at the WebSocket upgrade. `muxalot-agent revoke <id>` removes the key, but a connection that is already open keeps its PTY until it drops. For a lost phone, that is the case that matters most.
- **Confidence:** high.
- **Fix:** record the device id on each connection and re-check `Lookup` on the 25 s keepalive tick, closing the socket when the device is gone. Alternatively make `revoke` signal the running server.

### M3. Release integrity and the install path

> **Status: mostly fixed in v0.2.0.** GitHub Actions are pinned to commit SHAs; the release workflow now publishes a signed build-provenance attestation for each agent binary; `install.sh` verifies it when GitHub CLI 2.49+ is present (aborting on failure, noting the skip otherwise); the README documents manual verification and a tag-pinned install. Verified: `gh attestation verify` works without a login and exits non-zero for an unattested file. The v0.2.0 release workflow ran the attestation step, and the published binary passes `gh attestation verify`; `install.sh`'s own verification path has not been run against a release. Still open: branch and tag protection and 2FA on the GitHub account (repository settings, not code), and the attestation check is skipped where `gh` is missing, so the default `curl | sudo sh` path is still checksum-only on most servers. `make release-agent` uploads a local, unattested build and would fail verification; let CI publish. The text below describes the state at review time.

- **Where:** `deploy/install.sh`, `Makefile` (`SHA256SUMS`), `.github/workflows/*.yml`.
- **What:**
  - `install.sh` downloads the binary and `SHA256SUMS` from the same GitHub release. That catches corruption but not tampering: anyone who can replace the release assets replaces both files.
  - Nothing is signed (no cosign, minisign or GPG) and there is no build provenance attestation.
  - The documented command is `curl … raw.githubusercontent.com/…/main/deploy/install.sh | sudo sh`. It follows the mutable `main` branch and runs as root, so a compromised maintainer account or token means root on every server that installs afterwards.
  - GitHub Actions are pinned to tags (`@v4`, `@v5`), not commit SHAs.
  - The Android APK and Play bundle are signed with separate keys, which is good. The keys live in a gitignored `keystore.properties` on the maintainer's machine.
- **Confidence:** high.
- **Fix:** sign release assets and verify in `install.sh`, or publish GitHub build attestations and verify with `gh attestation verify`; pin Actions to SHAs (Dependabot can keep them current); document a "download, inspect, then run" install path and a release-tag-pinned install URL; enable branch protection, 2FA and tag protection on the repository.

### M4. No second factor on the device key

> **Status: mitigated in v0.2.0 (opt-in).** The server list has an "App lock" toggle. When on, the app asks for the phone's screen-lock credential (PIN, pattern or password) on a cold start, every time the screen turns off, and after 60 s in another app, and shows only a lock screen until it passes; turning the lock on or off needs the credential too. This is a software gate in front of the Keystore key, not a hardware-bound key: malware running as the app's uid is not stopped, and the credential prompt does not offer biometrics. Hardware-bound, biometric-gated keys remain an option (new pairings only; it needs the `androidx.biometric` dependency). The text below describes the state at review time.

- **Where:** `app/.../data/Data.kt` `DeviceKey.create`.
- **What:** the key is a non-exportable Keystore key, which protects it from theft of the key material. Its `KeyGenParameterSpec` does not call `setUserAuthenticationRequired`, `setIsStrongBoxBacked` or attestation. Anyone who can open the app on an unlocked phone gets a shell as the server user with nothing further to defeat, and so does malware running as the app's uid.
- **Confidence:** high.
- **Fix:** offer an app lock: a Keystore key bound to biometric or device credential with a short validity window (`setUserAuthenticationParameters`), plus `setInvalidatedByBiometricEnrollment(true)`. Try StrongBox where present. Keep it opt-in so pairing on devices without a lock screen still works. Pair with M2, so a reported loss can be shut off quickly.

### M5. Terminal output can overwrite the phone clipboard

> **Status: fixed in v0.2.0.** An OSC 52 write now opens a "Copy to clipboard?" dialog showing the length and a 300-character preview; Deny (or a second request while the dialog is open) leaves the clipboard untouched. Long-press selection and the menu's "Copy server clipboard" are user-initiated and still copy directly. The text below describes the state at review time.

- **Where:** `app/.../assets/terminal.html` (OSC 52 handler), `TerminalPane.kt` `onClipboard`.
- **What:** any text that reaches the terminal, including `cat` of a hostile file or output from a compromised server, can carry an OSC 52 sequence. The app writes it to the system clipboard with no prompt, and only a "Copied" toast. A later paste into a browser, banking app or password field can then insert attacker-chosen content.
- **Confidence:** high.
- **Fix:** ask before accepting an OSC 52 write, or add a per-server setting that defaults to off, and show the length and a preview.

### L1. Unauthenticated requests are expensive

> **Status: fixed in v0.2.0.** `Store.Lookup` keeps an in-memory device cache that is valid while `state.json` is the same file with the same mtime; unknown ids and known devices no longer take the file lock or parse the file (`TestLookupUnknownIDSkipsStateLock`, `TestLookupSeesRevokeAndAdd`). Each request still costs one `stat`. The text below describes the state at review time.

- **Where:** `agent/server.go` `verify` → `Store.Lookup`.
- **What:** `Lookup` takes a process-wide mutex and an exclusive `flock`, reads and parses `state.json`, and only then does the signature check. Any client can send a well-formed `Authorization: Sig …` header with a fresh timestamp and a random id. Each such request serialises on that lock and hits the disk. The cheap checks (field lengths, timestamp window) run first, but they are easy to satisfy. There is no limit on this path by design (see the comment in `auth`).
- **Confidence:** high.
- **Fix:** cache devices in memory with an mtime check on `state.json`, and use the file lock only for writes. Alternatively rate limit failures per IP with a generous bucket that a legitimate device never reaches.

### L2. `/pair` has no body read deadline

> **Status: fixed in v0.2.0.** `handlePair` sets a 10 s read deadline (`TestPairBodyReadDeadline`). The text below describes the state at review time.

- **Where:** `agent/main.go` (`http.Server` has `ReadHeaderTimeout` only), `handlePair`.
- **What:** the body is capped at 4096 bytes, but a client that sends headers and then stalls holds a goroutine indefinitely. A fronting proxy usually cuts this off. Direct exposure (the `--listen` LAN case in the README) does not.
- **Confidence:** moderate.
- **Fix:** `http.NewResponseController(w).SetReadDeadline(...)` at the top of `handlePair`.

### L3. Pairing rate limiter

> **Status: fixed in v0.2.0.** IPv6 clients are bucketed by /64, and the expired-entry sweep runs at most every window/10 (`TestLimiterKeysIPv6ByPrefix`, `TestLimiterSweepIsThrottled`). I did not add a global attempt ceiling: at 40 bits of code entropy it adds nothing, and it would let anyone lock every user out of pairing with a hundred requests. Behind a non-loopback proxy or CDN all clients still share one bucket (documented). The text below describes the state at review time.

- **Where:** `agent/server.go` `limiter`, `clientIP`.
- **What:**
  - It is keyed per IP address. An attacker with an IPv6 prefix has effectively unlimited addresses. At 40 bits of code entropy and a 10-minute lifetime, guessing a live code is still infeasible, so this is defence in depth.
  - `fail()` scans the whole map on every failure. A large number of distinct source addresses makes that O(n) work per request.
  - Behind a non-loopback proxy or a CDN, all clients share one bucket, so ten bad attempts from anyone block pairing for everyone for 15 minutes. The README already notes the shared bucket.
- **Confidence:** high.
- **Fix:** key IPv6 on the /64, run cleanup on a timer instead of per failure, and add a small global attempt ceiling.

### L4. Bracketed paste can be escaped

> **Status: fixed in v0.2.0.** `paste` strips ESC characters from the text before wrapping it. The Android app has no automated tests, so this is verified by build only. The text below describes the state at review time.

- **Where:** `app/.../ui/Input.kt` `paste`.
- **What:** with bracketed paste enabled, the text is wrapped in `ESC[200~ … ESC[201~` unmodified. Clipboard text that itself contains `ESC[201~` ends the bracket early, and what follows is typed as ordinary input, including newlines. Text copied from a web page can do this.
- **Confidence:** high.
- **Fix:** strip `\u001b` (or at least `\u001b[201~`) from pasted text before wrapping, as most terminals do. Without bracketed paste, a multi-line paste executes immediately. That is inherent, but a confirmation for multi-line pastes is worth considering.

### L5. Pairing deep link

> **Status: fixed in v0.2.0.** A pairing pre-filled by a link asks "Pair with <host>?" before sending anything. A link no longer replaces a live terminal, file transfer or shortcuts screen; the app shows a toast instead. The text below describes the state at review time.

- **Where:** `AndroidManifest.xml` (exported, `BROWSABLE` `muxalot://pair`), `MainActivity.handlePairIntent`, `Screens.kt` `PairScreen`.
- **What:** a web page or app can open `muxalot://pair?url=https://attacker.example&code=…`. The pair screen is pre-filled and the user must tap Pair. The URL is visible and must be `https://`. Someone who pairs anyway ends up with a terminal on the attacker's server, which is a social-engineering risk (typing secrets into it) rather than a technical bypass. The intent also replaces whatever screen is open, including a live terminal.
- **Confidence:** high.
- **Fix:** confirmation dialog naming the host ("Pair with attacker.example?"); do not replace an active terminal screen.

### L6. Files root is not a security boundary

> **Status: fixed in v0.2.0.** New uploads are created `0600` (overwrites keep the file's mode). The README now says confinement is defence in depth, not isolation. The text below describes the state at review time.

- **Where:** `agent/server.go` `resolve`, `handleUpload`; README wording.
- **What:** path handling is correct: absolute and relative paths are cleaned, symlinks resolved, containment checked on both the input and the resolved path, FIFOs refused, uploads written to a temp file and published atomically. The tests cover traversal and symlinks. But a paired device already has a shell as the same Unix user, so `--files-root` only guards against bugs in the file endpoints, not against a paired device. Two smaller points: new uploads are created `0644` regardless of umask, so a sensitive upload is world-readable on a shared host; `GET /ls` returns the resolved absolute path.
- **Confidence:** high.
- **Fix:** say so in the README (defence in depth, not isolation); create uploads `0600` unless the user says otherwise.

### L7. WebView and screen hardening

> **Status: fixed in v0.2.0.** `allowFileAccess` is off, WebView navigation is blocked, and `FLAG_SECURE` is on by default in release builds, with a Settings switch "Allow screenshots" (debug builds default to allowed so adb screencap works). Verified on a device: the screen goes blank in a screenshot with the switch off. Not verified on a device: the terminal page rendering with `allowFileAccess` off, since no server was paired on the test phone. The text below describes the state at review time.

- **Where:** `app/.../ui/TerminalPane.kt`, `AndroidManifest.xml`.
- **What:**
  - `allowFileAccess = true` is not needed for `file:///android_asset/` (asset URLs are not governed by that setting). Turn it off, or serve assets through `WebViewAssetLoader`.
  - There is no `WebViewClient`, so navigation is not locked to the bundled page.
  - The `Android` JavaScript bridge is exposed for the life of the page. Its inputs are only what the bundled page passes, and terminal output reaches the page through `term.write` as data. Nothing was found that lets terminal output call the bridge, but xterm.js is the only barrier. Keep it updated.
  - `FLAG_SECURE` is not set, so terminal contents show in the recents thumbnail and in screenshots and screen recordings.
  - `allowBackup="false"`, `usesCleartextTraffic="false"` and no debuggable WebView are all correct.
- **Confidence:** high on the settings; low on any exploit.
- **Fix:** the three settings above; make `FLAG_SECURE` a setting or default it on.

### L8. The app trusts server responses

> **Status: fixed in v0.2.0.** Text responses are capped at 1 MiB and redirects are no longer followed. The text below describes the state at review time.

- **Where:** `app/.../net/Api.kt`.
- **What:** `body.string()` reads whole responses into memory for `/sessions`, `/ls` and error bodies, so a hostile server can exhaust memory. The shared `OkHttpClient` follows redirects. OkHttp strips `Authorization` on a cross-host redirect, and the signature is bound to host and path, so nothing useful leaks, but a redirected `/pair` POST is turned into a GET. Not exploitable for more than a crash or a failed pairing.
- **Confidence:** high.
- **Fix:** cap response sizes; set `followRedirects(false)`.

### L9. Replay window reopens on restart

- **Where:** `agent/server.go` `nonceCache`.
- **What:** nonces live in memory for about two minutes. If the agent restarts, a previously captured header is valid again for up to 60 s of its timestamp. This needs a captured request, which means TLS was already broken or terminated by a party that can read requests (see L10).
- **Confidence:** high.
- **Fix:** acceptable as is. To close it, refuse requests with timestamps older than the process start time.

### L10. TLS-terminating middleboxes

- **Where:** design; README "Known limitations".
- **What:** authentication happens per HTTP request, and a WebSocket is authenticated only at upgrade. The upload body is not signed. Whoever terminates TLS can therefore read all terminal traffic and, by using a captured upgrade request once before the phone does, take over a session. A proxying CDN such as Cloudflare in "orange cloud" mode is such a party. There is no certificate pinning, only the system CA store (documented).
- **Confidence:** moderate.
- **Fix:** document that the TLS endpoint must be one you trust, prefer DNS-only or a tunnel you control, and consider optional SPKI pinning per server. A stronger fix is a session key exchange that binds the WebSocket to the device key, at a real complexity cost.

### L11. Service hardening and defaults

- **Where:** `agent/install.go` `unitFile`, `deploy/muxalot-agent.service`.
- **What:** the unit has no sandboxing. The comment explains why: it is an interactive shell. The installer defaults to the `sudo` invoker's own account, so the agent exposes that admin's full shell to any paired key. The installer refuses root, which is right. The Caddy example uses a Cloudflare DNS token; keep it scoped to `Zone:DNS:Edit` on the one zone.
- **Confidence:** high.
- **Fix:** none required. Consider making the dedicated user the default and stating the blast radius in the install output.

### L12. Dependencies

- **Go:** `gorilla/websocket` 1.5.3 and `creack/pty` 1.1.24 are current. `skip2/go-qrcode` (2020) is unmaintained but only renders QR codes in the `pair` command. Dependabot is enabled.
- **Android:** `zxing-android-embedded` 4.3.0 (2021), Compose BOM 2024.10.01, OkHttp 4.12.0, kotlinx-serialization 1.7.3. These were not CVE-scanned in this review. Run `dependencyCheck` or OSV-Scanner over `app/`.

## Desktop client (`desktop/`, Wails v3, Linux)

Added after the review above; this section is the author's own design review plus checks run against the built binary, not an independent audit. Design: Go holds the device key, signs requests and owns the WebSocket; the webview renders xterm.js and can call only the methods in `Service` (`desktop/service.go`).

| ID | Item | Status |
|---|---|---|
| W1 | Every exported `Service` method is callable by any script in the page, and so are Wails' own built-in runtime calls (open a URL in the browser, set/read the clipboard, native dialogs, window control, events) | Partly mitigated: no `Service` method signs, returns key material or takes a local path from JS, and `TestBoundSurface` fails if that list changes. The built-in runtime calls are **not** restricted: a script that got past the CSP could use them (for example write the clipboard without the OSC 52 prompt, which only guards terminal output). The runtime requests carry an object id, so an allowlist in the asset middleware may be possible; not implemented and not verified for every transport |
| W2 | No framework permission layer, CSP or navigation control | Partly mitigated: strict CSP set by asset middleware (`TestCSPHeaderAndPolicy`); no page HTML is built from server text (`textContent` only, checked with a hostile file name). Wails v3 beta.26 has no navigation-policy hook, so a script that ran anyway could still navigate the webview. Residual, needs a script-injection bug first |
| W3 | Key custody | Mitigated, not solved: OS keyring or a passphrase-wrapped file, no silent fallback (`keystore` tests). Either way the key is software-held; any process running as the same user can use the keyring while it is unlocked |
| W4 | OSC 52 clipboard overwrite | Fixed for terminal output: a confirm dialog with length and preview; Deny leaves the clipboard untouched (checked in the running app). Does not protect against a script running in the page itself (see W1) |
| W5 | Server-supplied file names become local paths | Mitigated: `SafeName` on the suggested name, the user picks the destination in a native dialog, created `O_EXCL` 0600 (unit tested). The native dialog flows themselves were not exercised automatically |
| W6 | Hostile server: redirects, huge bodies/frames | Fixed: redirects not followed, 1 MB API cap, 4 MB WebSocket frame limit, https required (http only for loopback) |
| W7 | Go cannot reliably zeroize the private key in memory | Accepted |
| W8 | cgo and the system webview are native code | Accepted; WebKitGTK is patched by the distribution, not by this project |
| W10 | DevTools and dev asset server in a release | Release builds use the `production` tag and set `DevToolsEnabled: false`; the dev server override is compiled out |
| W11 | Wails v3 is beta | Pinned to `v3.0.0-beta.26` |
| W12 | Toolchain and module vulnerabilities | `govulncheck` in CI; last run reported none affecting the code (one in a required module that the code does not call) |
| D5 | Bracketed paste escape | Fixed: `Paste` in Go drops ESC and other control characters (`AAA<ESC>[201~BBB<^C>CCC` arrived as `AAA[201~BBBCCC`, checked in the running app). A multi-line confirmation appears only when the terminal is not in bracketed-paste mode; tmux normally turns that mode on, so it rarely shows up under tmux, as in any terminal |
| X1 | **Terminal query replies.** The desktop client must wire `term.onData` to accept typing, so xterm.js answers terminal queries (device attributes, cursor position reports) from any output into the shell as input. The Android app avoids this by design (see "What is done well") | Accepted: the same as every desktop terminal emulator; the replies are short, fixed sequences and xterm.js does not implement title reports. Swallowing them would break programs that query the terminal |

Release artifact (M3 for the desktop client): the tag workflow builds the `.deb` on Ubuntu 22.04, installs and launches it on Ubuntu 22.04, 24.04, 26.04 and Debian 12, 13, and only then publishes it with a signed build-provenance attestation (`gh attestation verify`). The package has no maintainer scripts, nothing setuid, and files are `root:root` with directories `0755`. There is no apt repository and no package signing key: trust rests on the attestation and the release page, and a `.deb` uploaded locally with `make release-desktop` is not attested (the target refuses to replace an existing `.deb` unless `FORCE=1`). The smoke test proves the package installs and the page loads under software rendering; it cannot catch desktop-specific hangs like the WOFF-font one.

Not verified: macOS and Windows builds; the native upload and download dialogs; the OS keyring backend on a real desktop session (tests use a fake, the GUI check used passphrase mode); the go-keyring macOS backend possibly passing secrets on a command line (unconfirmed, verify before enabling keyring mode there); a distribution WebKitGTK older than 2.52.

## Code hygiene

- `agent/server.go` `ctxKey` was unused (`staticcheck` U1000); now used by the M2 fix.
- `app/app/proguard-rules.pro` has `-dontwarn org.bouncycastl`, probably meant `org.bouncycastle`.
- `Store.save` renames without `fsync`; a crash can lose the last write (a pairing or a `last_seen` update).
- `install.go` writes `/usr/local/bin/muxalot-agent` in place, which fails with "text file busy" when upgrading a running service.

## What is done well

- Public-key auth with a non-exportable Keystore key; the server stores public keys only, so a server breach leaks no credentials.
- Signature covers method, host, full request URI, timestamp and a nonce; a 60 s skew window; nonces consumed only after the signature verifies, so junk cannot fill the cache.
- Unauthenticated routes return a bare 404. Pairing codes are single-use, 40-bit, hashed at rest, compared in constant time, expire in 10 minutes, and failed attempts are rate limited.
- Session names are validated by a strict regex on every route, and tmux gets them as separate arguments (`exec.Command`, `=name` targets), so there is no shell or tmux command injection. Clipboard text goes to `tmux set-buffer --`.
- File endpoints: symlink-safe containment, FIFO refusal, `MaxBytesReader`, atomic publish with `link(2)` to avoid overwrite races, disk-write errors reported.
- WebSocket read limit, deadlines and keepalive; the agent refuses to run as root; state file is `0600` in a `0700` directory with a file lock.
- The app never wires `term.onData`, so terminal query responses (device attributes, cursor position reports) from a hostile server are not sent back as input.
- App: HTTPS enforced at pairing, cleartext disabled, backups disabled, minified release build, user-installed CAs not trusted by default on the current target SDK.

## Remaining work

- L9 (nonce cache lost on restart), L10 (TLS-terminating middleboxes) and L11 (unhardened unit, default service user) are open by design or documentation-level.
- L12: scan the Android dependencies (OSV-Scanner or `dependencyCheck`); none of them were CVE-scanned.
- M3: branch and tag protection and 2FA on the GitHub account; make the attestation check mandatory if `gh` becomes common on servers.
- M4: optional biometric-bound, hardware-backed keys for new pairings.
- Housekeeping from the Code hygiene list.
