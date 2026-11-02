import { expect, test } from 'bun:test';
import { attachTab } from './attach.js';

const tab = () => ({ server: 's', name: 'n', attached: false, term: { cols: 80, rows: 24 } });

test('size fitted while Attach is in flight reaches the server after attach', async () => {
  const t = tab();
  const calls = [];
  const call = async (m, ...a) => {
    calls.push([m, ...a]);
    if (m === 'Attach') t.term.cols = 200, t.term.rows = 50; // late fit; onResize is dropped (not attached yet)
  };
  await attachTab(t, call, () => {});
  expect(t.attached).toBe(true);
  expect(calls).toEqual([
    ['Attach', 's', 'n', 80, 24],
    ['Resize', 's', 'n', 200, 50],
  ]);
});

test('re-fits after attach and sends that size', async () => {
  const t = tab();
  const calls = [];
  const fit = (x) => { x.term.cols = 120; x.term.rows = 40; };
  await attachTab(t, async (m, ...a) => { calls.push([m, ...a]); }, fit);
  expect(calls.at(-1)).toEqual(['Resize', 's', 'n', 120, 40]);
});

test('failed Attach propagates and sends no Resize', async () => {
  const t = tab();
  const calls = [];
  const call = async (m) => { calls.push(m); throw new Error('boom'); };
  await expect(attachTab(t, call, () => {})).rejects.toThrow('boom');
  expect(t.attached).toBe(false);
  expect(calls).toEqual(['Attach']);
});

test('failed Resize is swallowed', async () => {
  const t = tab();
  const call = async (m) => { if (m === 'Resize') throw new Error('gone'); };
  await attachTab(t, call, () => {});
  expect(t.attached).toBe(true);
});
