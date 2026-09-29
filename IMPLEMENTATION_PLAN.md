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

## Release integration: requirements (brainstormed, not designed or built)

**Goal**: every `v*` tag also publishes a desktop `.deb`, attested like the agent, and only after it has been installed and launched on each supported distro.

**Decisions made**
- Linux amd64 only for the first release (the only platform ever built and run). macOS, Windows and arm64 are out of scope.
- Artifact: a `.deb` (menu entry, declared dependencies), no bare binary.
- Supported: Ubuntu and Debian 12+. Only Ubuntu 24.04 (WebKitGTK 2.52) has ever run the app.
- Gate: desktop tests plus an install-and-launch smoke test per distro; publishing is blocked if any fails.
- Ordering: the agent job stays exactly as is and creates the release; a follow-up `desktop` job (`needs: agent`) uploads to it. A desktop failure leaves a release without the `.deb` until the job is re-run.

**Functional requirements**
- F1. A tag push builds `muxalot-desktop_<version>_amd64.deb`; the version comes from the tag, embedded with `-X main.version`, build tags `production gtk3`, Go from `desktop/go.mod`.
- F2. Build on the oldest supported glibc (Ubuntu 22.04 base: 2.35) so the binary also runs on Debian 12+ and Ubuntu 24.04. Building on `ubuntu-latest` (24.04) is not acceptable.
- F3. Package contents: `/usr/bin/muxalot-desktop`, a `.desktop` launcher, an icon from `assets/`, and licence texts (MIT, xterm.js, JetBrainsMono OFL). No maintainer scripts, nothing setuid.
- F4. Dependencies cover GTK3 and WebKitGTK 4.1 across the t64 rename (`libgtk-3-0t64 | libgtk-3-0`, `libwebkit2gtk-4.1-0`); a Secret Service provider (for example gnome-keyring) is recommended, not required, because passphrase mode works without it.
- F5. Gate before upload: `make desktop-test`; then, for Ubuntu 22.04, 24.04, 26.04, Debian 12 and Debian 13 (containers): `apt install ./x.deb` resolves cleanly, `muxalot-desktop --check` passes under a virtual display and session bus, and the app launches and the page loads (no watchdog message) within a fixed time.
- F6. Provenance: attest the `.deb` with `attest-build-provenance`; publish checksums in a separate `SHA256SUMS-desktop` so the agent's `SHA256SUMS` (rewritten by `make agent`, uploaded with `--clobber`) is never touched.
- F7. Upload with `gh release upload "$GITHUB_REF_NAME" ... --clobber` so the job can be re-run.
- F8. Docs: README install and verify steps (`gh attestation verify`, `sudo apt install ./file.deb`), SECURITY_ANALYSIS M3 note (the `.deb` is attested; there is no apt repository or package signing key).
- F9. `.github/dependabot.yml` gets a `gomod` entry for `/desktop`; new Actions pinned to commit SHAs like the existing ones.
- F10. A `make deb` target builds the same package locally for testing, using the same script the workflow runs.

**Non-functional**: no new secrets; agent release path and `install.sh` unchanged; a desktop failure never removes or delays agent assets; matrix runs in parallel.

**Acceptance criteria**
- A1. A `v*` tag produces a release containing the agent assets (unchanged) plus the `.deb` and `SHA256SUMS-desktop`; `gh attestation verify` passes for the `.deb`.
- A2. On all five distros the `.deb` installs with no manual dependency fixes and the app's page loads under the smoke test.
- A3. `dpkg -c` lists exactly the files in F3.
- A4. Breaking the desktop build or one distro's smoke test blocks the `.deb` upload and leaves the agent release intact.

**Risks and unknowns**
- WebKitGTK on Ubuntu 22.04 and Debian 12/13 has never run this app with Wails `v3.0.0-beta.26`; the smoke matrix may show a distro cannot be supported, in which case drop it from the claim.
- The smoke test cannot reproduce desktop-specific failures like the WOFF-font hang or Wayland/GPU issues; it proves basic loading only.
- The Ubuntu 22.04 runner image may be retired by GitHub (timing unverified); the fallback is a `debian:12` or `ubuntu:22.04` container.
- A GUI smoke test in bare containers needs Xvfb, a session bus and possibly a window manager (bare Xvfb produced a stub window earlier); the exact rig and a machine-readable pass signal (exit code) are design work.

