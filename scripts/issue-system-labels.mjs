import fs from 'node:fs/promises';
import { isMainModule } from './is-main-module.mjs';

const systems = [
  ['macOS', /mac\s*os|macbook|\bmac\b|os\s*x/i],
  ['Windows', /windows|\bwin\s?1[01]\b/i],
  ['Linux', /linux|ubuntu|debian|fedora|centos|deepin/i],
];
const everySystem = /全部平台|所有平台|全平台/;

function detect(text) {
  if (everySystem.test(text)) return systems.map(([name]) => name);
  return systems.filter(([, pattern]) => pattern.test(text)).map(([name]) => name);
}

// Issue forms render each field as "### label" followed by the answer. Issues
// written through the CLI or API reuse the same headings, so the heading decides
// whether an answer describes the host or the guest.
export function systemLabelsFromBody(body) {
  const text = { 主机: '', 客机: '' };
  let heading = '';
  for (const line of String(body || '').split(/\r?\n/)) {
    const title = line.match(/^#{1,6}\s+(.*)$/);
    if (title) {
      heading = title[1];
      continue;
    }
    const host = heading.includes('主机');
    const guest = heading.includes('客机');
    if (host && guest) {
      // A shared field lists each side on its own "主机：" / "客机：" line.
      const side = line.match(/^\s*(?:[-*]\s*)?(主机|客机)/);
      if (side) text[side[1]] += `${line}\n`;
    } else if (host || guest) {
      text[host ? '主机' : '客机'] += `${line}\n`;
    }
  }
  return Object.entries(text).flatMap(([role, answer]) => detect(answer).map(system => `${system} ${role}`));
}

async function githubRequest(method, path, token, body) {
  const response = await fetch(`${process.env.GITHUB_API_URL || 'https://api.github.com'}${path}`, {
    method,
    headers: {
      Accept: 'application/vnd.github+json',
      Authorization: `Bearer ${token}`,
      'X-GitHub-Api-Version': '2022-11-28',
      ...(body ? { 'Content-Type': 'application/json' } : {}),
    },
    body: body ? JSON.stringify(body) : undefined,
    signal: AbortSignal.timeout(30_000),
  });
  const text = await response.text();
  return { status: response.status, data: text ? JSON.parse(text) : undefined };
}

export async function runIssueSystemLabels({ repository, number, token, request = githubRequest }) {
  number = String(number);
  if (!/^[1-9]\d*$/.test(number)) throw new Error('Issue 编号必须为正整数。');
  if (!/^[\w.-]+\/[\w.-]+$/.test(repository)) throw new Error('仓库名称无效。');
  if (!token) throw new Error('缺少 GITHUB_TOKEN，无法添加标签。');
  const path = `/repos/${repository}/issues/${number}`;
  const issue = await request('GET', path, token);
  if (issue.status !== 200) throw new Error(`GitHub API GET ${path} 失败（HTTP ${issue.status}）。`);
  if (issue.data.pull_request) return { status: 'skipped', labels: [] };
  const current = new Set(issue.data.labels.map(label => typeof label === 'string' ? label : label.name));
  // Only add: labels a maintainer set or corrected by hand stay as they are.
  const labels = systemLabelsFromBody(issue.data.body).filter(label => !current.has(label));
  if (!labels.length) return { status: 'unchanged', labels };
  const added = await request('POST', `${path}/labels`, token, { labels });
  if (added.status !== 200) throw new Error(`GitHub API POST ${path}/labels 失败（HTTP ${added.status}）。`);
  return { status: 'labeled', labels };
}

if (isMainModule(import.meta.url)) {
  try {
    const event = JSON.parse(await fs.readFile(process.env.GITHUB_EVENT_PATH, 'utf8'));
    const result = await runIssueSystemLabels({
      repository: process.env.GITHUB_REPOSITORY,
      number: event.issue?.number ?? event.inputs?.issue_number,
      token: process.env.GITHUB_TOKEN,
    });
    console.log(JSON.stringify(result));
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
