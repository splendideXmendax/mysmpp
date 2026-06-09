#!/usr/bin/env python3
"""
mysmpp DR/DLR full-flow test stub.

Modes:
  provider     Simulates an upstream HTTP SMS provider and calls mysmpp DLR URL.
  http-submit  Submits messages to mysmpp HTTP API and polls message state.
  smpp-esme    Binds to mysmpp as an ESME, submits MT messages, waits for DLR.

Only Python standard library is used.
"""

from __future__ import annotations

import argparse
import base64
import http.client
import json
import socket
import struct
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


CMD_BIND_TRANSCEIVER = 0x00000009
CMD_SUBMIT_SM = 0x00000004
CMD_DELIVER_SM = 0x00000005
CMD_ENQUIRE_LINK = 0x00000015
CMD_UNBIND = 0x00000006
CMD_BIND_TRANSCEIVER_RESP = 0x80000009
CMD_SUBMIT_SM_RESP = 0x80000004
CMD_DELIVER_SM_RESP = 0x80000005
CMD_ENQUIRE_LINK_RESP = 0x80000015
CMD_UNBIND_RESP = 0x80000006


def cstring(value: str) -> bytes:
    return value.encode("ascii", errors="replace") + b"\x00"


def read_cstring(body: bytes, offset: int) -> tuple[str, int]:
    end = body.find(b"\x00", offset)
    if end < 0:
        end = len(body)
        next_offset = end
    else:
        next_offset = end + 1
    return body[offset:end].decode("ascii", errors="replace"), next_offset


def write_pdu(sock: socket.socket, command_id: int, status: int, seq: int, body: bytes = b"") -> None:
    packet = struct.pack(">IIII", 16 + len(body), command_id, status, seq) + body
    sock.sendall(packet)


def read_exact(sock: socket.socket, n: int) -> bytes:
    chunks = []
    remaining = n
    while remaining > 0:
        data = sock.recv(remaining)
        if not data:
            raise EOFError("socket closed")
        chunks.append(data)
        remaining -= len(data)
    return b"".join(chunks)


def read_pdu(sock: socket.socket) -> tuple[int, int, int, bytes]:
    header = read_exact(sock, 16)
    length, command_id, status, seq = struct.unpack(">IIII", header)
    if length < 16 or length > 1024 * 1024:
        raise ValueError(f"invalid pdu length {length}")
    return command_id, status, seq, read_exact(sock, length - 16)


def encode_text(text: str) -> tuple[int, bytes]:
    try:
        return 0x00, text.encode("ascii")
    except UnicodeEncodeError:
        return 0x08, text.encode("utf-16-be")


def submit_body(src: str, dst: str, text: str, registered_delivery: int = 1) -> bytes:
    data_coding, encoded = encode_text(text)
    if len(encoded) > 254:
        raise ValueError("short_message is too long for this simple stub")
    body = b""
    body += cstring("")                 # service_type
    body += b"\x01\x01" + cstring(src)  # source TON/NPI/address
    body += b"\x01\x01" + cstring(dst)  # destination TON/NPI/address
    body += b"\x00\x00\x00"             # esm_class/protocol_id/priority
    body += cstring("")                 # schedule_delivery_time
    body += cstring("")                 # validity_period
    body += bytes([registered_delivery, 0x00, data_coding, 0x00])
    body += bytes([len(encoded)]) + encoded
    return body


def bind_body(system_id: str, password: str) -> bytes:
    body = b""
    body += cstring(system_id)
    body += cstring(password)
    body += cstring("mysmpp-test")
    body += b"\x34\x00\x00"
    body += cstring("")
    return body


def parse_deliver_sm(body: bytes) -> dict[str, str]:
    offset = 0
    _, offset = read_cstring(body, offset)  # service_type
    offset += 2
    source, offset = read_cstring(body, offset)
    offset += 2
    dest, offset = read_cstring(body, offset)
    offset += 3
    _, offset = read_cstring(body, offset)
    _, offset = read_cstring(body, offset)
    offset += 4
    sm_len = body[offset]
    offset += 1
    text = body[offset : offset + sm_len].decode("ascii", errors="replace")
    return {"from": source, "to": dest, "text": text}


def http_request(method: str, url: str, headers: dict[str, str] | None = None, body: bytes | None = None) -> tuple[int, bytes]:
    parsed = urllib.parse.urlparse(url)
    if parsed.scheme not in ("http", "https"):
        raise ValueError(f"unsupported URL scheme: {parsed.scheme}")
    conn_cls = http.client.HTTPSConnection if parsed.scheme == "https" else http.client.HTTPConnection
    host = parsed.hostname or ""
    port = parsed.port
    conn = conn_cls(host, port, timeout=10)
    path = parsed.path or "/"
    if parsed.query:
        path += "?" + parsed.query
    conn.request(method, path, body=body, headers=headers or {})
    resp = conn.getresponse()
    data = resp.read()
    conn.close()
    return resp.status, data


