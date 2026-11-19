#!/usr/bin/env bash
set -euo pipefail
if systemctl is-active --quiet desktop-session.service; then
  test -f /run/desktop-session/keyring-ready || { echo '请先保存并关闭现有 Claude Desktop，再使用 Cage 账户重新打开。' >&2; exit 1; }
  exit 0
fi
if systemctl is-failed --quiet desktop-session.service; then
  sudo systemctl reset-failed desktop-session.service
fi
sudo systemctl start --no-block desktop-session.service
for i in $(seq 1 150); do
  if [ -p /run/desktop-session/unlock ]; then break; fi
  if systemctl is-failed --quiet desktop-session.service; then
    echo '图形会话未能启动。' >&2; exit 1
  fi
  sleep .1
done
test -p /run/desktop-session/unlock || { echo '等待凭据解锁通道超时。' >&2; exit 1; }
# The internal random key comes exclusively from SSH stdin, never argv or disk.
timeout 10 sh -c 'cat > /run/desktop-session/unlock'
for i in $(seq 1 300); do
  if systemctl is-active --quiet desktop-session.service; then exit 0; fi
  if systemctl is-failed --quiet desktop-session.service; then
    echo 'Claude Desktop 未能启动或密钥环未能解锁；已有登录数据保持不变。' >&2; exit 1
  fi
  sleep .1
done
echo 'Claude Desktop 启动超时。' >&2
exit 1