**Open questions**
1. Package maintainer and copyright fields: what public identity goes there (the repo is public)?
2. Icon and menu category: `assets/logo.svg` or `favicon.svg`?
3. Matrix: exactly Ubuntu 22.04, 24.04, Debian 12, 13, or also Ubuntu 26.04?
4. Default assumed: no `make release-desktop` local upload (local builds are unattested and have the wrong glibc floor). Agree?
5. Build the package by hand with `dpkg-deb` (default, no extra tool) or use nfpm?

Next step: `/sc:design` for the workflow and package layout, then `/sc:workflow` to implement.

## Release integration: design

Design only; no repo files exist for this yet. Facts marked *verified* come from a throwaway prototype (container build, hand-built `.deb`, install and launch in four containers); everything else is a decision.

### Job graph (`.github/workflows/release.yml`, trigger unchanged: `v*` tag)

```
agent (unchanged: build, attest, gh release create)
desktop-build  (ubuntu-22.04 runner)        runs in parallel with agent
      |
desktop-smoke  (matrix: ubuntu:22.04, ubuntu:24.04, ubuntu:26.04, debian:12, debian:13)
      |
desktop-publish  needs: [agent, desktop-smoke]   attest, checksum, gh release upload --clobber
```

- **desktop-build**: checkout; `setup-go` from `desktop/go.mod`; apt `tmux libgtk-3-dev libwebkit2gtk-4.1-dev dpkg-dev fakeroot`; `make desktop-test`; `make deb` with the version taken from the tag; upload the `.deb` as a workflow artifact and expose its sha256 as a job output. Job permissions `contents: read` only.
- **desktop-smoke**: download the artifact, verify the sha256, run `desktop/packaging/smoke.sh` inside the matrix image (`docker run`), `contents: read` only. The matrix is the single source of truth for the supported-distro list in the README.
- **desktop-publish**: download the artifact, re-check the sha256, `attest-build-provenance` on the `.deb`, write `SHA256SUMS-desktop`, `gh release upload "$GITHUB_REF_NAME" <deb> SHA256SUMS-desktop --clobber`. Only this job gets `contents: write`, `id-token: write`, `attestations: write`. It waits for `agent`, so the release exists, and re-running a failed job is safe.
- The `agent` job and the workflow-level permissions stay untouched; new jobs narrow permissions per job. New actions pinned to commit SHAs like the existing ones. `download-artifact` and `upload-artifact` join the pinned set.
- A desktop failure never affects the agent release (decision: follow-up job, not atomic).
- `ci.yml` also runs `make deb` (no smoke) on PRs and main so packaging breakage shows up before a tag.

### Build environment

- **ubuntu-22.04 runner** (glibc 2.35) builds the binary; *verified* in an `ubuntu:22.04` container: builds with WebKitGTK dev 2.50.4, needs at most `GLIBC_2.34`, so it runs on Ubuntu 22.04+ and Debian 12+.
- Fallback if GitHub retires that runner image: run the same build inside an `ubuntu:22.04` container step (the prototype did exactly this with the host Go toolchain mounted).
- `make deb` locally builds on whatever the host is, so a plain local `.deb` is for packaging tests only; `release-desktop` (below) builds in an `ubuntu:22.04` container instead, so its glibc floor matches CI.

### Package layout (`muxalot-desktop_<version>_amd64.deb`)

```
/usr/bin/muxalot-desktop                                   0755
/usr/share/applications/muxalot-desktop.desktop            0644
/usr/share/icons/hicolor/scalable/apps/muxalot.svg         0644   (from assets/logo.svg)
/usr/share/doc/muxalot-desktop/copyright                   0644   (MIT + third-party notices)
```

