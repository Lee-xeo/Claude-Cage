import { createInterface } from 'node:readline';
import { parseArgs } from 'node:util';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { askCli, askResult, askTool, resultTool } from './ask-cli.mjs';

const toolList = [
  askTool, resultTool,
  { ...askTool, name: 'send', description: 'ask 的兼容入口。' },
];
const supportedVersions = ['2025-11-25', '2025-06-18', '2025-03-26', '2024-11-05'];
const isObject = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const parseJson = text => JSON.parse(text.replace(/^\uFEFF/, ''));

function errorBody(error) {
  return { error: error.code || 'cage_error', message: error.message, ...(error.details ? { details: error.details } : {}) };
}

async function serve(call) {
  const input = createInterface({ input: process.stdin, crlfDelay: Infinity });
  let initialized = false;
  const write = payload => process.stdout.write(JSON.stringify({ jsonrpc: '2.0', ...payload }) + '\n');
  async function handle(line) {
    let request;
    try {
      request = parseJson(line);
    } catch {
      return write({ id: null, error: { code: -32700, message: 'Parse error' } });
    }
    if (!isObject(request) || request.jsonrpc !== '2.0' || typeof request.method !== 'string' ||
        ('id' in request && !(Number.isSafeInteger(request.id) || typeof request.id === 'string'))) {
      return write({ id: null, error: { code: -32600, message: 'Invalid Request' } });
    }
    if (!Object.hasOwn(request, 'id')) return;
    const { id, method, params = {} } = request;
    const rpcError = (code, message) => write({ id, error: { code, message } });
    if (!isObject(params)) return rpcError(-32602, 'Invalid params');
    if (method === 'initialize') {
      if (initialized || typeof params.protocolVersion !== 'string') return rpcError(-32602, 'Invalid initialize');
      initialized = true;
      const protocolVersion = supportedVersions.includes(params.protocolVersion) ? params.protocolVersion : supportedVersions[0];
      return write({ id, result: { protocolVersion, capabilities: { tools: {} }, serverInfo: { name: 'cage-claude', version: '1.0.0' } } });
    }
    if (method === 'ping') return write({ id, result: {} });
    if (!initialized) return rpcError(-32002, 'Server not initialized');
    if (method === 'tools/list') return write({ id, result: { tools: toolList } });
    if (method !== 'tools/call') return rpcError(-32601, 'Method not found');
    if (!toolList.some(tool => tool.name === params.name)) return rpcError(-32602, 'Unknown tool');
    let value, isError = false;
    try {
      value = await call(params.name, params.arguments === undefined ? {} : params.arguments, params._meta?.threadId);
      isError = value.status === 'failed' || value.status === 'busy';
    } catch (error) {
      value = errorBody(error);
      isError = true;
    }
    const content = [{ type: 'text', text: JSON.stringify(value) }];
    write({ id, result: { content, isError } });
  }
  const pending = new Set();
  for await (const line of input) {
    if (!line.trim()) continue;
    const response = handle(line);
    pending.add(response);
    response.finally(() => pending.delete(response));
  }
  await Promise.all(pending);
}


try {
  const { values } = parseArgs({ options: {
    'host-id': { type: 'string' },
    'profile-dir': { type: 'string' },
    call: { type: 'string' },
  } });
  const hostId = values['host-id'] || process.env.CAGE_HOST_ID;
  if (!hostId) throw Error('请指定 --host-id 或 CAGE_HOST_ID');
  const profileDir = values['profile-dir'] || join(homedir(), '.claude-cage');
  const call = (name, args, threadId) => {
    const tool = toolList.find(item => item.name === name);
    if (!tool) throw Error('未知工具');
    if (!isObject(args) || Object.keys(args).some(key => !Object.hasOwn(tool.inputSchema.properties, key))) {
      throw Error('工具参数无效');
    }
    const config = { hostId, profileDir };
    return name === 'ask_result' ? askResult(config, args) : askCli(config, args, threadId);
  };
  if (values.call) {
    let raw = '';
    process.stdin.setEncoding('utf8');
    for await (const chunk of process.stdin) raw += chunk;
    const result = await call(values.call, parseJson(raw), process.env.CODEX_THREAD_ID || process.env.CODEX_SESSION_ID);
    console.log(JSON.stringify(result));
    if (result.status === 'failed' || result.status === 'busy') process.exitCode = 1;
  } else {
    await serve(call);
  }
} catch (error) {
  console.error(JSON.stringify(errorBody(error)));
  process.exitCode = 1;
}