def post_json(url: str, payload: dict, headers: dict[str, str] | None = None) -> tuple[int, bytes]:
    data = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    merged = {"Content-Type": "application/json"}
    if headers:
        merged.update(headers)
    return http_request("POST", url, merged, data)


class ProviderState:
    def __init__(self, args: argparse.Namespace) -> None:
        self.args = args
        self.lock = threading.Lock()
        self.seq = 0
        self.requests: list[dict] = []

    def next_provider_id(self) -> str:
        with self.lock:
            self.seq += 1
            return f"stub-{int(time.time())}-{self.seq:06d}"

    def remember(self, item: dict) -> None:
        with self.lock:
            self.requests.append(item)


def run_provider(args: argparse.Namespace) -> None:
    state = ProviderState(args)

    class Handler(BaseHTTPRequestHandler):
        server_version = "mysmpp-dr-provider-stub/1.0"

        def log_message(self, fmt: str, *values: object) -> None:
            print("[provider]", fmt % values)

        def do_GET(self) -> None:
            if self.path == "/healthz":
                self.send_response(200)
                self.end_headers()
                self.wfile.write(b'{"status":"ok"}\n')
                return
            if self.path == "/requests":
                with state.lock:
                    body = json.dumps(state.requests, ensure_ascii=False, indent=2).encode("utf-8")
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(body)
                return
            self.send_error(404)

        def do_POST(self) -> None:
            if self.path != args.send_path:
                self.send_error(404)
                return
            length = int(self.headers.get("Content-Length", "0") or "0")
            raw = self.rfile.read(length)
            content_type = self.headers.get("Content-Type", "")
            try:
                if "application/json" in content_type:
                    payload = json.loads(raw.decode("utf-8") or "{}")
                else:
                    parsed = urllib.parse.parse_qs(raw.decode("utf-8"), keep_blank_values=True)
                    payload = {k: v[0] if v else "" for k, v in parsed.items()}
            except Exception as exc:
                self.send_error(400, f"bad request: {exc}")
                return

            provider_id = state.next_provider_id()
            item = {
                "provider_id": provider_id,
                "received_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                "payload": payload,
            }
            state.remember(item)
            print(f"[provider] accepted provider_id={provider_id} payload={payload}")

            threading.Thread(target=send_dlr_later, args=(state, provider_id), daemon=True).start()

            body = json.dumps({"code": 0, "message_id": provider_id, "data": {"message_id": provider_id}}).encode("utf-8")
            self.send_response(args.accept_status)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(body)

    bind = (args.host, args.port)
    print(f"[provider] listening on http://{args.host}:{args.port}{args.send_path}")
    ThreadingHTTPServer(bind, Handler).serve_forever()


def send_dlr_later(state: ProviderState, provider_id: str) -> None:
    time.sleep(state.args.dlr_delay)
    payload = {
        "message_id": provider_id,
        "status": state.args.dlr_status,
        "error_code": state.args.dlr_error_code,
    }
    headers = {state.args.dlr_auth_header: state.args.dlr_auth_token}
    try:
        status, body = post_json(state.args.gateway_dlr_url, payload, headers=headers)
        print(f"[provider] dlr provider_id={provider_id} status={status} body={body.decode('utf-8', errors='replace')}")
    except Exception as exc:
        print(f"[provider] dlr provider_id={provider_id} failed: {exc}")


def run_http_submit(args: argparse.Namespace) -> None:
    headers = {}
    if args.client_id:
        headers["X-Client-ID"] = args.client_id
    if args.token:
        headers["X-Token"] = args.token
    if args.admin_user or args.admin_password:
        raw = f"{args.admin_user}:{args.admin_password}".encode("utf-8")
        headers["Authorization"] = "Basic " + base64.b64encode(raw).decode("ascii")
    submitted = []
    for i in range(args.count):
        client_msg_id = f"{args.client_msg_prefix}-{int(time.time())}-{i + 1}" if args.client_msg_prefix else ""
        payload = {"from": args.src, "to": args.dst, "text": f"{args.text} #{i + 1}"}
        if client_msg_id:
            payload["client_msg_id"] = client_msg_id
        status, body = post_json(args.gateway_messages_url, payload, headers=headers)
        print(f"[http-submit] submit status={status} body={body.decode('utf-8', errors='replace')}")
        if status not in (200, 202):
            continue
        try:
            submitted.append(json.loads(body.decode("utf-8"))["gateway_id"])
        except Exception:
            pass

    deadline = time.time() + args.wait
    while submitted and time.time() < deadline:
        status, body = http_request("GET", args.gateway_messages_url + "?limit=100&offset=0", headers=headers)
        if status != 200:
            print(f"[http-submit] poll status={status} body={body.decode('utf-8', errors='replace')}")
            time.sleep(args.poll_interval)
            continue
        messages = json.loads(body.decode("utf-8"))
        states = {m.get("ID") or m.get("id"): m.get("State") or m.get("state") for m in messages}
        print("[http-submit] states", {gid: states.get(gid) for gid in submitted})
        if all(states.get(gid) == args.expect_state for gid in submitted):
            print("[http-submit] PASS")
            return
        time.sleep(args.poll_interval)
    print("[http-submit] DONE; inspect states above")


