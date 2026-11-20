import fs from 'node:fs/promises';
import { isMainModule } from './is-main-module.mjs';

const marker = '<!-- contribution-invite -->';

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

export async function runContributionInvite({ repository, actor, eventName, event, token, inviteToken, request = githubRequest }) {
  if (eventName !== 'workflow_dispatch' && !(eventName === 'issues' && event.action === 'labeled' && event.label?.name === 'accepted')) {
    return { status: 'skipped' };
  }
  const number = String(eventName === 'workflow_dispatch' ? event.inputs?.issue_number : event.issue?.number);
  if (!/^[1-9]\d*$/.test(number)) throw new Error('Issue 编号必须为正整数。');
  if (!/^[\w.-]+\/[\w.-]+$/.test(repository)) throw new Error('仓库名称无效。');
  if (!token) throw new Error('缺少 GITHUB_TOKEN，无法核验审核权限。');
  const prefix = `/repos/${repository}`;
  async function api(method, path, apiToken = token, body, allowed = [200]) {
    const response = await request(method, path, apiToken, body);
    if (!allowed.includes(response.status)) throw new Error(`GitHub API ${method} ${path} 失败（HTTP ${response.status}）。`);
    return response;
  }
  async function list(path, apiToken = token) {
    const items = [];
    for (let page = 1; ; page++) {
      const { data } = await api('GET', `${path}?per_page=100&page=${page}`, apiToken);
      items.push(...data);
      if (data.length < 100) return items;
    }
  }

  // Repository write access alone must not allow newly invited contributors to invite others.
  const reviewer = await api('GET', `${prefix}/collaborators/${encodeURIComponent(actor)}/permission`);
  if (!['admin', 'maintain'].includes(reviewer.data.role_name || reviewer.data.permission) && !reviewer.data.user?.permissions?.maintain) {
    throw new Error('审核者需要仓库 admin 或 maintain 权限，才能触发协作者邀请。');
  }
  const { data: issue } = await api('GET', `${prefix}/issues/${number}`);
  const labels = new Set(issue.labels.map(label => typeof label === 'string' ? label : label.name));
  if (issue.pull_request || issue.state !== 'open' || !labels.has('contribution') || !labels.has('accepted') || issue.user.type !== 'User') {
    return { status: 'skipped' };
  }
  const author = issue.user.login;
  const permission = await api('GET', `${prefix}/collaborators/${encodeURIComponent(author)}/permission`, token, undefined, [200, 404]);
  let status = 'already-collaborator';
  if (permission.status !== 200 || !['admin', 'maintain', 'write'].includes(permission.data.permission)) {
    if (!inviteToken) throw new Error('请配置 CONTRIBUTION_INVITE_TOKEN：仅授权当前仓库，Administration: Read and write。配置后可手动重跑此 Issue。');
    const invitations = await list(`${prefix}/invitations`, inviteToken);
    if (invitations.some(invitation => invitation.invitee?.id === issue.user.id)) {
      status = 'invitation-pending';
    } else {
      const invitation = await api('PUT', `${prefix}/collaborators/${encodeURIComponent(author)}`, inviteToken, { permission: 'push' }, [201, 204]);
      status = invitation.status === 201 ? 'invited' : 'already-collaborator';
    }
  }

  const server = process.env.GITHUB_SERVER_URL || 'https://github.com';
  const nextStep = status === 'already-collaborator'
    ? '你已拥有仓库写权限，可以为本 Issue 创建 PR。'
    : `协作者邀请已发出或仍待接受。请先通过 GitHub 通知或[邀请页面](${server}/${repository}/invitations)接受邀请，再创建 PR。`;
  const body = `${marker}\n@${author}，本次方案已通过维护者审核。${nextStep}\n\n协作者具有整个仓库的写权限，请为本次方案创建独立分支，在 PR 中关联 #${number}，补充改动和验证结果，等待审查通过后合入。详细步骤见[贡献指南](${server}/${repository}/blob/HEAD/.github/CONTRIBUTING.md)。`;
  const comments = await list(`${prefix}/issues/${number}/comments`);
  const previous = comments.find(comment => comment.user?.login === 'github-actions[bot]' && comment.body?.includes(marker));
  if (previous) {
    if (previous.body !== body) await api('PATCH', `${prefix}/issues/comments/${previous.id}`, token, { body });
  } else {
    await api('POST', `${prefix}/issues/${number}/comments`, token, { body }, [201]);
  }
  return { status, issue: Number(number), author };
}

if (isMainModule(import.meta.url)) {
  try {
    const event = JSON.parse(await fs.readFile(process.env.GITHUB_EVENT_PATH, 'utf8'));
    const result = await runContributionInvite({
      repository: process.env.GITHUB_REPOSITORY,
      actor: process.env.GITHUB_ACTOR,
      eventName: process.env.GITHUB_EVENT_NAME,
      event,
      token: process.env.GITHUB_TOKEN,
      inviteToken: process.env.CONTRIBUTION_INVITE_TOKEN,
    });
    console.log(JSON.stringify(result));
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
