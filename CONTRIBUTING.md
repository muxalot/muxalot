# Contributing to muxalot

Issues and pull requests are welcome at <https://github.com/muxalot/muxalot>. By contributing you agree your work is released under the [MIT License](LICENSE).

## Layout

- `agent/`: Go server (PTY, tmux, signed-request auth, files)
- `app/`: Android app (Kotlin, Jetpack Compose, xterm.js in a WebView)
- `deploy/`: systemd unit and Caddyfile

## Agent (Go)

```sh
cd agent && go test ./...
```

Tmux tests use a private `TMUX_TMPDIR` and are skipped if `tmux` isn't installed. Run `gofmt` before committing.

## App (Android)

Open `app/` in Android Studio, or build from the command line with Gradle 8.10+ (`gradle :app:assembleFreeDebug`; the `pro` flavor is the Play supporter build from the same source). minSdk 26. There is no Gradle wrapper in the repo.

## Pull requests

- Keep changes small and focused; one concern per PR.
- Add or update tests for agent changes. Fix a bug with a test that fails first.
- Match the surrounding code style. Don't reformat unrelated code.
- The agent and app share a wire protocol (the `muxalot-v1` request signature, the `muxalot://pair` link, REST and WebSocket messages). Changes to it must update both sides and the README.
- Never commit secrets, tokens, `.env` files or keys.
- Commit messages: short, say what changed and why.

## Security

Auth bugs (signature checks, replay protection, pairing, path confinement) are sensitive. Report them privately through GitHub's "Report a vulnerability" on the repo's Security tab, not in a public issue.
