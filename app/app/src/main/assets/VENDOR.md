# Vendored terminal assets

| File | Source | Version |
|---|---|---|
| `xterm.js`, `xterm.css` | npm `@xterm/xterm` (`lib/xterm.js`, `css/xterm.css`) | 6.0.0 |
| `addon-fit.js` | npm `@xterm/addon-fit` (`lib/addon-fit.js`) | 0.11.0 |

SHA-256 of each file is in `xterm.sha256`; `make xterm-check` verifies it and
`make desktop-assets` runs it first. To bump: change `XTERM_VERSION` / `FIT_VERSION`
in the Makefile, run `make xterm-update`, review the diff, test both clients
(`terminal.html` touch selection depends on the `.xterm-screen` class).
