# Desktop client (Wails v3) - implementation plan

Delete this file when all stages are complete.

Goal: a Linux desktop client for muxalot (macOS/Windows build-only in CI). Go process owns key, signing, HTTP and WebSocket; the webview only renders xterm.js. No agent changes.

Security review: W1-W12 (Go/Wails) and D1-D12 (shared) from the design discussion; each stage lists the ones it closes. Stage 4 is the gate: nothing ships until its checklist passes.

## Decisions (veto any before Stage 0)

1. Wails v3 (beta), pinned exact tag, GTK3/WebKitGTK 4.1 build (`-tags gtk3`; the dev box has 4.1, not 6.0). Wails CLI runs via `go run ...@<tag>`, never installed.
2. Separate Go module `desktop/` (`muxalot-desktop`). The agent module is untouched: no shared `wire` module. The signed string is one line; the e2e test against the real agent binary catches drift.
3. Key storage: OS keyring (`zalando/go-keyring`) by default; passphrase-wrapped file (argon2id + XChaCha20-Poly1305) as a per-server choice. No silent fallback: keyring unavailable = error telling the user to pick passphrase mode.
4. URLs: `https` only; plain `http` only for loopback (dev/e2e). No LAN-http checkbox in v1.
5. Pairing by pasting URL + code, or pasting the `muxalot://pair?...` link (parsed into the fields). No QR, no deep-link registration, no app lock.
6. Out of scope: themes/pro gating, tmux clipboard buffer sync (`clip_get`/`clip_set`), auto-update, installers (AppImage/deb/dmg/msi), release-workflow wiring, iOS/Android targets.
7. Files (browse/download/upload) are included, last (Stage 5), so it can be cut without touching the rest.

## Layout

```
desktop/
  go.mod                 muxalot-desktop, go 1.26.8 (match agent), wails v3 pinned
  main.go                app + window + embed + CSP; nothing else
  service.go             the ONE bound service; exported methods = the whitelist
  internal/config/       servers.json (0600, dir 0700) under os.UserConfigDir()/muxalot
  internal/keystore/     keyring + passphrase file
  internal/client/       auth.go api.go conn.go files.go (no Wails imports)
  internal/e2e/          builds ../agent, runs it, drives client (skips without tmux)
  frontend/              index.html app.js style.css; vendor/ + bindings/ are build output (gitignored)
Makefile                 desktop-assets, desktop, desktop-test targets
```

`internal/client` and `internal/keystore` import no Wails, so all logic is testable without a window.

Bound service surface (test asserts this exact list, W1):
`Servers`, `Pair`, `Forget`, `Unlock`, `Sessions`, `Attach`, `Detach`, `Send`, `Paste`, `Resize`, `SetClipboard`, `Ls`, `Download`, `Upload`.
Deliberately absent: anything that signs, returns key material, takes a filesystem path from JS, or returns a raw Authorization header.

Events Go -> JS: `term:data` `{server,session,b64}` (coalesced ~8 ms, chunks <= 48 KB), `term:state` `{server,session,state}`, `xfer:progress`.

## Stage 0: Spike (gates everything)
**Goal**: Prove the unknowns in a throwaway `desktop/` skeleton before writing real code.
**Success Criteria**: each answered yes/no and recorded at the bottom of this file:
- builds and runs on this box with `-tags gtk3`; note the exact Wails tag used
- vanilla frontend (no bundler) can `import` generated bindings and call a bound method
- xterm.js + fit addon + the bundled font render under the strict CSP (meta or asset-middleware header)
- a `Terminal.onData` keystroke round-trips to Go; a Go event reaches JS
- Ctrl+Shift+C/V work in WebKitGTK; if not, plan Go-side clipboard read (adds one exported method, update whitelist)
- external navigation / `window.open` / `<a href>` is blocked (find the Wails hook; if none, record residual risk and rely on CSP `frame-src 'none'; base-uri 'none'; form-action 'none'`)
- DevTools are off in a normal build
**Tests**: none (throwaway); delete the spike code, keep the findings.
**Status**: Complete (findings at the bottom; done against the Wails source and the running app, no throwaway code kept)

