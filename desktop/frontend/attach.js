// Attach a tab's stream, then push the size the terminal has *now*.
// term.onResize is gated on t.attached, so any fit that lands while Attach is in
// flight (cell size / webfont not measured at open time) never reaches tmux.
export async function attachTab(t, call, fitTab) {
  await call('Attach', t.server, t.name, t.term.cols, t.term.rows);
  t.attached = true;
  fitTab(t);
  call('Resize', t.server, t.name, t.term.cols, t.term.rows).catch(() => {});
}
