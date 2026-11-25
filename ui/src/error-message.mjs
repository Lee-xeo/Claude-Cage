import { formatMessage } from "./message-catalog.mjs";
import { localizeBackendMessage } from "./backend-message.mjs";

export const errorText = {
  http: "{0}失败（HTTP {1}）：{2}",
  connection: "{0}失败：工作台与 Cage 后台的连接已中断。请点击重新连接；若刚才正在保存或上传，恢复后先核对结果再重试。",
  native: "{0}失败：{1}。请重新连接 Cage；仍失败时在设置中导出诊断包。",
  invalidResponse: "{0}失败：后台返回的结果格式无效。请重新连接；仍出现时在设置中导出诊断包。",
};

// An untranslated template uses its source-language fragments together.
export function localizeFailure(localize, template, ...values) {
  if (localize(template) === template) return formatMessage(template, ...values);
  return localize(template, ...values.map(value => typeof value === "string" ? localize(value) : value));
}

export function operationName(path = "", method = "GET") {
  const reading = method === "GET";
  if (path === "/client/about") return reading ? "读取软件版本" : "检测应用更新";
  if (path === "/client/notifications") return reading ? "读取通知设置" : "保存通知设置";
  if (path.startsWith("/api/mobile/cli")) return reading ? "读取会话消息" : "提交会话操作";
  if (path.startsWith("/api/mobile/browser")) return "连接电脑上的浏览器";
  if (path.startsWith("/api/mobile/desktop")) return "连接电脑上的 Claude Desktop";
  if (path.startsWith("/environment/resources")) return reading ? "读取资源配置" : "应用资源配置";
  if (path === "/environment/initialization") return reading ? "读取初始化进度" : "保存初始化状态";
  if (path.startsWith("/environment/versions")) return "读取环境组件版本";
  if (path === "/environment/cache/inventory") return "盘点当前环境缓存";
  if (path === "/environment/cache/clean") return "清理所选缓存";
  if (path === "/environment/cache/restore") return "恢复缓存";
  if (path === "/environment/device-reset") return "读取换机状态";
  if (path === "/environment/device-reset/preview") return "预览新机配置";
  if (path === "/environment/device-reset/execute") return "执行环境换机";
  if (path === "/environment/device-reset/restore") return "恢复旧环境";
  if (path === "/environment/device-reset/delete-archive") return "删除旧环境归档";
  if (path.startsWith("/environment/")) return "更新环境";
  if (path.startsWith("/input/files") || path.startsWith("/browser/files")) return "传递文件";
  if (path.startsWith("/tailscale")) return reading ? "读取 Tailscale 状态" : "操作 Tailscale";
  if (path.startsWith("/browser")) return "操作浏览器";
  if (path === "/proxy/local-configs") return "查找本机代理配置";
  if (path === "/proxy/import-local") return "导入本机代理配置";
  if (path.startsWith("/proxy")) return reading ? "读取代理设置" : "应用代理设置";
  if (path.startsWith("/safety")) return "检查联网条件";
  if (path.startsWith("/settings/dns-check")) return "测试加密 DNS";
  if (path.startsWith("/settings/probe")) return "验证代理出口";
  if (path.startsWith("/settings")) return reading ? "读取设置" : "保存设置";
  if (path.startsWith("/diagnostics/export")) return "导出诊断日志";
  if (path.startsWith("/diagnostics")) return "读取诊断日志";
  if (path.startsWith("/projects")) return "连接项目目录";
  if (path.startsWith("/account")) return "处理账户操作";
  if (path.startsWith("/claude-desktop")) return "操作 Claude Desktop";
  if (path.startsWith("/desktop-instances")) return reading ? "读取 Desktop 实例" : "操作 Desktop 实例";
  if (path === "/token-usage") return reading ? "读取本地用量统计" : "更新本地用量统计";
  if (path.startsWith("/terminals")) return "连接终端";
  if (path === "/start") return "启动环境";
  if (path === "/stop") return "停止环境";
  return "连接 Cage 工作台";
}

export function httpErrorMessage(path, status, text = "", contentType = "", method = "GET", localize = formatMessage) {
  // Proxy HTML pages, stack traces and untranslated runtime errors are not UI
  // explanations. Preserve short Chinese messages supplied by Cage itself.
  const message = text.trim();
  if (contentType.startsWith("text/plain") && message.length <= 2000 && /\p{Script=Han}/u.test(message) && !/<(?:html|body|script|!doctype)\b/i.test(message)) {
    return localizeBackendMessage(message, localize);
  }
  if (path === "/settings" && method === "POST" && status === 503) {
    return localize("暂时无法确认设置是否已保存（HTTP 503）。请在连接恢复后核对设置和操作详情，再决定是否重新保存。");
  }
  const action = operationName(path, method);
  const reasons = {
    400: "后台没有接受这次请求。请核对填写的内容；仍失败时在设置中导出诊断包。",
    401: "工作台登录会话已失效。请重新连接 Cage，再发起这次操作。",
    403: "后台拒绝了这次访问。请从 Cage 应用重新打开工作台。",
    404: "当前后台没有提供所需功能。请确认应用与后台已完成更新。",
    409: "环境状态已变化或另一个操作尚未结束。请刷新状态并等待当前操作完成。",
    413: "文件或请求内容超过允许大小。请缩小文件或分批处理。",
    423: "Cage 已锁定。请先解锁账户，再发起这次操作。",
    429: "请求过于频繁。请等待片刻，避免连续点击。",
    500: "后台处理请求时出错。请在设置中导出诊断包，并附上刚才执行的操作。",
    502: "后台未能连接环境内的服务。请确认环境已启动，再检查环境组件状态。",
    503: "后台服务尚未就绪或正在重启。请等待连接恢复后再操作。",
    504: "等待后台响应超时。请检查环境负载，恢复连接后核对操作是否已生效。",
  };
  return localizeFailure(localize, errorText.http, action, status, reasons[status] || "尚未取得可识别的原因。请在设置中导出诊断包。");
}

export function connectionErrorMessage(path, method = "GET", localize = formatMessage) {
  return localizeFailure(localize, errorText.connection, operationName(path, method));
}

export function nativeFailureMessage(action, message = "", localize = formatMessage) {
  if (/\p{Script=Han}/u.test(message) && !["文件操作失败", "原生操作超时，请重试", "操作失败"].includes(message)) {
    return localizeBackendMessage(message, localize);
  }
  const names = { chooseFolder: "选择本机文件夹", chooseUploadFiles: "选择上传文件", uploadHostFiles: "上传本机文件", openHostPath: "打开本机文件", exportDiagnostics: "导出诊断包", appUpdate: "检查或安装应用更新", clipboardRead: "读取剪贴板", clipboardWrite: "写入剪贴板", refreshSession: "恢复工作台登录会话", notificationStatus: "读取系统通知状态", requestNotificationPermission: "请求系统通知权限", showNotification: "发送系统通知", openNotificationSettings: "打开系统通知设置" };
  const reason = /timeout|超时/i.test(message) ? "等待本机组件响应超时" : "本机组件没有返回可识别的原因";
  return localizeFailure(localize, errorText.native, names[action] || "执行本机操作", reason);
}