## Stage 1: Client core (no UI)
**Goal**: `internal/client` + `internal/keystore` + `internal/config`, fully tested.
**Success Criteria**:
- `auth.go`: `Authorization(key, deviceID, method, host, uri)`; format identical to `agent/server.go` `verify()`; host is `URL.Host` as sent (URLs canonicalised at pairing: default port stripped)
- `api.go`: `Pair`, `Sessions`, `Kill`, `Ls`. `CheckRedirect` -> `ErrUseLastResponse`; 1 MB body cap; https-or-loopback rule; timeouts match Android (15 s connect, 60 s read)
- `conn.go`: one session over WebSocket; auth header on dial; cols/rows in query; backoff `min(15 s, 500ms << min(attempt,5))`; 20 s ping; explicit `SetReadLimit` (4 MB); `resize`, `exit` handling; `Kick`; `Close`; session name regex `^[A-Za-z0-9_-]{1,32}$`
- `keystore`: create P-256 key, PKIX pubkey base64 for pairing, keyring mode and passphrase mode, file mode 0600, wrong passphrase fails, keyring error surfaces (no fallback)
- `config`: servers.json round-trip, corrupt file -> empty list + no crash
**Tests** (write first):
- auth header parses with the agent's regex; fields/lengths within the agent's limits (nonce 8-64)
- api against `httptest`: redirect not followed, oversize body truncated/errors, http non-loopback rejected
- conn against a fake gorilla server: reconnect + backoff, frame over read limit closes, `exit` stops retries, `Close` stops everything
- keystore: passphrase round-trip, tamper fails, keyring fake returns error -> `Create` errors
**Closes**: W3, W6, W7 (documented), D1, D6
**Status**: Complete (`go test -race` passes for client, keystore, config)

## Stage 2: End-to-end against the real agent
**Goal**: prove wire compatibility, not just self-consistency.
**Success Criteria**: `internal/e2e` builds `../agent` (`go build` with `Dir: ../../agent`), runs `serve --data <tmp> --root <tmp> --listen 127.0.0.1:<free>` with a private `TMUX_TMPDIR`, gets a code from `pair --url https://x --data <tmp>` output (`Code:` line), then: pair -> `Sessions` -> attach -> send `echo hi\r` -> receive `hi` -> resize -> kill; `revoke` the device -> live connection closes within ~7 s (agent revokeInterval is 5 s).
**Tests**: the above is the test. Skips without tmux or with `-short`.
**Status**: Complete (also covers upload/409/overwrite/download/ls-outside-root; the agent flag is `--files-root`, not `--root`)