- All directories `0755`, owner `root:root`. The prototype produced `0775` directories because of the host umask: the build script must set modes explicitly, not rely on umask.
- No maintainer scripts, nothing setuid, nothing runs as root at install time beyond dpkg itself.
- `control`: `Package: muxalot-desktop`, `Architecture: amd64`, `Section: net`, `Priority: optional`, `Homepage`, `Maintainer` (open question), `Installed-Size` computed, `Recommends: gnome-keyring | kwalletd5 | kwalletd6` (keyring mode only; passphrase mode works without one), short description.
- **Depends is generated, not hand-written**: `dpkg-shlibdeps -O` on the built binary. *Verified* output on 22.04: `libc6 (>= 2.34), libgdk-pixbuf-2.0-0 (>= 2.31.1), libglib2.0-0 (>= 2.33.14), libgtk-3-0 (>= 3.21.5), libjavascriptcoregtk-4.1-0, libsoup-3.0-0 (>= 2.4.0), libwebkit2gtk-4.1-0 (>= 2.39.91), libx11-6`. It resolved through the t64 renames on Ubuntu 24.04 and Debian 13 without edits, because the renamed packages provide the old names.
- Copyright file: the project MIT licence plus the licence texts already vendored (`LICENSE-xterm.txt`, `LICENSE-nerdfonts.txt`) and the Go module licences (Wails MIT, go-keyring MIT, x/crypto BSD-3).
- Version: tag `v0.3.0` -> `0.3.0`; a suffix such as `-rc1` becomes `~rc1` so pre-releases sort before the release; local `-dev` builds become `~dev`.
- **Launcher matching (measured, corrects an earlier note)**: the id GNOME matches windows on is the executable name, not the Wails/GApplication id (`org.wails.muxalot` is only the D-Bus name). With the binary installed as `muxalot-desktop`: native Wayland windows report `app_id = "muxalot-desktop"` (headless `sway` in an Ubuntu 24.04 container, `swaymsg -t get_tree`), and X11/Xwayland windows report `WM_CLASS = "muxalot-desktop", "Muxalot-desktop"` (Xvfb + `xprop`). This agrees with Wails' own documentation (GTK takes the Wayland `app_id` from the program name, which defaults to the executable name).
- Therefore the launcher is `/usr/share/applications/muxalot-desktop.desktop` (file id equals the `app_id`, the most portable match across GNOME, KDE and others) with `StartupWMClass=muxalot-desktop` as the fallback (GNOME compares it case-insensitively, so it also covers `Muxalot-desktop`). Keys: `Type=Application`, `Name=muxalot`, `Comment`, `Exec=muxalot-desktop`, `Icon=muxalot`, `Terminal=false`, `Categories=Network;RemoteAccess;`.
- No code change is needed: leave the Wails `ApplicationID` and `ProgramName` options unset. Setting either would change the `app_id` and break this match, so a comment next to the window options should say so.
- The window itself has no embedded icon; while the app runs from a matched launcher the icon comes from the desktop file. Run from a terminal or an unmatched name, the window shows a generic icon. Embedding a PNG through the Wails `Icon` option is optional and out of scope.
- No maintainer scripts are needed for the icon: GTK falls back to scanning when the hicolor cache is stale, and GNOME watches the applications directory.

### Files added to the repo (implementation will create these)

- `desktop/packaging/build-deb.sh <version> <binary> <outdir>`: stages the tree with explicit modes, runs `dpkg-shlibdeps`, writes `control`, builds with `fakeroot dpkg-deb --root-owner-group`.
- `desktop/packaging/control.in`, `desktop/packaging/muxalot.desktop`, `desktop/packaging/copyright`.
- `desktop/packaging/smoke.sh`: the in-container script below.
- `Makefile`: `deb` (depends on `desktop`), `deb-smoke IMAGE=debian:12` (local `docker run` of the same script), and `release-desktop` (container build, `check-tag`, `desktop-test`, refuses to overwrite an existing `.deb` without `FORCE=1`).
- `.github/workflows/release.yml`, `.github/workflows/ci.yml`, `.github/dependabot.yml` (add `gomod` for `/desktop`).
- README install and verify section; SECURITY_ANALYSIS M3 note; local CLAUDE.md gotchas.

### Smoke test (`smoke.sh`, runs as root inside each matrix image)

