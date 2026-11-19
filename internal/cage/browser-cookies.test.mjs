import assert from 'node:assert/strict';
import test from 'node:test';
import { browserCookies } from './browser-cookies.mjs';
import { BrowserVault } from './browser-vault.mjs';

const tab = { targetId: 'tab', url: 'https://sub.example.test/page', browserContextId: 'private-context', incognito: true };
const cookie = (domain, extra = {}) => ({ name: 'session', value: 'fixture-secret', domain, path: '/', expires: -1, session: true, ...extra });

function storage(initial) {
  let cookies = initial;
  const deletions = [];
  const cdp = async (method, params, session) => {
    if (method === 'Storage.getCookies') {
      assert.deepEqual(params, { browserContextId: 'private-context' });
      return { cookies };
    }
    assert.equal(method, 'Network.deleteCookies');
    assert.equal(session, 'sid');
    deletions.push(params);
    cookies = cookies.filter(item => !(item.name === params.name && item.domain === params.domain && item.path === params.path &&
      JSON.stringify(item.partitionKey) === JSON.stringify(params.partitionKey)));
  };
  return { cdp, deletions, run: input => browserCookies(cdp, async () => 'sid', tab, { scope: 'all', ...input }) };
}

test('all search results expose selectable keys across pages and batch deletion preserves unmatched cookies', async () => {
  const f = storage([
    ...Array.from({ length: 57 }, (_, i) => cookie('sub.example.test', { name: `batch-${i}` })),
    cookie('sub.example.test', { name: 'unmatched' }),
    cookie('other.test', { name: 'batch-other-site' }),
    cookie('sub.example.test', { name: 'batch-opaque', partitionKeyOpaque: true }),
  ]);
  const result = await f.run({ mode: 'list', scope: 'site', query: 'BATCH-', page: 1 });
  assert.equal(result.total, 58);
  assert.equal(result.cookies.length, 8);
  assert.equal(result.selectableKeys.length, 57);
  assert.equal(new Set(result.selectableKeys).size, 57);
  const after = await f.run({ mode: 'delete-selected', scope: 'site', query: 'BATCH-', page: 1, cookieKeys: result.selectableKeys });
  assert.equal(f.deletions.length, 57);
  assert.equal(after.total, 1);
  assert.equal(after.page, 0);
  assert.deepEqual(after.selectableKeys, []);
  assert.deepEqual((await f.run({ mode: 'list' })).cookies.map(item => item.name).sort(), ['batch-opaque', 'batch-other-site', 'unmatched']);
});

test('batch selection keeps exact paths and partition keys and deduplicates requested keys', async () => {
  const partitionKey = { topLevelSite: 'https://example.test', hasCrossSiteAncestor: true };
  const f = storage([cookie('sub.example.test'), cookie('sub.example.test', { path: '/nested' }), cookie('sub.example.test', { partitionKey })]);
  const { cookies } = await f.run({ mode: 'list' });
  const selected = cookies.filter(item => item.path === '/nested' || item.partitionKey).map(item => item.key);
  const after = await f.run({ mode: 'delete-selected', cookieKeys: [...selected, selected[0]] });
  assert.equal(after.total, 1);
  assert.equal(after.cookies[0].path, '/');
  assert.equal(after.cookies[0].partitionKey, undefined);
  assert.equal(f.deletions.length, 2);
  assert.ok(f.deletions.some(item => item.path === '/nested'));
  assert.ok(f.deletions.some(item => item.partitionKey === partitionKey));
});

test('invalid, stale, out-of-scope or opaque batches fail before deleting any selected cookies', async () => {
  const f = storage([cookie('sub.example.test'), cookie('other.test'), cookie('sub.example.test', { name: 'opaque', partitionKeyOpaque: true })]);
  const { cookies } = await f.run({ mode: 'list' });
  const valid = cookies.find(item => item.name === 'session' && item.domain === 'sub.example.test').key;
  const other = cookies.find(item => item.domain === 'other.test').key;
  const opaque = cookies.find(item => item.partitionKeyOpaque).key;
  for (const cookieKeys of [undefined, [], 'not-an-array', [null], ['bad'], [valid, '0'.repeat(64)], [valid, opaque]]) {
    await assert.rejects(f.run({ mode: 'delete-selected', cookieKeys }));
  }
  await assert.rejects(f.run({ mode: 'delete-selected', scope: 'site', cookieKeys: [valid, other] }), /已变化/);
  await assert.rejects(f.run({ mode: 'delete-selected', query: 'opaque', cookieKeys: [opaque, valid] }), /已变化/);
  await assert.rejects(f.run({ mode: 'delete-selected', url: 'https://previous.test/', cookieKeys: [valid] }), /网页已切换/);
  assert.deepEqual(f.deletions, []);
});