def run_smpp_esme(args: argparse.Namespace) -> None:
    sock = socket.create_connection((args.host, args.port), timeout=10)
    sock.settimeout(args.wait + 10)
    seq = 1
    write_pdu(sock, CMD_BIND_TRANSCEIVER, 0, seq, bind_body(args.system_id, args.password))
    cmd, status, _, body = read_pdu(sock)
    if cmd != CMD_BIND_TRANSCEIVER_RESP or status != 0:
        raise SystemExit(f"bind failed cmd=0x{cmd:08x} status=0x{status:08x} body={body!r}")
    print("[smpp-esme] bound")

    submitted = 0
    dlrs = 0
    for i in range(args.count):
        seq += 1
        write_pdu(sock, CMD_SUBMIT_SM, 0, seq, submit_body(args.src, args.dst, f"{args.text} #{i + 1}", 1))
        cmd, status, _, body = read_pdu(sock)
        if cmd != CMD_SUBMIT_SM_RESP or status != 0:
            print(f"[smpp-esme] submit failed cmd=0x{cmd:08x} status=0x{status:08x}")
            continue
        msg_id, _ = read_cstring(body, 0)
        submitted += 1
        print(f"[smpp-esme] submitted gateway_id={msg_id}")

    deadline = time.time() + args.wait
    while time.time() < deadline and dlrs < submitted:
        remaining = max(1, deadline - time.time())
        sock.settimeout(remaining)
        try:
            cmd, status, seq_id, body = read_pdu(sock)
        except socket.timeout:
            break
        if cmd == CMD_DELIVER_SM:
            dlr = parse_deliver_sm(body)
            dlrs += 1
            print(f"[smpp-esme] DLR {dlrs}/{submitted}: {dlr}")
            write_pdu(sock, CMD_DELIVER_SM_RESP, 0, seq_id)
        elif cmd == CMD_ENQUIRE_LINK:
            write_pdu(sock, CMD_ENQUIRE_LINK_RESP, 0, seq_id)
        else:
            print(f"[smpp-esme] ignored cmd=0x{cmd:08x} status=0x{status:08x}")

    print(f"[smpp-esme] submitted={submitted} dlr_received={dlrs}")
    if submitted and dlrs == submitted:
        print("[smpp-esme] PASS")


def main() -> None:
    parser = argparse.ArgumentParser(description="mysmpp DR/DLR full-flow test stub")
    sub = parser.add_subparsers(dest="mode", required=True)

    p = sub.add_parser("provider", help="run upstream HTTP provider stub")
    p.add_argument("--host", default="0.0.0.0")
    p.add_argument("--port", type=int, default=18080)
    p.add_argument("--send-path", default="/send")
    p.add_argument("--accept-status", type=int, default=200)
    p.add_argument("--gateway-dlr-url", required=True, help="e.g. http://GW:19087/callback/stub/dlr")
    p.add_argument("--dlr-auth-header", default="X-Callback-Token")
    p.add_argument("--dlr-auth-token", default="CALLBACK_TOKEN")
    p.add_argument("--dlr-delay", type=float, default=2.0)
    p.add_argument("--dlr-status", default="DELIVRD")
    p.add_argument("--dlr-error-code", type=int, default=0)
    p.set_defaults(func=run_provider)

    h = sub.add_parser("http-submit", help="submit via mysmpp HTTP API and poll state")
    h.add_argument("--gateway-messages-url", required=True, help="e.g. http://GW:19087/v1/messages")
    h.add_argument("--client-id", default="")
    h.add_argument("--token", default="")
    h.add_argument("--admin-user", default="")
    h.add_argument("--admin-password", default="")
    h.add_argument("--src", default="10690000")
    h.add_argument("--dst", default="13800138000")
    h.add_argument("--text", default="hello http dr")
    h.add_argument("--count", type=int, default=1)
    h.add_argument("--wait", type=float, default=20)
    h.add_argument("--poll-interval", type=float, default=2)
    h.add_argument("--expect-state", default="DELIVRD")
    h.add_argument("--client-msg-prefix", default="drtest")
    h.set_defaults(func=run_http_submit)

    s = sub.add_parser("smpp-esme", help="submit via SMPP and wait for deliver_sm DLR")
    s.add_argument("--host", required=True)
    s.add_argument("--port", type=int, default=29175)
    s.add_argument("--system-id", default="dev-esme")
    s.add_argument("--password", default="esmepw1")
    s.add_argument("--src", default="10690000")
    s.add_argument("--dst", default="13800138000")
    s.add_argument("--text", default="hello smpp dr")
    s.add_argument("--count", type=int, default=1)
    s.add_argument("--wait", type=float, default=30)
    s.set_defaults(func=run_smpp_esme)

    args = parser.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
