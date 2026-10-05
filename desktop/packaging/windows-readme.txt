Muxalot desktop — Windows (amd64)
=================================

Requirements: Windows 10 21H2 or newer, or Windows 11. The app renders with
Microsoft's WebView2 runtime, which those systems ship preinstalled. Windows 10
without it: install it once from
https://developer.microsoft.com/en-us/microsoft-edge/webview2/

Run: unzip this file anywhere (no installer, no admin rights) and double-click
muxalot-desktop.exe.

Pair with a server: on the server run

    muxalot-agent pair --url https://your.host

then enter the shown URL and code (or paste the muxalot://pair link) in the app.

Notes:
- The build is not code-signed, so Windows SmartScreen may ask "Do you want to
  keep" on first run: choose "More info" -> "Run anyway". The release page has
  a build-provenance attestation you can verify with `gh attestation verify`.
- This device's key is stored in Windows Credential Manager, never on the
  server. Revoke a device on the server with `muxalot-agent revoke <device>`.