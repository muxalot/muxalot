<p align="center"><img src="assets/banner.svg" alt="Muxalot" width="600"></p>

Open source under the [MIT License](LICENSE). Source and issues: <https://github.com/muxalot/muxalot>

Remote terminal for Android, streamed from a Linux server. Sessions are tmux sessions, so they survive disconnects; each tab is one session.

```
Android app (Compose)                         Linux server
 xterm.js (render) + native key capture  <->  Caddy (TLS) -> muxalot-agent (Go, 127.0.0.1:8787) -> PTY -> tmux
```

## Status

| Part | State |
|---|---|
| `agent/` | Built and tested (`go test ./...`): pairing, signed-request auth, replay/forgery rejection, rate limiting, path-traversal/symlink checks, upload limits, tmux persistence across a dropped connection, clipboard. |
| `app/` | Working Android app: terminal, tabs, clipboard, files, multi-server. No automated tests yet. |

## Auth: public/private keys

- On pairing, the phone generates an **ECDSA P-256 key pair in the Android Keystore** (the private key never leaves the device). Only the public key is sent to the server.
- The agent stores public keys only (`state.json`). There is no bearer token or password to steal.
- Every request (including the WebSocket upgrade) carries
  `Authorization: Sig id="<device>", ts="<unix>", nonce="<b64url>", sig="<b64 DER>"`
  signed over `"muxalot-v1\nMETHOD\nHOST\nREQUEST_URI\nts\nnonce"`.
  Timestamps must be within 60 s and each nonce works once, so captured headers can't be replayed. The phone clock must be roughly correct.
- Pairing codes are single use, expire in 10 minutes, and failed attempts are rate limited per IP (10 per 15 min). Unauthenticated requests get a bare 404.
- Revoke a lost phone: `muxalot-agent revoke <id>`. Admins can also register a key directly: `muxalot-agent add-key --name laptop @pubkey.b64` (base64 X.509 SPKI, P-256).

## Server setup

Requires Linux with `tmux` and a domain for TLS. Install the latest release (verifies its checksum, creates the `muxalot-agent` user, installs and starts the systemd unit):

```sh
curl -fsSL https://raw.githubusercontent.com/muxalot/muxalot/main/deploy/install.sh | sudo sh
muxalot-agent proxy --type caddy --domain tty.example.com    # or nginx, apache: prints a reverse-proxy snippet
sudo -u muxalot-agent muxalot-agent pair --url https://tty.example.com     # QR + one-time code
```

To build from source instead: `git clone https://github.com/muxalot/muxalot && cd muxalot/agent && go build -o muxalot-agent . && sudo ./muxalot-agent install`.

Any TLS reverse proxy works (Caddy, nginx, Apache). It must pass WebSocket upgrades, must not rewrite or strip the request path (the signature covers it), must send `X-Forwarded-For`, and must not buffer file streams or cap uploads too low. `muxalot-agent proxy` prints a snippet that does all of this.

### Install options

`install` (and `install.sh`, which passes its arguments on) accepts:

| Flag | Default | Meaning |
|---|---|---|
| `--user` | asked on the terminal; default is the user who ran `sudo`, else `muxalot-agent` | Unix user the agent and its terminals run as. Created if missing. `root` is rejected |
| `--listen` | `127.0.0.1:8787` | Address the agent binds |
| `--files-root` | that user's home | Directory tree exposed to file upload and download |

```sh
curl -fsSL https://raw.githubusercontent.com/muxalot/muxalot/main/deploy/install.sh | sudo sh -s -- --user alice --listen 192.168.1.10:8788
```

- **Which user:** the default dedicated user has an empty home and its own tmux, which is the most contained choice. To control your own machine (your tmux sessions, projects and shell), install with `--user <you>`. The agent still refuses to run as root.
- **Proxy in Docker or on another host:** loopback is only reachable by a proxy on the same host and network namespace. If your proxy runs in a container or elsewhere, bind a LAN address with `--listen` and point the proxy at it. Restrict that port with a firewall to the proxy only. The agent trusts `X-Forwarded-For` only from loopback, so behind a non-loopback proxy the per-IP pairing rate limit is shared by all clients.
- **Pairing:** run `pair` as the service user, for example `muxalot-agent pair --url https://your.host` when that is you, or `sudo -u muxalot-agent muxalot-agent pair …` for the default user. It must be the same user, because pairing state is stored in that user's config directory.
- **Environment:** tmux starts sessions as login shells, so your `~/.profile` and `~/.bashrc` apply and your `PATH` additions are there. If a tmux server for that user is already running, new sessions inherit its environment. Only when the service itself starts the tmux server do sessions lack desktop-session variables such as `XDG_RUNTIME_DIR`, `DBUS_SESSION_BUS_ADDRESS` (so `systemctl --user` fails) and `SSH_AUTH_SOCK`.