test('a transport failure during a batch reports partial deletion and requests a refresh', async () => {
  const f = storage([cookie('sub.example.test', { name: 'first' }), cookie('sub.example.test', { name: 'second' })]);
  const result = await f.run({ mode: 'list' });
  let attempts = 0;
  const cdp = async (...args) => {
    if (args[0] === 'Network.deleteCookies' && ++attempts === 2) throw new Error('connection closed');
    return f.cdp(...args);
  };
  await assert.rejects(browserCookies(cdp, async () => 'sid', tab, {
    mode: 'delete-selected', scope: 'all', cookieKeys: result.selectableKeys,
  }), /部分 Cookie 可能已删除.*刷新列表/);
  assert.equal(f.deletions.length, 1);
  assert.equal((await f.run({ mode: 'list' })).total, 1);
});

test('site scope includes parent domain cookies and excludes host-only parent and lookalikes', async () => {
  const calls = [];
  const cdp = async (method, params) => {
    calls.push({ method, params });
    return { cookies: [cookie('.example.test'), cookie('example.test'), cookie('sub.example.test'), cookie('.notexample.test'), cookie('other.example.test')] };
  };
  const result = await browserCookies(cdp, async () => 'sid', tab, { mode: 'list', scope: 'site' });
  assert.deepEqual(result.cookies.map(cookie => cookie.domain), ['.example.test', 'sub.example.test']);
  assert.equal(result.incognito, true);
  assert.ok(result.cookies.every(cookie => !('value' in cookie)));
  assert.deepEqual(calls, [{ method: 'Storage.getCookies', params: { browserContextId: 'private-context' } }]);
});

test('opaque partitions refuse site-wide and individual deletion before any mutation', async () => {
  const calls = [];
  const cdp = async method => { calls.push(method); return { cookies: [cookie('sub.example.test'), cookie('sub.example.test', { partitionKeyOpaque: true })] }; };
  const data = await browserCookies(cdp, async () => 'sid', tab, { mode: 'list', scope: 'site' });
  await assert.rejects(browserCookies(cdp, async () => 'sid', tab, { mode: 'delete', scope: 'site', cookieKey: data.cookies.find(cookie => cookie.partitionKeyOpaque).key }), /分区/);
  await assert.rejects(browserCookies(cdp, async () => 'sid', tab, { mode: 'clear', scope: 'site' }), /分区/);
  assert.ok(calls.every(method => method === 'Storage.getCookies'));
});

test('stale page or authorization tabs cannot reach cookie storage', async () => {
  const cdp = () => { throw new Error('must not contact cookie storage'); };
  await assert.rejects(browserCookies(cdp, null, tab, { mode: 'clear', scope: 'all', url: 'https://previous.test/' }), /网页已切换/);
  await assert.rejects(browserCookies(cdp, null, { ...tab, login: true }, { mode: 'list', scope: 'all' }), /不可管理/);
});

test('vault never attaches credential observers to private or authorization tabs', async () => {
  for (const mode of ['incognito', 'login']) {
    const vault = new BrowserVault({ cdp: () => { throw new Error('must not install credential observer'); }, active: () => 'tab', tab: () => ({ [mode]: true }) });
    await vault.attach('tab', 'sid');
    assert.equal(vault.sessions.size, 0);
    await vault.request({ action: 'status', lease: 'f'.repeat(64), tab: 'tab' });
    await assert.rejects(vault.request({ action: 'fill', lease: 'f'.repeat(64), tab: 'tab' }), /网页已切换/);
  }
});