1. `apt-get install -y --no-install-recommends ./pkg.deb xvfb xauth dbus dbus-x11`; failure to resolve dependencies fails the job.
2. `ldd /usr/bin/muxalot-desktop` reports no missing libraries.
3. `xvfb-run -a dbus-run-session -- ...`: `muxalot-desktop --check` must print `ok`; then `timeout 30 muxalot-desktop` must still be running when the timeout fires (exit 124). Exit 1 means the load watchdog or startup failed (it fires at 20 s); any other code is a crash. No new app flag is needed.
4. *Verified* on all five images (Ubuntu 22.04, 24.04, 26.04, Debian 12, 13): install clean, no missing libraries, `--check` ok, exit 124. No window manager and no font package were needed; a session bus was.
4b. **Launcher checks** (automated, no compositor needed; adds `desktop-file-utils` and `x11-utils` to the install list; *all verified on the five images*): `desktop-file-validate /usr/share/applications/muxalot-desktop.desktop` exits 0; the file's base name equals `StartupWMClass`; `Exec` resolves in `PATH`; the file named by `Icon=` exists under `/usr/share/icons/hicolor/scalable/apps/`; and **some** top-level window in `xwininfo -root -tree` has a `WM_CLASS` instance equal to `StartupWMClass`. Do not test the first window in the tree: on Ubuntu 22.04 and Debian 12 that is WebKit's own `WebKitWebProcess` helper window, which made a first-match version of this check fail falsely. Because GTK derives the Wayland `app_id` from the same program name, this guards the Wayland match too; that equality was measured once (see the launcher notes above), not in CI.
   - With the final `Categories=Network;RemoteAccess;` the validator prints nothing. It printed a hint (exit 0) for the earlier `...;System;` value. CI should still run it as a check on the exit code, so a future edit that adds an error fails the job.
5. Optional add: also install `fonts-opendyslexic` in the container. It may not reproduce the WOFF hang headless, so it is a cheap regression guard, not proof.
6. On failure, upload the app log as an artifact.

### Supply chain and trust

- The `.deb` is attested; users verify with `gh attestation verify <file>.deb --repo muxalot/muxalot`, and can check `SHA256SUMS-desktop` (corruption only).
- There is no apt repository and no package signing key: trust rests on the attestation and the release page. The README and SECURITY_ANALYSIS must say so.
- `SHA256SUMS` (agent) is never modified; `install.sh` keeps working unchanged (*verified* by reading it: it selects its own file by name).
- The artifact passes between jobs through GitHub's artifact store; the publish job re-hashes it against the build job's output before attesting.

### Not verified by the prototype

- GNOME Shell's own rendering of the launcher. **Confirmed by you on the real desktop** (GNOME/Wayland, Zorin OS 18.1) using a user-level launcher and the logo, started with `gtk-launch muxalot-desktop`: the dock shows one entry with the logo, so the launcher matches the running window. **Not reported, so unconfirmed**: the Activities/Super-search entry, keeping the icon after "Add to Favorites", and the terminal-launch case. Also untested: the packaged install under `/usr/share` (the check used `~/.local/share`), and other desktops (KDE, XFCE). GNOME's introspection API refuses to answer other processes, so this can only be confirmed visually.
- Anything GPU/Wayland/fontconfig-specific: the smoke test proves basic loading under software rendering only.
- `lintian` cleanliness; installing the `Recommends`; upgrade and removal (`apt remove` leaves the per-user config, as expected).
- The actual workflow YAML, artifact hand-off and attestation of a `.deb` on GitHub (needs a fork or a throwaway tag to test).

### Test plan before the first real tag

1. Run the workflow on a fork with a throwaway tag; confirm the three desktop jobs, the release assets, and `gh attestation verify` on the `.deb`.
2. Install the released `.deb` on the workstation (GNOME/Wayland) and check, in this order: the app appears in Activities search as "muxalot" with the logo; launching it from there shows the same icon in the dock; the running window is grouped with that dock entry (one icon, not two); pinning it keeps the icon. If it shows twice or with a generic icon, compare `StartupWMClass`/file name against the measured `app_id` first.
3. Break one thing on purpose (for example a bad dependency) and confirm publish is blocked while the agent assets stay.