The bundled `deploy/Caddyfile` reads these environment variables:

| Variable | Meaning |
|---|---|
| `MUXALOT_DOMAIN` | Public hostname Caddy serves, e.g. `tty.example.com` |
| `MUXALOT_AGENT` | Agent address to proxy to, e.g. `127.0.0.1:8787` (must match the agent's `--listen`) |
| `CLOUDFLARE_API_TOKEN` | Cloudflare API token for DNS-01 TLS. Only needed with the bundled `(cloudflare)` snippet; use your own `tls` block otherwise |

Recommended `~muxalot-agent/.tmux.conf`: `set -g mouse on` (swipe-to-scroll in the app maps to tmux wheel events). The agent sets `set-clipboard on` itself.

Files: uploads/downloads are confined to `--files-root` (default: the agent user's home), symlink-safe, size-capped by `--max-upload-mb`.

## Android app

Open `app/` in Android Studio (the Gradle wrapper isn't included; Studio creates one, or run `gradle wrapper`). minSdk 26.

- **Terminal**: xterm.js (MIT, bundled in `assets/`) renders; a native invisible view owns the soft keyboard (no suggestions or autocorrect), so vi, Ctrl-C etc. behave. Extra-keys row: Esc, Tab, sticky Ctrl/Alt, arrows (auto-repeat), ^C ^D ^Z, `| ~ / - _`, Home/End/PgUp/PgDn/Del, F1-F12. Hardware keyboards work.
- **Tabs**: one per tmux session. `+ Tab` creates or attaches; closing offers Detach (keeps running) or Kill.
- **Reconnect**: exponential backoff, plus an immediate retry when the network changes. Connections only live while the app process is alive; tmux makes the reattach seamless.
- **Clipboard**: OSC 52 from tmux/vim goes to the phone clipboard; long-press-drag selects and copies; the menu has Paste, "Send clipboard to server" (tmux buffer) and "Copy server clipboard".
- **Files**: browse, upload (system picker), download (system save dialog).
- **Multi-server**: saved list, each server with its own Keystore key.

## Wire protocol (WebSocket `/ws?session=NAME&cols=N&rows=N`)

Binary frames are raw terminal bytes both ways. Text frames are JSON: client `{"t":"resize","cols","rows"}`, `{"t":"clip_set","text"}`, `{"t":"clip_get"}`, `{"t":"ping"}`; server `{"t":"clip","text"}`, `{"t":"pong"}`, `{"t":"exit"}`.
REST (all signed): `GET /sessions`, `DELETE /sessions/{name}`, `GET /ls?path=`, `GET /files?path=` (Range supported), `PUT /files?path=[&overwrite=1]`. Unsigned: `POST /pair {code,name,pubkey}`.

## Support the project

Muxalot is free and stays free. If it saves you time, you can support development with GitHub Sponsors or with the supporter edition ("Muxalot Pro") on Google Play once it's listed. The pro build comes from the same source in this repo (`pro` flavor: `gradle :app:assembleProDebug`) and adds shortcut export/import, terminal color themes, and multi-file upload with progress bars. Everything else is identical to the free build.

## License

muxalot is released under the [MIT License](LICENSE). Contributions are welcome, see [CONTRIBUTING.md](CONTRIBUTING.md).

Third-party components: Termux's terminal libraries were **not** used: `TerminalSession` is `final` and bound to a local process, and the Termux repo is GPLv3-only. xterm.js is MIT, OkHttp and zxing-android-embedded are Apache-2.0. Go deps: gorilla/websocket (BSD-2), creack/pty (MIT), go-qrcode (MIT).

## Known limitations / next steps

- Upload/download bodies are protected by TLS and the signed request line, but the body itself isn't signed.
- The server certificate is validated against the system CA store (no pinning), which suits Caddy with Let's Encrypt.
- Not yet done: background keep-alive service, scrollback search, pinch-zoom, Play Store polish (icon, onboarding).
