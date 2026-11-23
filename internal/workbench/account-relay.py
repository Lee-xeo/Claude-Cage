"""Linux namespace edge. The only uplink is a controller-created Unix socket."""

import asyncio
import os
import socket
import struct
import ipaddress
import sys

UPSTREAM = sys.argv[1]
LOGIN_SOCKET = sys.argv[2]
STDIN_AUTH = len(sys.argv) > 3 and sys.argv[3] == "--stdin-auth"
AUTH_HEADER = b""
MAX_DNS = 65535
slots = asyncio.Semaphore(96)
browser_slots = asyncio.Semaphore(48)


async def upstream(header, payload=b""):
    reader, writer = await asyncio.open_unix_connection(UPSTREAM)
    try:
        if AUTH_HEADER and header.endswith(b"\r\n\r\n"):
            header = header[:-2] + AUTH_HEADER + b"\r\n"
        writer.write(header + payload)
        await writer.drain()
        reply = await asyncio.wait_for(reader.readuntil(b"\r\n\r\n"), 28)
        if not reply.startswith(b"HTTP/1.1 200 "):
            raise ConnectionError("route unavailable")
        return reader, writer, reply
    except BaseException:
        writer.close()
        raise


async def copy(source, destination):
    while data := await source.read(65536):
        destination.write(data)
        await destination.drain()
    destination.write_eof()


async def pipe_pair(reader, writer, incoming, remote):
    tasks = [
        asyncio.create_task(copy(reader, remote)),
        asyncio.create_task(copy(incoming, writer)),
    ]
    try:
        await asyncio.gather(*tasks)
    finally:
        for task in tasks:
            task.cancel()
        await asyncio.gather(*tasks, return_exceptions=True)


async def browser_peer(reader, writer):
    version, count = await reader.readexactly(2)
    if version != 5 or 0 not in await reader.readexactly(count):
        raise ValueError("unsupported SOCKS authentication")
    writer.write(b"\x05\x00")
    await writer.drain()
    version, command, reserved, kind = await reader.readexactly(4)
    if (version, command, reserved) != (5, 1, 0):
        raise ValueError("expected SOCKS CONNECT")
    if kind == 3:
        size = (await reader.readexactly(1))[0]
        host = (await reader.readexactly(size)).decode("ascii")
    elif kind == 1:
        host = socket.inet_ntoa(await reader.readexactly(4))
    else:
        raise ValueError("unsupported SOCKS address")
    port = int.from_bytes(await reader.readexactly(2), "big")
    if not port or port in (53, 15301):
        raise ValueError("reserved port")
    if host.rstrip(".").lower() == "localhost":
        host = "127.0.0.1"
    else:
        addresses = await asyncio.get_running_loop().getaddrinfo(
            host, port, family=socket.AF_INET, type=socket.SOCK_STREAM
        )
        host = addresses[0][4][0]
    if ipaddress.ip_address(host).is_loopback and (host != "127.0.0.1" or port < 1024):
        raise ValueError("invalid callback")
    return await asyncio.open_connection(host, port, family=socket.AF_INET)


async def browser_connect(reader, writer):
    # Chromium sends names over SOCKS5; DNS and the localhost callback both
    # use this account's namespace. Proxied public TCP also enters tcp().
    remote = None
    try:
        if browser_slots.locked():
            return
        async with browser_slots:
            incoming, remote = await asyncio.wait_for(browser_peer(reader, writer), 10)
            writer.write(b"\x05\x00\x00\x01" + bytes(6))
            await writer.drain()
            await pipe_pair(reader, writer, incoming, remote)
    except (OSError, ValueError, asyncio.IncompleteReadError, asyncio.TimeoutError):
        pass
    finally:
        if remote:
            remote.close()
        writer.close()


async def tcp(reader, writer):
    remote = None
    try:
        if slots.locked():
            return
        async with slots:
            # Linux SO_ORIGINAL_DST returns the sockaddr before OUTPUT redirection.
            target = writer.get_extra_info("socket").getsockopt(socket.SOL_IP, 80, 16)
            address = socket.inet_ntoa(target[4:8])
            port = int.from_bytes(target[2:4], "big")
            dest = f"{address}:{port}"
            incoming, remote, _ = await upstream(
                f"CONNECT {dest} HTTP/1.1\r\nHost: {dest}\r\n\r\n".encode()
            )
            await pipe_pair(reader, writer, incoming, remote)
    except (OSError, ValueError, asyncio.IncompleteReadError, asyncio.TimeoutError):
        pass
    finally:
        if remote:
            remote.close()
        writer.close()


async def dns(query):
    if not query or len(query) > MAX_DNS or slots.locked():
        raise ValueError("invalid DNS request")
    async with slots:
        header = f"POST /dns HTTP/1.1\r\nHost: resolver\r\nConnection: close\r\nContent-Length: {len(query)}\r\n\r\n".encode()
        reader, writer, _ = await upstream(header, query)
        try:
            return await asyncio.wait_for(read_dns(reader), 25)
        finally:
            writer.close()


async def read_dns(reader):
    data = bytearray()
    while part := await reader.read(4096):
        data.extend(part)
        if len(data) > MAX_DNS:
            raise ValueError("oversized DNS answer")
    return bytes(data)


class UDP(asyncio.DatagramProtocol):
    def connection_made(self, transport):
        self.transport = transport

    def datagram_received(self, data, peer):
        if not slots.locked():
            asyncio.create_task(self.reply(data, peer))

    async def reply(self, data, peer):
        try:
            self.transport.sendto(await dns(data), peer)
        except (OSError, ValueError, asyncio.TimeoutError, asyncio.IncompleteReadError):
            pass


async def dns_tcp(reader, writer):
    try:
        size = int.from_bytes(await asyncio.wait_for(reader.readexactly(2), 5), "big")
        reply = await dns(await asyncio.wait_for(reader.readexactly(size), 5))
        writer.write(struct.pack("!H", len(reply)) + reply)
        await writer.drain()
    except (OSError, ValueError, asyncio.IncompleteReadError, asyncio.TimeoutError):
        pass
    finally:
        writer.close()


async def main():
    global AUTH_HEADER
    if STDIN_AUTH:
        token = sys.stdin.buffer.readline().decode("ascii").strip()
        if not token:
            raise ValueError("missing relay authorization")
        AUTH_HEADER = f"Proxy-Authorization: Bearer {token}\r\n".encode("ascii")
    loop = asyncio.get_running_loop()
    await loop.create_datagram_endpoint(UDP, local_addr=("127.0.0.1", 53))
    servers = [
        await asyncio.start_unix_server(browser_connect, LOGIN_SOCKET),
        await asyncio.start_server(dns_tcp, "127.0.0.1", 53),
        await asyncio.start_server(tcp, "127.0.0.1", 15301),
    ]
    os.chmod(LOGIN_SOCKET, 0o660)
    ended = asyncio.Event()

    def controller():
        if not os.read(0, 64):
            ended.set()

    loop.add_reader(0, controller)
    print("ready", os.getpid(), flush=True)
    while not ended.is_set():
        try:
            await asyncio.wait_for(ended.wait(), 90)
        except asyncio.TimeoutError:
            inode = os.stat("/proc/self/ns/net").st_ino
            for pid in os.listdir("/proc"):
                if not pid.isdecimal() or int(pid) == os.getpid():
                    continue
                try:
                    if os.stat(f"/proc/{pid}/ns/net").st_ino == inode:
                        break
                except OSError:
                    continue
            else:
                break
    for server in servers:
        server.close()


asyncio.run(main())
