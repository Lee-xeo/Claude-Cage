set -euo pipefail
id=$1
[[ $id =~ ^[0-9a-f]{24}$ ]]
ns="net-$id"
exec 9>/run/account-network.lock
flock -w 20 9
if ! test -e "/run/netns/$ns"; then
  ip netns add "$ns"
  trap 'ip netns del "$ns"' ERR
  ip -n "$ns" link set lo up
  ip netns exec "$ns" sysctl -qw net.ipv4.ip_unprivileged_port_start=0
  ip -n "$ns" link add uplink type dummy
  ip -n "$ns" address add 192.0.2.1/32 dev uplink
  ip -n "$ns" link set uplink up
  ip -n "$ns" route add default dev uplink
  ip netns exec "$ns" sysctl -qw net.ipv6.conf.all.disable_ipv6=1
  # No veth, physical interface, or NAT route out of the namespace. Every
  # external TCP connection is intercepted; unhandled protocols go nowhere.
  ip netns exec "$ns" iptables -t nat -A OUTPUT ! -d 127.0.0.0/8 -p tcp -j REDIRECT --to-ports 15301
  trap - ERR
fi
install -d -m 0755 "/etc/netns/$ns"
printf 'nameserver 127.0.0.1\noptions attempts:1 timeout:4\n' >"/etc/netns/$ns/resolv.conf"
# Let the workbench browser reach this namespace without exposing a guest TCP
# listener. The browser controller's group is configured for custom guest users.
group=$(stat -c %G /etc/claude-cage/browser-control.json)
install -d -m 2750 -o "${SUDO_UID:?}" -g "$group" "/run/account-routes/$id"
# Avoid host resolver services reached via Unix sockets.
sed -E 's/^hosts:.*/hosts: files dns/' /etc/nsswitch.conf >"/etc/netns/$ns/nsswitch.conf"
dir="/usr/local/lib/account-routes/$id"
install -d -m 0755 "$dir"
cat >"$dir/open" <<'OPEN'
#!/bin/bash
set -euo pipefail
id=${0%/open}; id=${id##*/}
[[ $id =~ ^[a-f0-9]{24}$ ]]
args=(--route-id "$id")
while [[ $# -ge 3 ]]; do
  [[ $1 == --account-id || $1 == --login-id ]] && [[ $2 =~ ^[a-f0-9]{24}$ ]] || exit 2
  args+=("$1" "$2"); shift 2
done
opener=(sudo nsenter --net=/proc/1/ns/net setpriv --reuid="$(id -u)" --regid="$(id -g)" --init-groups /usr/local/bin/cage-open "${args[@]}")
if [[ ${1:-} == --finish-login ]]; then exec "${opener[@]}" --finish-login; fi
if [[ ${1:-} == --stdin ]]; then url=$(cat); else url=${1:-}; fi
[[ $url == https://* && $url != *[[:space:]]* && ${#url} -le 8192 ]] || exit 2
printf '%s' "$url" | "${opener[@]}" --stdin
OPEN
chmod 0755 "$dir/open"
