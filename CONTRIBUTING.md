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

## Building and releasing

`make help` lists the targets: `make test`, `make agent` (linux amd64 and arm64 into `dist/`), `make apk` (signed free APK), `make aab` (signed pro bundle for Play), `make version`. Pass `GRADLE=/path/to/gradle` if Gradle 8.10+ isn't at the default wrapper path, and `GRADLE_FLAGS=--offline` to avoid network access.

The version comes from git tags only (`vX.Y.Z`). Maintainers release with `make release` (bumps patch; `BUMP=minor|major`; `DRY=1` to preview), which tags and pushes. CI then publishes the agent binaries. `make release-apk` and `make release-agent` upload a locally built artifact to the latest release, and only run with that tag checked out.

Signing keys stay on the maintainer's machine. Create them once and describe them in `app/keystore.properties` (gitignored):

```sh
keytool -genkeypair -keystore ~/keystores/muxalot-apk.jks -alias apk -keyalg RSA -keysize 4096 -validity 10000
keytool -genkeypair -keystore ~/keystores/muxalot-play.jks -alias play -keyalg RSA -keysize 4096 -validity 10000
```
Use RSA (Play accepts RSA keys everywhere) and a validity of 10000 days (about 27 years). Keep the keystores outside the repo.
```properties
apk.storeFile=/home/you/keystores/muxalot-apk.jks
apk.storePassword=...
apk.keyAlias=apk
apk.keyPassword=...
play.storeFile=/home/you/keystores/muxalot-play.jks
play.storePassword=...
play.keyAlias=play
play.keyPassword=...
```
`apk` signs the GitHub APK; `play` is the Play upload key (Play App Signing holds the real signing key). Back up the APK key: losing it means installed users can't upgrade in place.

## Pull requests

- Keep changes small and focused; one concern per PR.
- Add or update tests for agent changes. Fix a bug with a test that fails first.
- Match the surrounding code style. Don't reformat unrelated code.
- The agent and app share a wire protocol (the `muxalot-v1` request signature, the `muxalot://pair` link, REST and WebSocket messages). Changes to it must update both sides and the README.
- Never commit secrets, tokens, `.env` files or keys.
- Commit messages: short, say what changed and why.

## Security

Auth bugs (signature checks, replay protection, pairing, path confinement) are sensitive. Report them privately through GitHub's "Report a vulnerability" on the repo's Security tab, not in a public issue.