## Stage 3: Service + frontend
**Goal**: usable app: pair, tabs, terminal, reconnect, zoom.
**Success Criteria**:
- `service.go` wraps the controller: per-server session list, tab open/close (detach vs kill), keyed by `(server, session)`; `Send`/`Resize` are the hot path
- `frontend`: pair screen (URL, code, name, key-storage choice, link paste parses to fields), server switcher, tab bar (+ / x / kill confirm), one `Terminal` per tab kept alive while hidden, connection-state badge, `Exited` banner, Ctrl+= / Ctrl+- zoom, unlock-passphrase prompt
- keys: xterm `onData` (string) -> UTF-8 -> base64 -> `Send`; `onBinary` -> `Send`; resize via fit addon -> `Resize`
- `terminal.html` logic reused from `app/app/src/main/assets/terminal.html` (fit, font-load re-measure, OSC handler shape, mode reporting) minus touch code and `Android.*`
**Tests**: bound-surface whitelist test (reflect over the service's exported methods; fails on any addition/removal); service tests with a fake controller for tab bookkeeping (add/close/select-next, invalid names rejected).
**Verify**: manual run against a real agent (checklist in Stage 6).
**Status**: Complete. Deviations: tab bookkeeping lives in the page (Go only holds connections), so there is no Go tab test; no generated bindings, the page uses `Call.ByName('main.Service.<Method>')`. Run under Xvfb+xfwm4+dbus: pairing, passphrase unlock (wrong then right), tabs, terminal I/O, detach dialog all worked.

## Stage 4: Hardening gate
**Goal**: close the review items in the UI/service layer.
**Success Criteria** (all required):
- CSP enforced: `default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self'; img-src 'self' data:; connect-src 'self'; frame-src 'none'; base-uri 'none'; form-action 'none'` (unsafe-inline for styles is required by xterm.js; scripts stay strict); no remote loads anywhere (W2)
- navigation locked per Stage 0 finding (W2, D8); the terminal link handler is off (no web-links addon) so no link opening in v1
- OSC 52: JS parses, shows a confirm overlay with length + first 300 chars; only Confirm calls `SetClipboard` (Go caps 1 MB); Deny/concurrent request = no write (W4, D3). Linux PRIMARY is not touched.
- Paste: `Paste` in Go strips ESC, normalises CRLF->CR, wraps in `ESC[200~..ESC[201~` only when JS reports bracketed mode; JS shows a confirm when text is multi-line and bracketed mode is off (D5)
- `SetClipboard` from a user copy action (selection + Ctrl+Shift+C) needs no confirm
- release build: DevTools off, no dev URL, no server mode; embed only `frontend/` output via `//go:embed all:frontend` (W10)
- no logging of Authorization, signatures, or terminal bytes; errors shown to the user carry no key material (D12)
- app config dir 0700, servers.json 0600
**Tests**: `Paste` sanitiser table test (ESC, `ESC[201~`, CRLF, empty, huge); `SetClipboard` cap; config perms; a test that `main.go`'s window options have DevTools disabled if the API allows reading them (else a CI grep for the debug tag).
**Status**: Complete with one residual: Wails v3 beta.26 has no navigation-policy hook, so navigation is not blocked beyond CSP (`frame-src`/`form-action`/`base-uri`), and a file dropped on the window is `preventDefault`ed. DevTools: explicit `DevToolsEnabled: false` plus the `production` tag; there is no automated check that reads it back. Checked in the running app: OSC 52 confirm (Deny keeps the clipboard, Copy sets it), paste sanitiser (ESC and ^C removed). Not exercised: the multi-line paste confirm (tmux turns bracketed paste on for the outer terminal, so the condition rarely holds).

## Stage 5: File transfer (cut here for an MVP)
**Goal**: browse, download, upload, all path handling in Go.
**Success Criteria**:
- `Ls(server, path)` returns entries; JS never supplies a local path
- `Download(server, remotePath)`: Go opens the native SaveFile dialog itself, uses only `filepath.Base` of the suggested name, rejects separators/NUL/`..`/empty/Windows reserved names and trailing dots, creates with `O_CREATE|O_EXCL`, mode 0600, no symlink follow, never silent overwrite (W5, D4)
- `Upload(server, remoteDir)`: Go opens the native OpenFile dialog, streams with a length, handles 409 (ask overwrite) and the agent's size cap error
- progress via `xfer:progress`; cancel supported
**Tests**: `safeName` table (../, absolute, NUL, `CON`, `nul.txt`, `a.`, unicode, 300-char name); create-exclusive refuses an existing file and a symlink; upload 409 path; e2e download/upload round trip against the real agent.
**Status**: Complete except the native dialogs: `Download` (save dialog) and `Upload` (open dialog, overwrite question) were not driven in the GUI. Listing works, and a hostile file name renders as inert text. Deviation: no cancel button for transfers (would be a 15th bound method).

## Stage 6: Build, CI, docs
**Goal**: reproducible build and a documented, checked release candidate (no release wiring).
**Success Criteria**:
- `Makefile`: `desktop-assets` (copies `xterm.js`, `xterm.css`, `addon-fit.js`, font, licences from `app/app/src/main/assets/` into `desktop/frontend/vendor/`; runs `go run github.com/wailsapp/wails/v3/cmd/wails3@<tag> generate bindings`), `desktop` (`CGO_ENABLED=1 go build -tags gtk3 -trimpath -ldflags "-s -w -X main.version=$(VERSION)"` into `dist/`), `desktop-test` (`go vet` + `go test`, plus e2e). `make help` lists them.
- `.gitignore`: `desktop/frontend/vendor/`, `desktop/frontend/bindings/`, built binary
- CI job `desktop` on ubuntu: install `libgtk-3-dev libwebkit2gtk-4.1-dev tmux`, `make desktop-test desktop`, `govulncheck ./...`; macOS + Windows jobs build only; Actions pinned to SHAs like the existing jobs
- `README.md`: a short Desktop section (install deps, build, pair, key-storage modes, what it does not do)
- `SECURITY_ANALYSIS.md`: desktop section with W1-W12/D1-D12 status; `CLAUDE.md`: desktop commands + gotchas (name-coupled strings unchanged: same `muxalot-v1`)
- delete this file (kept: items below are still open)
**Manual checklist** (run before calling it done): pair with a real agent over https; two tabs; reconnect after network drop (`kick`); `cat` a file containing an OSC 52 sequence -> overlay, Deny leaves clipboard; paste text containing `ESC[201~` -> stripped; multi-line paste confirm; revoke the device -> terminal closes; wrong passphrase; no keyring available (run without a Secret Service) -> clear error; download `../x` name from a hostile fake server -> refused.
**Status**: Mostly complete. Done: Makefile targets (`desktop-assets`, `desktop`, `desktop-test`), `.gitignore`, a Linux CI job (tests, build, `govulncheck`), README, SECURITY_ANALYSIS and CLAUDE.md updates. Not done: macOS and Windows CI jobs (cannot be verified from here, so none were added); the manual checklist items below that are not in the notes above (real https pairing, reconnect after a network drop through the UI, revoke seen in the UI (covered at client level by the e2e test), no-Secret-Service error path in the UI, hostile download name through the save dialog).

## Risks / unknowns
- Wails v3 is beta: API names above (navigation hook, DevTools flag, dialogs, clipboard) come from docs I have not run. Stage 0 exists to catch this; if navigation control is missing, record the residual risk in SECURITY_ANALYSIS.md and lean on CSP.
- GTK3/WebKitGTK 4.1 is the legacy path in Wails v3 (default is GTK4/WebKitGTK 6.0). Fine for now; revisit if distros drop 4.1.
- go-keyring on macOS may pass secrets via the `security` CLI arguments (low confidence). Verify in Stage 1 before enabling keyring mode there.
- Clock skew (60 s) is the same failure mode as on the phone; surface "check your system clock" on 404s that follow a valid pairing.
- Go cannot zeroize `*ecdsa.PrivateKey` reliably (W7); documented, not fixed.

## Stage 0 findings (Wails v3.0.0-beta.26, GTK3/WebKitGTK 4.1, this box)
- Builds and runs with `-tags gtk3`; production binary `-tags "production gtk3"` is ~13 MB. Bare Xvfb only gives a 10x10 stub window; it needs `dbus-run-session` and a window manager (xfwm4) to show the real window.
- Vanilla page: `import { Call, Events } from '/wails/runtime.js'` works; `Call.ByName('main.Service.<Method>', ...args)` calls a bound method (FQN is `<package path>.<Type>.<Method>`); Go `app.Event.Emit(name, payload)` arrives as `event.data`. No CLI or generated bindings needed.
- xterm.js, fit addon and the bundled font render under the strict CSP set through `AssetOptions.Middleware`.
- Keystrokes round-trip to Go and Go events reach JS (terminal I/O verified live).
- Ctrl+Shift+V reaches the page as a DOM `paste` event, so no Go-side clipboard read is needed. xterm.js listens for paste on its own textarea; the page listens in the capture phase on the parent so it always runs first.
- Navigation: no hook in `WebviewWindowOptions`/`Options`. Residual risk recorded in SECURITY_ANALYSIS.md.
- DevTools default to on in builds without the `production` tag; the release build uses the tag and also sets `DevToolsEnabled: false`. A production build ignores the dev-server URL override.
- Unexpected: tmux enables bracketed paste on the outer terminal, so the "confirm multi-line paste when bracketed mode is off" rule rarely triggers under tmux.
- Unexpected: xterm.js answers terminal queries into the shell once `onData` is wired (X1 in SECURITY_ANALYSIS.md).
- Found on a real desktop (Zorin OS 18.1 / Ubuntu 24.04, GNOME Wayland, Intel HD 630, WebKitGTK 2.52.6): the window never loaded because the `fonts-opendyslexic` package installs `/usr/share/fonts/woff/opendyslexic/*.woff`; fontconfig then returns one of them as the best match for every family (even installed ones), and the WebKitWebProcess main thread spins at 100% CPU. Not the sandbox (userns restriction off, no AppArmor denials), not the GPU (Mesa fine; `WEBKIT_DISABLE_DMABUF_RENDERER`/`COMPOSITING_MODE` made no difference), not Wayland. Fix: `fonts.go` points `FONTCONFIG_FILE` at a generated config that includes the user's own and rejects `/*.woff` and `/*.woff2`. A relative glob such as `*.woff` silently matches nothing because fontconfig resolves it against the config file's directory. Verified on that machine: web process idle, no watchdog, and only the 4 WOFF faces hidden (3152 -> 3148 fonts).
- Added: launch-time preflight (graphical session, D-Bus), `--check`, and a 20 s watchdog on the page's runtime-ready event (`preflight.go`).

## Commit plan
One commit per stage, terse bullets, no AI attribution, run `gofmt`/`go vet` first; never `--no-verify`. Committed in two steps: the client (`desktop/`), then build, CI and docs.