### Decisions on the open questions (answered)

1. **Maintainer and copyright**: the project identity, `muxalot <noreply@users.noreply.github.com>`; no personal address in the package.
2. **Icon and category**: `assets/logo.svg`; `Categories=Network;RemoteAccess;`. `System` was dropped after `desktop-file-validate` warned on the real machine that two main categories (`Network` and `System`) mean the application "might appear more than once in the application menu"; the two-category value is what was first chosen. The final value validates with no errors and no hints.
3. **Matrix**: Ubuntu 22.04, 24.04, 26.04, Debian 12, Debian 13. Ubuntu 26.04.1 (glibc 2.43, WebKitGTK 2.52.6) was run through the same prototype and passed; every image in the matrix has now been exercised headless. The README's supported list must match this matrix exactly.
4. **Local upload: yes**, a `make release-desktop` like `release-agent`. Consequences the implementation must handle:
   - Build the `.deb` inside an `ubuntu:22.04` container, not on the host, so the glibc floor is right (the prototype's container build already works; the Go toolchain must come from inside the image or a read-only mount). Refuse to run without docker.
   - Same guards as the other local releases: `check-tag` (HEAD must be exactly the latest tag, release must exist) and `desktop-test`.
   - It is unattested, so `gh attestation verify` fails for that file. Unlike `release-agent`, it must **not** silently replace a CI-attested `.deb`: refuse if the release already has a `.deb` asset unless `FORCE=1`, and print the attestation caveat.
   - It regenerates and uploads `SHA256SUMS-desktop` only; the agent's `SHA256SUMS` is never touched.
   - The README says local uploads fail verification, as it already does for the agent.
5. **Packaging tool**: hand-rolled `dpkg-deb` with `dpkg-shlibdeps` (as prototyped); nfpm is not used.

### Implementation status (built)

Built: `desktop/packaging/` (`build-deb.sh`, `smoke.sh`, `container-build.sh`, `muxalot-desktop.desktop`); Makefile `deb`, `deb-smoke`, `release-desktop`; `release.yml` jobs `desktop-build`, `desktop-smoke` (5-image matrix), `desktop-publish`; `ci.yml` packaging dry run and launcher validation; Dependabot `gomod /desktop`; README, SECURITY_ANALYSIS and CLAUDE.md notes; a comment in `desktop/main.go` not to set the Wails application id or program name.

Deviations from the design, on purpose: no `control.in` or `copyright` template files (the control file and copyright text are produced inside `build-deb.sh`); an extra `container-build.sh` for the local release path; the smoke test asserts the app is still running at 26 s (the load watchdog exits at 20 s) instead of `timeout 30`.

Verified locally on the dev machine:
- `make deb` produces the expected package: exactly the planned files, `root:root`, all directories `0755`, generated `Depends`.
- The `ubuntu:22.04` container build (what `release-desktop` runs) works in about 2 minutes and hands file ownership back. The package built there has the old-style dependency names (`libgtk-3-0`, `libglib2.0-0`); one built on 24.04 has `t64` names that do not exist on 22.04 and Debian 12, so a host-built `.deb` is for 24.04+ testing only.
- `make deb-smoke` passes on Ubuntu 22.04, 24.04, 26.04 and Debian 12, 13, and fails with exit 1 and the right message when the launcher class is wrong.
- `actionlint` is clean on both workflows; action SHAs verified to be commits (lightweight tags).

Not verified, needs GitHub or a real tag: the workflow run itself (artifact hand-off, sha256 check, `attest-build-provenance` on a `.deb`, `gh release upload` from a job without a checkout); the `release-desktop` upload and its `FORCE` guard (needs a real tag and release); the first release's smoke matrix on GitHub's runners; the packaged install under `/usr/share` on the real GNOME desktop (only a user-level launcher was checked there: one dock icon with the logo).

Test before the first real tag: run the workflow on a fork with a throwaway tag (see the test plan above).

## Commit plan
One commit per stage, terse bullets, no AI attribution, run `gofmt`/`go vet` first; never `--no-verify`. Committed in two steps: the client (`desktop/`), then build, CI and docs.
