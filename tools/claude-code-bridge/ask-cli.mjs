import { execFile } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { open, readFile, realpath, stat } from 'node:fs/promises';
import { basename, isAbsolute, join } from 'node:path';
import { deflateSync } from 'node:zlib';

const uuid = /^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i;
const maxFiles = 8;
const maxFileBytes = 10 * 1024 * 1024;
const maxTotalBytes = 20 * 1024 * 1024;
const waitSeconds = { type: 'integer', minimum: 0, maximum: 60, description: '本次最多等多少秒。默认 30，传 0 立即返回任务号。' };
const schema = (properties, required) => ({ type: 'object', properties, required, additionalProperties: false });
export const askTool = {
  name: 'ask',
  description: '通过云端 Claude Code 提问。同一 Codex 聊天自动续聊。返回 running 时用 ask_result 等到完成后转回全文。',
  inputSchema: schema({
    text: { type: 'string', minLength: 1, description: '用户要发给 Claude 的原文。' },
    sessionId: { type: 'string', description: '显式继续指定的 Claude 会话。旧客户端未提供聊天编号时，续问带上轮的 sessionId。' },
    newSession: { type: 'boolean', description: '为本聊天另开一个 Claude 会话。' },
    files: { type: 'array', maxItems: maxFiles, items: { type: 'string' }, description: '本轮用户指定的本机附件绝对路径，随问题上传到 Cage 客机。' },
    waitSeconds,
  }, ['text']),
};
export const resultTool = {
  name: 'ask_result',
  description: '按 jobId 取云端原任务的回复。running 时继续用同一 jobId 等待。',
  inputSchema: schema({ jobId: { type: 'string', description: 'ask 返回的任务号。' }, waitSeconds }, ['jobId']),
};

const source = deflateSync(await readFile(new URL('./guest.py', import.meta.url))).toString('base64');
const python = "import base64,zlib;GUEST_SOURCE=zlib.decompress(base64.b64decode(\"" + source + "\")).decode();exec(GUEST_SOURCE)";
const guestCommand = "TOKIO_WORKER_THREADS=1 ~/.claude-cage/cloud-host/bin/cage run /usr/bin/python3 -c '" + python + "'";

function validWait(value = 30) {
  if (!Number.isInteger(value) || value < 0 || value > 60) throw Error('waitSeconds 应为 0 到 60 的整数');
  return value;
}

function privatePath(path) {
  return path.split(/[\\/]/).some(part =>
    /^(?:\.ssh|\.gnupg|\.aws|\.azure|\.kube|\.codex|\.claude|\.env(?:\..*)?|id_(?:rsa|ed25519|ecdsa|dsa)(?:\..*)?|auth\.json|\.credentials\.json|credentials(?:\.json)?|.*\.(?:pem|p12|pfx|key))$/i.test(part));
}

async function attachments(files = []) {
  if (!Array.isArray(files) || files.length > maxFiles) throw Error('每次最多可带 8 个附件');
  const result = [];
  let total = 0;
  for (const path of files) {
    if (typeof path !== 'string' || !isAbsolute(path) || /[\x00-\x1f]/.test(path)) throw Error('附件需要使用本机文件的绝对路径');
    const actual = await realpath(path);
    if (privatePath(path) || privatePath(actual)) throw Error('不能将凭据文件作为附件：' + basename(path));
    if (!(await stat(actual)).isFile()) throw Error('附件必须是普通文件：' + basename(path));
    const file = await open(actual, 'r');
    try {
      const { size } = await file.stat();
      if (size > maxFileBytes || total + size > maxTotalBytes) throw Error('附件超出限制：每个 10 MiB，每次合计 20 MiB');
      const data = await file.readFile();
      if (data.length !== size) throw Error('附件在读取时发生变化：' + basename(path));
      total += size;
      result.push({ name: basename(path), data: data.toString('base64') });
    } finally {
      await file.close();
    }
  }
  return result;
}

async function runGuest({ profileDir, hostId }, request) {
  const hosts = JSON.parse((await readFile(join(profileDir, 'desktop-entry', 'remote-hosts.json'), 'utf8')).replace(/^\uFEFF/, ''));
  const host = hosts.hosts.find(item => item.id === hostId);
  if (!host) throw Error('未找到 Cage SSH 主机');
  const sshArgs = ['-T', '-o', 'BatchMode=yes', '-o', 'IdentitiesOnly=yes', '-o', 'StrictHostKeyChecking=yes',
    '-o', 'ConnectTimeout=8', '-i', host.auth.identity_file, '-p', String(host.port), host.user + '@' + host.host, guestCommand];
  return new Promise((resolve, reject) => {
    // This deadline only bounds the SSH exchange. Claude runs in a detached guest process.
    const child = execFile('ssh', sshArgs, { windowsHide: true, timeout: 100_000, maxBuffer: 16 << 20 }, (error, out, stderr) => {
      let receipt;
      for (const line of out.trim().split(/\r?\n/)) {
        let value;
        try { value = JSON.parse(line); } catch { continue; }
        if (value.error) return reject(Error(value.error));
        if (value.jobId && value.status) receipt = value;
      }
      if (receipt) return resolve(receipt);
      // Never include execFile's full command (host, key path and packaged source) in the error.
      const message = stderr.trim() || (error?.killed ? 'SSH 等待超时' : '未收到 Cage 客机的回执');
      reject(Object.assign(Error(message), { details: { jobId: request.jobId, hint: '先用 ask_result 查询这个任务号。' } }));
    });
    child.stdin.on('error', () => {}); // ssh exit status and any receipt are handled above.
    child.stdin.end(JSON.stringify(request));
  });
}

export async function askCli(config, args, threadId) {
  if (typeof args.text !== 'string' || !args.text.trim()) throw Error('请输入问题');
  if (args.sessionId !== undefined && (typeof args.sessionId !== 'string' || !uuid.test(args.sessionId))) throw Error('无效的 Claude 会话 ID');
  if (args.newSession !== undefined && typeof args.newSession !== 'boolean') throw Error('newSession 需要布尔值');
  if (args.newSession && args.sessionId) throw Error('新开会话和指定已有会话不能同时使用');
  const wait = validWait(args.waitSeconds);
  const chat = typeof threadId === 'string' && threadId.trim() ? threadId : undefined;
  const result = await runGuest(config, { action: 'ask', jobId: randomUUID(), text: args.text, files: await attachments(args.files),
    sessionId: args.sessionId, newSession: args.newSession, threadId: chat, waitSeconds: wait });
  if (!chat) result.needsSessionId = true;
  return result;
}

export function askResult(config, args) {
  if (typeof args.jobId !== 'string' || !uuid.test(args.jobId)) throw Error('无效的任务号');
  return runGuest(config, { action: 'result', jobId: args.jobId, waitSeconds: validWait(args.waitSeconds) });
}
