"""Isolated A2c mock and direct test-binary controller; no application API client.

The caller owns source builds, private MySQL, scenario oracles and resource
cleanup. This module never accepts a shell command, external mock origin, or a
PID supplied by the caller. Errors intentionally contain only fixed codes.
"""
from __future__ import annotations

import dataclasses
import datetime as dt
import hashlib
import hmac
import http.client
import http.server
import json
import os
from pathlib import Path, PurePosixPath
import re
import socket
import stat
import subprocess
import threading
import time
from typing import Any, Mapping

PROTOCOL = "a2c-process-v1"
CA_SOURCE = "64650ecc9e3a672ea49d0ffaf6f417b03fe6ad89"
P_SOURCE = "24da28583eddfaad151b9a4a054264a8275b61bb"
TEST_NAME = "TestMySQLMutationBinaryHandoffProcess"
_POSIX = os.name == "posix"
CASE_IDS = tuple(
    [f"L1-{a}" for a in ("cancel", "delete")]
    + [f"L2-{a}-{c}" for a in ("cancel", "delete") for c in ("Poff", "Pon")]
    + [f"{n}-{a}-{c}" for n in ("N1", "N2", "N3") for a in ("cancel", "delete") for c in ("Ca", "Poff")]
    + ["N1-wall-cancel-Ca", "N3-delete-404-Ca", "N4-cancel-502-Ca", "N4-delete-EOF-Poff"]
    + [f"{n}-{a}-Ca" for n in ("R1", "R2") for a in ("cancel", "delete")]
    + [f"{n}-{a}" for n in ("M1", "M2") for a in ("cancel", "delete")]
    + [f"M3-{a}-{i}" for a in ("cancel", "delete") for i in (1, 2, 3)]
    + ["Q1-cancel-succeeded-first", "Q1-cancel-cancelled-first", "N3-cancel-both-flags-Ca"]
)
assert len(CASE_IDS) == len(set(CASE_IDS)) == 39
_NAME = re.compile(r"[a-z][a-z0-9_-]{0,47}\Z")
_HEX64 = re.compile(r"[0-9a-f]{64}\Z")
_ID = re.compile(r"[A-Za-z0-9_-]{1,128}\Z")
_UINT = re.compile(r"[1-9][0-9]{0,19}\Z")
_TIME = re.compile(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z\Z")
_BARRIERS = {"none", "start", "registered_before_http", "inbox_durable_before_apply", "legacy_claim_before_http", "legacy_receipt_durable"}
_RESPONSES = {"ack204", "not_found404", "upstream502", "eof", "hold"}
_ERRORS = {"startup_invalid", "config_invalid", "database_unavailable", "invalid_command", "business_failed", "barrier_failed", "barrier_timeout", "inspect_failed", "invariant_failed", "cleanup_failed"}


class ProtocolError(RuntimeError):
    def __init__(self, code: str):
        self.code = code
        super().__init__("a2c:" + code)


def _require(value: Any, code: str) -> None:
    if not value:
        raise ProtocolError(code)


def _pairs(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    out: dict[str, Any] = {}
    for key, value in pairs:
        _require(key not in out, "duplicate_json_field")
        out[key] = value
    return out


def _json(data: bytes, limit: int = 65536) -> dict[str, Any]:
    _require(len(data) <= limit and not data.startswith(b"\xef\xbb\xbf"), "invalid_json")
    try:
        out = json.loads(data.decode("utf-8", "strict"), object_pairs_hook=_pairs,
                         parse_constant=lambda _: (_ for _ in ()).throw(ProtocolError("invalid_json")))
    except (UnicodeError, ValueError, RecursionError):
        raise ProtocolError("invalid_json") from None
    _require(isinstance(out, dict), "invalid_json")
    return out


def _encoded(value: Any) -> bytes:
    return json.dumps(value, ensure_ascii=True, allow_nan=False, separators=(",", ":")).encode("ascii")


def _keys(value: Mapping[str, Any], names: set[str]) -> None:
    _require(set(value) == names, "invalid_fields")


def _integer(value: Any, low: int = 0, high: int = 2**31 - 1) -> bool:
    return type(value) is int and low <= value <= high


def _enum(value: Any, choices) -> bool:
    return isinstance(value, str) and value in choices


def _timestamp(value: Any) -> dt.datetime:
    _require(isinstance(value, str) and _TIME.fullmatch(value), "invalid_time")
    try:
        return dt.datetime.strptime(value, "%Y-%m-%dT%H:%M:%S.%fZ").replace(tzinfo=dt.timezone.utc)
    except ValueError:
        raise ProtocolError("invalid_time") from None


def _identity(run_id: str, case_id: str) -> tuple[str, str]:
    _require(isinstance(run_id, str) and re.fullmatch(r"[0-9a-f]{16}", run_id), "invalid_run")
    _require(case_id in CASE_IDS, "invalid_case")
    digest = hashlib.sha256(case_id.encode("ascii")).hexdigest()[:12]
    return "a2c_" + run_id + "_" + digest, "ark_test_action_" + run_id + "_" + digest


def _secret(value: Any) -> bool:
    return isinstance(value, str) and 32 <= len(value.encode("utf-8")) <= 4096 and not any(c in value for c in "\r\n\x00")


def _safe_root(path: Path) -> Path:
    path = Path(path)
    _require(path.is_absolute() and path.exists(), "invalid_artifact_root")
    current = path
    while current != current.parent:
        _require(not current.is_symlink(), "unsafe_artifact_path")
        current = current.parent
    _require(path.is_dir(), "invalid_artifact_root")
    if os.name != "nt":
        st = path.stat()
        _require(st.st_uid == os.getuid() and stat.S_IMODE(st.st_mode) & 0o077 == 0, "unsafe_artifact_permissions")
    return path.resolve()


def _artifact_path(root: Path, relative: str, existing: bool = True) -> Path:
    _require(isinstance(relative, str) and "\\" not in relative and ":" not in relative, "unsafe_artifact_path")
    parsed = PurePosixPath(relative)
    _require(relative and not parsed.is_absolute() and str(parsed) == relative and all(p not in ("", ".", "..") for p in parsed.parts), "unsafe_artifact_path")
    path = root.joinpath(*parsed.parts)
    current = root
    for part in parsed.parts:
        current = current / part
        _require(not current.is_symlink(), "unsafe_artifact_path")
    _require(path.resolve().is_relative_to(root.resolve()), "unsafe_artifact_path")
    if existing:
        _require(path.is_file(), "artifact_missing")
    return path


def read_verified_artifact(case_dir: Path, relative: str, expected_sha256: str) -> dict[str, Any]:
    root = _safe_root(case_dir)
    _require(isinstance(expected_sha256, str) and _HEX64.fullmatch(expected_sha256), "invalid_artifact_hash")
    path = _artifact_path(root, relative)
    try:
        flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
        fd = os.open(path, flags)
        with os.fdopen(fd, "rb") as source:
            st = os.fstat(source.fileno())
            _require(stat.S_ISREG(st.st_mode) and st.st_size <= 2 << 20, "artifact_too_large")
            if os.name != "nt":
                _require(st.st_uid == os.getuid() and stat.S_IMODE(st.st_mode) & 0o077 == 0, "unsafe_artifact_permissions")
            data = source.read((2 << 20) + 1)
    except OSError:
        raise ProtocolError("artifact_unavailable") from None
    _require(hmac.compare_digest(hashlib.sha256(data).hexdigest(), expected_sha256), "artifact_hash_mismatch")
    return _json(data, 2 << 20)


def _private_file(path: Path):
    try:
        return os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o600), "wb")
    except OSError:
        raise ProtocolError("artifact_create_failed") from None


@dataclasses.dataclass(frozen=True)
class BinarySpec:
    path: Path
    source_sha: str
    sha256: str

    def verify(self) -> Path:
        path = Path(self.path)
        _require(path.is_absolute() and path.is_file() and not path.is_symlink(), "invalid_binary")
        _require(self.source_sha in (CA_SOURCE, P_SOURCE) and _HEX64.fullmatch(self.sha256), "invalid_binary_identity")
        try:
            with path.open("rb") as source:
                actual = hashlib.file_digest(source, "sha256").hexdigest()
        except OSError:
            raise ProtocolError("binary_unavailable") from None
        _require(hmac.compare_digest(actual, self.sha256), "binary_hash_mismatch")
        return path


class _MockServer(http.server.ThreadingHTTPServer):
    daemon_threads = True
    block_on_close = False
    allow_reuse_address = False

    def handle_error(self, request, client_address):
        # BaseServer otherwise prints a traceback, including arbitrary parser
        # input, to stderr. Preserve only a fixed failure marker.
        with self.owner._lock:
            self.owner._error = self.owner._error or "mock_handler_failed"
            self.owner._lock.notify_all()


class MockArk:
    def __init__(self, run_id: str, case_id: str, provider_token: str, control_token: str):
        self.upstream_id, _ = _identity(run_id, case_id)
        _require(_secret(provider_token) and _secret(control_token) and not hmac.compare_digest(provider_token, control_token), "invalid_mock_secret")
        self.run_id, self.case_id = run_id, case_id
        self._provider, self._control = provider_token, control_token
        self._lock = threading.Condition()
        self._requests: list[dict[str, Any]] = []
        self._pending: dict[int, dict[str, Any]] = {}
        self._threads: set[threading.Thread] = set()
        self._query = ["queued"]
        self._query_index = 0
        self._delete = "ack204"
        self._error: str | None = None
        self._server: _MockServer | None = None
        self._thread: threading.Thread | None = None
        self.origin = ""

    def start(self) -> "MockArk":
        _require(self._server is None, "mock_already_started")
        owner = self

        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *_):
                pass

            def handle(self):
                with owner._lock:
                    owner._threads.add(threading.current_thread())
                self.connection.settimeout(5)
                try:
                    super().handle()
                finally:
                    with owner._lock:
                        owner._threads.discard(threading.current_thread())
                        owner._lock.notify_all()

            def do_POST(self):
                owner._handle(self)

            do_GET = do_POST
            do_DELETE = do_POST
            do_PUT = do_POST
            do_PATCH = do_POST
            do_HEAD = do_POST

            def __getattr__(self, name):
                if name.startswith("do_"):
                    return self.do_POST
                raise AttributeError(name)

        try:
            self._server = _MockServer(("127.0.0.1", 0), Handler)
            self._server.owner = self
            self.origin = f"http://127.0.0.1:{self._server.server_port}"
            self._thread = threading.Thread(target=self._server.serve_forever, name="a2c-mock", daemon=True)
            self._thread.start()
        except OSError:
            raise ProtocolError("mock_start_failed") from None
        return self

    def _reply(self, handler, status: int, body: dict[str, Any] | None, request_seq: int = 0):
        data = b"" if body is None else _encoded(body)
        try:
            handler.send_response(status)
            if body is not None:
                handler.send_header("Content-Type", "application/json")
            handler.send_header("Content-Length", str(len(data)))
            handler.send_header("Connection", "close")
            if request_seq:
                handler.send_header("X-Request-Id", f"a2c-request-{request_seq}")
            handler.end_headers()
            if data:
                handler.wfile.write(data)
                handler.wfile.flush()
        except (OSError, ValueError):
            with self._lock:
                self._error = self._error or "mock_response_failed"
        handler.close_connection = True

    def _body(self, handler, limit: int) -> bytes:
        _require(handler.headers.get_all("Transfer-Encoding") is None, "invalid_mock_request")
        values = handler.headers.get_all("Content-Length", [])
        _require(len(values) == 1 and re.fullmatch(r"0|[1-9][0-9]*", values[0]), "invalid_mock_request")
        size = int(values[0])
        _require(size <= limit, "invalid_mock_request")
        body = handler.rfile.read(size)
        _require(len(body) == size, "invalid_mock_request")
        return body

    def _header(self, handler, name: str, expected: str) -> bool:
        values = handler.headers.get_all(name, [])
        return len(values) == 1 and hmac.compare_digest(values[0], expected)

    def _handle(self, handler):
        is_control = handler.path.startswith("/__a2c/")
        try:
            if is_control:
                _require(self._header(handler, "Authorization", "Bearer " + self._control), "mock_control_denied")
                if handler.path == "/__a2c/status" and handler.command == "GET":
                    self._reply(handler, 200, self._status())
                    return
                _require(handler.path == "/__a2c/control" and handler.command == "POST", "invalid_mock_control")
                self._apply_control(_json(self._body(handler, 4096), 4096))
                self._reply(handler, 200, {"ok": True})
                return
            _require(self._header(handler, "Authorization", "Bearer " + self._provider), "invalid_mock_request")
            _require(self._header(handler, "X-A2C-Run", self.run_id) and self._header(handler, "X-A2C-Case", self.case_id), "invalid_mock_request")
            instance = handler.headers.get_all("X-A2C-Instance", [])
            _require(len(instance) == 1 and _NAME.fullmatch(instance[0]), "invalid_mock_request")
            base = "/api/v3/contents/generations/tasks"
            method = handler.command
            _require((method == "POST" and handler.path == base) or (method in ("GET", "DELETE") and handler.path == base + "/" + self.upstream_id), "invalid_mock_request")
            if method == "POST":
                _json(self._body(handler, 1 << 20), 1 << 20)
            else:
                _require(handler.headers.get("Content-Length", "0") == "0" and handler.headers.get("Transfer-Encoding") is None, "invalid_mock_request")
            with self._lock:
                _require(len(self._requests) < 100, "mock_request_limit")
                seq = len(self._requests) + 1
                outcome = "accepted" if method == "POST" else self._delete
                if method == "GET":
                    outcome = self._query[min(self._query_index, len(self._query) - 1)]
                    self._query_index += 1
                row = {"request_seq": seq, "instance": instance[0], "method": method, "outcome": outcome, "waiting": outcome == "hold", "responded": False}
                self._requests.append(row)
                if method == "DELETE" and outcome == "hold":
                    pending = {"row": row, "response": None, "socket": handler.connection}
                    self._pending[seq] = pending
                    self._lock.notify_all()
                    until = time.monotonic() + 60
                    while pending["response"] is None and time.monotonic() < until:
                        self._lock.wait(min(1, max(0, until - time.monotonic())))
                    _require(pending["response"] is not None, "mock_hold_timeout")
                    outcome = pending["response"]
                    row["waiting"] = False
                    row["outcome"] = outcome
                    del self._pending[seq]
                self._lock.notify_all()
            if outcome in ("eof", "dropped"):
                handler.close_connection = True
                try:
                    handler.connection.shutdown(socket.SHUT_RDWR)
                except OSError:
                    pass
                return
            if method == "POST":
                status, body = 200, {"id": self.upstream_id}
            elif method == "GET":
                status, body = 200, {"id": self.upstream_id, "status": outcome}
                if outcome == "succeeded":
                    body["usage"] = {"completion_tokens": 100, "total_tokens": 100}
            elif outcome == "ack204":
                status, body = 204, None
            elif outcome == "not_found404":
                status, body = 404, {"error": {"code": "TaskNotFound", "message": "synthetic missing"}}
            else:
                status, body = 502, {"error": {"code": "SyntheticUnavailable", "message": "synthetic upstream failure"}}
            self._reply(handler, status, body, seq)
            with self._lock:
                row["responded"] = True
                self._lock.notify_all()
        except (ProtocolError, OSError, ValueError, TypeError):
            if not is_control:
                with self._lock:
                    self._error = self._error or "mock_request_rejected"
                    self._lock.notify_all()
            self._reply(handler, 409, {"error": "invalid_control" if is_control else "invalid_request"})

    def _status(self) -> dict[str, Any]:
        with self._lock:
            _require(self._error is None, self._error or "mock_failed")
            counts = {"post": 0, "get": 0, "delete": 0}
            instances: dict[str, dict[str, int]] = {}
            for row in self._requests:
                key = row["method"].lower()
                counts[key] += 1
                instances.setdefault(row["instance"], {"post": 0, "get": 0, "delete": 0})[key] += 1
            return {"protocol": PROTOCOL, "run_id": self.run_id, "case_id": self.case_id, "seq": len(self._requests), **counts,
                    "by_instance": instances, "pending_deletes": sorted(self._pending), "requests": [dict(r) for r in self._requests]}

    def _apply_control(self, body: dict[str, Any]):
        command = body.get("command")
        with self._lock:
            if command == "set":
                _keys(body, {"command", "query_sequence", "delete_response"})
                values = body["query_sequence"]
                _require(isinstance(values, list) and 1 <= len(values) <= 3 and all(v in ("queued", "succeeded", "cancelled") for v in values), "invalid_mock_control")
                _require(_enum(body["delete_response"], _RESPONSES), "invalid_mock_control")
                self._query, self._query_index, self._delete = list(values), 0, body["delete_response"]
            elif command in ("release_delete", "drop_waiting"):
                _keys(body, {"command", "request_seq", "response"} if command == "release_delete" else {"command", "request_seq"})
                seq = body["request_seq"]
                _require(_integer(seq, 1, 100) and seq in self._pending and self._pending[seq]["response"] is None, "invalid_mock_control")
                response = body.get("response", "dropped")
                _require(_enum(response, _RESPONSES - {"hold"}) if command == "release_delete" else response == "dropped", "invalid_mock_control")
                self._pending[seq]["response"] = response
                self._lock.notify_all()
            else:
                raise ProtocolError("invalid_mock_control")

    def _control_request(self, body: dict[str, Any] | None) -> dict[str, Any]:
        _require(self._server is not None, "mock_not_started")
        connection = http.client.HTTPConnection("127.0.0.1", self._server.server_port, timeout=5)
        try:
            data = None if body is None else _encoded(body)
            connection.request("GET" if body is None else "POST", "/__a2c/status" if body is None else "/__a2c/control", body=data,
                               headers={"Authorization": "Bearer " + self._control, "Content-Type": "application/json"})
            response = connection.getresponse()
            result = _json(response.read(65537))
            _require(response.status == 200, "mock_control_failed")
            return result
        except (OSError, http.client.HTTPException):
            raise ProtocolError("mock_control_failed") from None
        finally:
            connection.close()

    def set(self, query_sequence: list[str], delete_response: str):
        self._control_request({"command": "set", "query_sequence": query_sequence, "delete_response": delete_response})

    def status(self) -> dict[str, Any]:
        return self._control_request(None)

    def wait_delete(self, instance: str, timeout: float = 60) -> dict[str, Any]:
        _require(_NAME.fullmatch(instance) and 0 < timeout <= 60, "invalid_wait")
        until = time.monotonic() + timeout
        while time.monotonic() < until:
            status = self.status()
            for row in status["requests"]:
                if row["instance"] == instance and row["method"] == "DELETE":
                    return row
            time.sleep(min(0.1, max(0, until - time.monotonic())))
        raise ProtocolError("mock_delete_timeout")

    def release_delete(self, seq: int, response: str):
        self._control_request({"command": "release_delete", "request_seq": seq, "response": response})

    def drop_waiting(self, seq: int, exited_process: "ProcessController"):
        _require(isinstance(exited_process, ProcessController) and exited_process.returncode == -9 and exited_process.expected_crash_verified and exited_process.run_id == self.run_id and exited_process.case_id == self.case_id, "drop_requires_verified_kill")
        status = self.status()
        _require(any(r["request_seq"] == seq and r["instance"] == exited_process.instance and r["waiting"] for r in status["requests"]), "invalid_waiting_request")
        self._control_request({"command": "drop_waiting", "request_seq": seq})

    def close(self, timeout: float = 5):
        _require(0 < timeout <= 10, "invalid_wait")
        if self._server is None:
            return
        until = time.monotonic() + timeout
        with self._lock:
            pending_on_close = bool(self._pending)
            for item in self._pending.values():
                item["response"] = "dropped"
            self._lock.notify_all()
        self._server.shutdown()
        self._server.server_close()
        if self._thread:
            self._thread.join(max(0, until - time.monotonic()))
        with self._lock:
            while self._threads and time.monotonic() < until:
                self._lock.wait(max(0, until - time.monotonic()))
            alive, error = bool(self._threads), self._error
        self._server = None
        _require(not alive, "mock_cleanup_timeout")
        _require(not pending_on_close, "mock_unreleased_request")
        _require(error is None, error or "mock_failed")


_A2C_ENV = {"A2C_PROTOCOL", "A2C_RUN_ID", "A2C_CASE_ID", "A2C_INSTANCE", "A2C_MODE", "A2C_BUILD_ROLE", "A2C_SOURCE_SHA", "A2C_CASE_DIR", "A2C_CLOCK_MODE", "A2C_INITIAL_TIME", "A2C_MOCK_ORIGIN", "A2C_UPSTREAM_ID", "A2C_PROVIDER_TOKEN"}
_LAIAI_ENV = {"LAIAI_DSN", "LAIAI_AUTH_SECRET", "LAIAI_DATA_KEYS", "LAIAI_DATA_KEY_ACTIVE", "LAIAI_VIDEO_HMAC_SECRET", "LAIAI_VIDEO_RESPONSE_INBOX", "LAIAI_VIDEO_MUTATION_RESPONSE_INBOX", "LAIAI_DB_MAX_OPEN_CONNS", "LAIAI_DB_MAX_IDLE_CONNS"}


def _child_environment(values: Mapping[str, str], case_dir: Path, binary: BinarySpec) -> dict[str, str]:
    _require(set(values) <= _A2C_ENV | _LAIAI_ENV and _A2C_ENV <= set(values), "invalid_environment")
    required = _LAIAI_ENV - {"LAIAI_VIDEO_MUTATION_RESPONSE_INBOX"}
    _require(required <= set(values) and all(isinstance(v, str) and "\x00" not in v for v in values.values()), "invalid_environment")
    _require(values["A2C_PROTOCOL"] == PROTOCOL and values["A2C_CASE_DIR"] == str(case_dir), "invalid_environment")
    upstream, database = _identity(values["A2C_RUN_ID"], values["A2C_CASE_ID"])
    role = values["A2C_BUILD_ROLE"]
    _require(role in ("Ca", "Poff", "Pon", "Pboth") and values["A2C_MODE"] in ("seed", "worker", "inspect") and _NAME.fullmatch(values["A2C_INSTANCE"]), "invalid_environment")
    _require(binary.source_sha == (CA_SOURCE if role == "Ca" else P_SOURCE) == values["A2C_SOURCE_SHA"], "source_role_mismatch")
    _require(values["A2C_CLOCK_MODE"] == ("wall" if values["A2C_CASE_ID"] == "N1-wall-cancel-Ca" else "logical"), "invalid_environment")
    _timestamp(values["A2C_INITIAL_TIME"])
    _require(values["A2C_UPSTREAM_ID"] == upstream and re.fullmatch(r"http://127\.0\.0\.1:([1-9][0-9]{0,4})", values["A2C_MOCK_ORIGIN"]), "invalid_environment")
    _require(int(values["A2C_MOCK_ORIGIN"].rsplit(":", 1)[1]) <= 65535, "invalid_environment")
    _require(all(_secret(values[k]) for k in ("A2C_PROVIDER_TOKEN", "LAIAI_AUTH_SECRET", "LAIAI_VIDEO_HMAC_SECRET")), "invalid_environment")
    _require(values["LAIAI_DATA_KEYS"] and values["LAIAI_DATA_KEY_ACTIVE"], "invalid_environment")
    dsn = values["LAIAI_DSN"]
    # The runner creates credentials with safe ASCII, so no alternative DSN
    # transports, Unix sockets, escaped schema names or external endpoints exist.
    pattern = r"[A-Za-z0-9_]+:[A-Za-z0-9_-]+@tcp\(127\.0\.0\.1:([1-9][0-9]{0,4})\)/" + re.escape(database) + r"\?(.+)"
    match = re.fullmatch(pattern, dsn)
    _require(match is not None and int(match[1]) <= 65535, "invalid_private_dsn")
    params = match[2].split("&")
    parsed: dict[str, str] = {}
    for item in params:
        pair = item.split("=", 1)
        _require(len(pair) == 2 and pair[0] not in parsed, "invalid_private_dsn")
        parsed[pair[0]] = pair[1]
    _require(parsed.get("parseTime") == "true" and parsed.get("loc") == "UTC" and set(parsed) <= {"parseTime", "loc", "timeout", "readTimeout", "writeTimeout", "charset"}, "invalid_private_dsn")
    for key in ("timeout", "readTimeout", "writeTimeout"):
        if key in parsed:
            _require(re.fullmatch(r"[1-5]s", parsed[key]), "invalid_private_dsn")
    if "charset" in parsed:
        _require(parsed["charset"] == "utf8mb4", "invalid_private_dsn")
    _require(values["LAIAI_DB_MAX_OPEN_CONNS"] == "8" and values["LAIAI_DB_MAX_IDLE_CONNS"] == "4", "invalid_environment")
    _require(values["LAIAI_VIDEO_RESPONSE_INBOX"] == ("true" if role == "Pboth" else "false"), "invalid_flags")
    if role == "Ca":
        _require("LAIAI_VIDEO_MUTATION_RESPONSE_INBOX" not in values, "invalid_flags")
    else:
        _require(values.get("LAIAI_VIDEO_MUTATION_RESPONSE_INBOX") == ("false" if role == "Poff" else "true"), "invalid_flags")
    child = {key: os.environ[key] for key in ("PATH", "HOME", "TMPDIR") if key in os.environ}
    child.update({"TZ": "UTC", "LANG": "C.UTF-8"})
    child.update(values)
    return child


class ProcessController:
    def __init__(self, binary: BinarySpec, go_binary: Path, case_dir: Path, env: Mapping[str, str]):
        self.case_dir = _safe_root(Path(case_dir))
        self.binary = binary
        self._binary_path = binary.verify()
        self._go = Path(go_binary)
        _require(self._go.is_absolute() and self._go.is_file() and not self._go.is_symlink(), "invalid_go_binary")
        self._env = _child_environment(env, self.case_dir, binary)
        self.instance = env["A2C_INSTANCE"]
        self.run_id, self.case_id, self.role, self.mode = (env[k] for k in ("A2C_RUN_ID", "A2C_CASE_ID", "A2C_BUILD_ROLE", "A2C_MODE"))
        self._condition = threading.Condition()
        self._write_lock = threading.Lock()
        self._events: list[dict[str, Any]] = []
        self._commands: dict[int, dict[str, Any]] = {}
        self._done: set[int] = set()
        self._seq = 0
        self._ready = False
        self._stopped = False
        self._run_active: int | None = None
        self._barrier: dict[str, Any] | None = None
        self._error: ProtocolError | None = None
        self._threads: list[threading.Thread] = []
        self._files: dict[str, Any] = {}
        self._paths: dict[str, Path] = {}
        self._proc: subprocess.Popen | None = None
        self._converter: subprocess.Popen | None = None
        self._closed = False
        self.expected_crash_verified = False
        self._aborted = False
        self._test_run = self._test_pass = False
        self._test_bad = False
        self._stdout_done = False
        self._current_clock = env["A2C_INITIAL_TIME"]
        self._owns_artifact_dir = False

    @property
    def pid(self) -> int | None:
        return None if self._proc is None else self._proc.pid

    @property
    def returncode(self) -> int | None:
        return None if self._proc is None else self._proc.poll()

    def _fail(self, code: str):
        with self._condition:
            if self._error is None:
                self._error = ProtocolError(code)
            self._condition.notify_all()
        if self._proc is not None and self._proc.poll() is None:
            try:
                self._proc.kill()
            except OSError:
                pass

    def start(self, timeout: float = 15) -> dict[str, Any]:
        _require(self._proc is None and not self._closed and 0 < timeout <= 15, "invalid_start")
        self.binary.verify()  # Recheck immediately before direct execution.
        try:
            processes = self.case_dir / "processes"
            if not processes.exists():
                processes.mkdir(mode=0o700)
            _safe_root(processes)
            self._dir = processes / self.instance
            self._dir.mkdir(mode=0o700)
            self._owns_artifact_dir = True
            for key, name in (("stdout", "stdout.log"), ("stderr", "stderr.log"), ("json", "test2json.jsonl"), ("events", "events.jsonl"), ("converter_stderr", "test2json.stderr.log")):
                self._files[key] = _private_file(self._dir / name)
                self._paths[key] = self._dir / name
            converter_env = {k: self._env[k] for k in ("PATH", "HOME", "TMPDIR", "TZ", "LANG") if k in self._env}
            self._converter = subprocess.Popen([str(self._go), "tool", "test2json", "-p", self.role, "-t"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=converter_env, shell=False)
            self._proc = subprocess.Popen([str(self._binary_path), "-test.run=^" + TEST_NAME + "$", "-test.v", "-test.count=1", "-test.timeout=12m"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=self._env, shell=False)
            for name, stream in (("stdout", self._proc.stdout), ("stderr", self._proc.stderr), ("json", self._converter.stdout), ("converter_stderr", self._converter.stderr)):
                thread = threading.Thread(target=self._read, args=(name, stream), daemon=True, name="a2c-" + name)
                self._threads.append(thread)
                thread.start()
            return self.wait_event("ready", cmd_id=0, timeout=timeout)
        except ProtocolError:
            self.abort_owned()
            raise
        except (OSError, ValueError):
            self.abort_owned()
            raise ProtocolError("process_start_failed") from None

    def _read(self, kind: str, stream):
        total = 0
        limit = (32 if kind in ("stdout", "json") else 8) << 20
        try:
            while True:
                data = stream.readline(65538)
                if not data:
                    break
                total += len(data)
                _require(total <= limit and len(data) <= 65537 and data.endswith(b"\n"), "process_output_limit")
                for key in ("A2C_PROVIDER_TOKEN", "LAIAI_AUTH_SECRET", "LAIAI_VIDEO_HMAC_SECRET", "LAIAI_DSN", "LAIAI_DATA_KEYS"):
                    secret = self._env[key].encode("utf-8")
                    _require(secret not in data, "secret_in_process_output")
                self._files[kind].write(data)
                self._files[kind].flush()
                if kind == "stdout":
                    if data.startswith(b"A2C_EVENT "):
                        self._accept_event(_json(data[len(b"A2C_EVENT "):].rstrip(b"\r\n")))
                    self._converter.stdin.write(data)
                    self._converter.stdin.flush()
                elif kind == "json":
                    value = _json(data.rstrip(b"\r\n"))
                    if value.get("Action") == "run" and value.get("Test") == TEST_NAME:
                        self._test_run = True
                    if value.get("Action") == "pass" and value.get("Test") == TEST_NAME:
                        self._test_pass = True
                    if value.get("Action") in ("skip", "fail"):
                        self._test_bad = True
        except ProtocolError as error:
            self._fail(error.code)
        except (OSError, ValueError, TypeError, KeyError):
            self._fail("process_output_failed")
        finally:
            if kind == "stdout" and self._converter and self._converter.stdin:
                try:
                    self._converter.stdin.close()
                except OSError:
                    pass
            with self._condition:
                if kind == "stdout":
                    self._stdout_done = True
                self._condition.notify_all()

    def _validate_event_data(self, event: dict[str, Any]):
        kind, data, cmd_id = event["event"], event["data"], event["cmd_id"]
        _require(isinstance(kind, str) and isinstance(data, dict), "invalid_event")
        fields = {
            "ready": {"source_sha", "clock_mode", "clock", "db_time", "create_inbox", "mutation_field_present", "mutation_inbox"} | ({"org_id", "task_id", "operation_id"} if self.mode != "seed" else set()),
            "seeded": {"seed_file", "seed_sha256", "org_id", "key_id", "task_id", "operation_id", "action", "clock", "upstream_id", "baseline_financial_sha256"},
            "pending_intent": {"org_id", "task_id", "operation_id", "action", "operation_status", "mutation_attempt_count", "receipt_count"},
            "run_started": {"barrier", "clock"},
            "barrier_reached": {"barrier", "owner_cmd_id", "verification_file", "verification_sha256", "clock", "db_time"},
            "clock_advanced": {"previous", "to", "db_time"},
            "purged": {"input_payload_ref", "purged_count", "before_identity_sha256", "after_identity_sha256", "wrapped_key_present", "nonce_present", "staging_count", "capture_status"},
            "inspected": {"snapshot_file", "snapshot_sha256"},
            "command_done": {"command"} | ({"clock", "db_time", "process_once_returned"} if data.get("command") == "run_once" else set()),
            "stopped": {"completed_commands"}, "error": {"code"},
        }
        _require(kind in fields and isinstance(data, dict), "invalid_event")
        _keys(data, fields[kind])
        for key, value in data.items():
            if key in ("clock", "db_time", "previous", "to"):
                _timestamp(value)
            elif key.endswith("sha256"):
                _require(isinstance(value, str) and _HEX64.fullmatch(value), "invalid_event")
            elif key in ("org_id", "key_id"):
                _require(isinstance(value, str) and _UINT.fullmatch(value) and int(value) < 2**64, "invalid_event")
            elif key in ("task_id", "operation_id", "upstream_id", "input_payload_ref"):
                _require(isinstance(value, str) and _ID.fullmatch(value), "invalid_event")
        if kind == "ready":
            _require(cmd_id == 0 and not self._ready and self._seq == 0, "duplicate_ready")
            _require(data["source_sha"] == self.binary.source_sha and data["clock_mode"] == self._env["A2C_CLOCK_MODE"], "invalid_ready")
            _require(data["create_inbox"] is (self.role == "Pboth") and data["mutation_field_present"] is (self.role != "Ca") and data["mutation_inbox"] is (None if self.role == "Ca" else self.role in ("Pon", "Pboth")), "invalid_ready")
            if self._env["A2C_CLOCK_MODE"] == "logical":
                _require(data["clock"] == self._current_clock, "invalid_ready")
            self._ready = True
        elif kind == "error":
            _require(_enum(data["code"], _ERRORS) and (cmd_id == 0 or cmd_id in self._commands), "invalid_event")
            raise ProtocolError("harness_" + data["code"])
        else:
            _require(self._ready and cmd_id in self._commands and (cmd_id not in self._done or kind == "stopped"), "invalid_event_command")
            command = self._commands[cmd_id]
            expected = {"seeded": "seed", "pending_intent": "seed", "run_started": "run_once", "barrier_reached": "run_once", "clock_advanced": "advance_clock", "purged": "purge_expired", "inspected": "inspect", "stopped": "stop"}
            _require(kind == "command_done" or command["command"] == expected[kind], "invalid_event_command")
            _require(not any(e["event"] == kind and e["cmd_id"] == cmd_id for e in self._events), "duplicate_event")
            if kind == "run_started":
                _require(data["barrier"] == command["barrier"], "invalid_barrier")
            elif kind == "barrier_reached":
                _require(data["barrier"] == command["barrier"] != "none" and _integer(data["owner_cmd_id"], 1) and data["owner_cmd_id"] == cmd_id and self._barrier is None, "invalid_barrier")
                _require(any(e["event"] == "run_started" and e["cmd_id"] == cmd_id for e in self._events), "missing_command_evidence")
                self._verify_event_artifact(data["verification_file"], data["verification_sha256"])
                self._barrier = event
            elif kind == "seeded":
                _require(data["seed_file"] == "seed.json" and data["upstream_id"] == self._env["A2C_UPSTREAM_ID"] and data["action"] == ("delete" if "-delete" in self.case_id else "cancel"), "invalid_seed")
                self._verify_event_artifact("seed.json", data["seed_sha256"])
            elif kind == "pending_intent":
                _require(data["action"] == ("delete" if "-delete" in self.case_id else "cancel") and data["operation_status"] == "pending" and type(data["mutation_attempt_count"]) is int and data["mutation_attempt_count"] == 0 and type(data["receipt_count"]) is int and data["receipt_count"] == 0, "invalid_pending")
            elif kind == "clock_advanced":
                _require(data["to"] == command["to"] and _timestamp(data["previous"]) <= _timestamp(data["to"]), "invalid_time")
                self._current_clock = data["to"]
            elif kind == "purged":
                _require(_integer(data["purged_count"]) and data["wrapped_key_present"] is False and data["nonce_present"] is False and type(data["staging_count"]) is int and data["staging_count"] == 0 and data["capture_status"] == "expired", "invalid_purge")
            elif kind == "inspected":
                expected_file = f"snapshots/{self.instance}-{cmd_id}-{command['label']}.json"
                _require(data["snapshot_file"] == expected_file, "invalid_snapshot_path")
                self._verify_event_artifact(data["snapshot_file"], data["snapshot_sha256"])
            elif kind == "stopped":
                _require(not self._stopped and cmd_id in self._done and _integer(data["completed_commands"], len(self._commands), len(self._commands)), "invalid_stop")
                self._stopped = True
            elif kind == "command_done":
                _require(data["command"] == command["command"], "invalid_event_command")
                required = {"seed": {"seeded", "pending_intent"}, "run_once": {"run_started"}, "advance_clock": {"clock_advanced"}, "inspect": {"inspected"}, "purge_expired": {"purged"}}.get(command["command"], set())
                _require(required <= {e["event"] for e in self._events if e["cmd_id"] == cmd_id}, "missing_command_evidence")
                if command["command"] == "run_once":
                    _require(data["process_once_returned"] is True and self._run_active == cmd_id and self._barrier is None, "invalid_completion")
                    self._run_active = None
                self._done.add(cmd_id)

    def _verify_event_artifact(self, relative: str, digest: str) -> dict[str, Any]:
        out = read_verified_artifact(self.case_dir, relative, digest)
        _require(out.get("protocol") == PROTOCOL and out.get("run_id") == self.run_id and out.get("case_id") == self.case_id, "artifact_identity_mismatch")
        return out

    def _accept_event(self, event: dict[str, Any]):
        with self._condition:
            _keys(event, {"protocol", "run_id", "case_id", "instance", "role", "mode", "pid", "seq", "cmd_id", "event", "data"})
            _require(all(event[k] == v for k, v in (("protocol", PROTOCOL), ("run_id", self.run_id), ("case_id", self.case_id), ("instance", self.instance), ("role", self.role), ("mode", self.mode), ("pid", self.pid))), "event_identity_mismatch")
            _require(_integer(event["pid"], 1) and _integer(event["seq"], 1) and event["seq"] == self._seq + 1 and _integer(event["cmd_id"]), "invalid_event_sequence")
            self._validate_event_data(event)
            self._seq = event["seq"]
            self._events.append(event)
            self._files["events"].write(_encoded(event) + b"\n")
            self._files["events"].flush()
            self._condition.notify_all()

    def send(self, command: str, **fields) -> int:
        with self._write_lock, self._condition:
            _require(self._ready and not self._stopped and self.returncode is None and self._error is None, "process_not_ready")
            extra = {"seed": set(), "run_once": {"barrier"}, "release_barrier": {"barrier", "owner_cmd_id"}, "advance_clock": {"to"}, "inspect": {"label"}, "purge_expired": set(), "stop": set()}
            _require(isinstance(command, str) and command in extra and set(fields) == extra[command] and all(v is not None for v in fields.values()), "invalid_command")
            _require(not any(i not in self._done and c["command"] != "run_once" for i, c in self._commands.items()), "command_in_progress")
            if command != "release_barrier":
                _require(self._run_active is None, "command_in_progress")
            if command == "seed":
                _require(self.mode == "seed" and not self._commands, "invalid_command")
            elif command == "run_once":
                _require(self.mode == "worker" and _enum(fields["barrier"], _BARRIERS), "invalid_command")
            elif command == "release_barrier":
                _require(self.mode == "worker" and self._barrier is not None and _integer(fields["owner_cmd_id"], 1) and fields["owner_cmd_id"] == self._run_active and fields["barrier"] == self._barrier["data"]["barrier"], "invalid_barrier")
            elif command == "advance_clock":
                _require(self.mode in ("worker", "inspect") and self._env["A2C_CLOCK_MODE"] == "logical" and _timestamp(fields["to"]) >= _timestamp(self._current_clock), "invalid_time")
            elif command == "inspect":
                _require(self.mode == "inspect" and isinstance(fields["label"], str) and _NAME.fullmatch(fields["label"]), "invalid_command")
            elif command == "purge_expired":
                _require(self.mode == "worker" and self.case_id.startswith("R2-"), "invalid_command")
            cmd_id = len(self._commands) + 1
            value = {"protocol": PROTOCOL, "run_id": self.run_id, "case_id": self.case_id, "instance": self.instance, "cmd_id": cmd_id, "command": command, **fields}
            data = _encoded(value) + b"\n"
            _require(len(data) <= 4096, "command_too_large")
            self._commands[cmd_id] = value
            if command == "run_once":
                self._run_active = cmd_id
            elif command == "release_barrier":
                self._barrier = None
            try:
                self._proc.stdin.write(data)
                self._proc.stdin.flush()
            except (OSError, ValueError):
                self._fail("command_write_failed")
                raise ProtocolError("command_write_failed") from None
            return cmd_id

    def wait_event(self, event: str, cmd_id: int | None = None, timeout: float = 60) -> dict[str, Any]:
        _require(0 < timeout <= 60, "invalid_wait")
        until = time.monotonic() + timeout
        with self._condition:
            while True:
                if self._error:
                    raise self._error
                for item in self._events:
                    if item["event"] == event and (cmd_id is None or item["cmd_id"] == cmd_id):
                        return item
                # A fast normal child may exit before its pipe reader consumes
                # the final command_done/stopped lines. Drain before rejecting.
                if self.returncode is not None and self._stdout_done:
                    raise ProtocolError("process_exited_before_event")
                remaining = until - time.monotonic()
                if remaining <= 0:
                    raise ProtocolError("event_timeout")
                self._condition.wait(min(0.1, remaining))

    def wait_done(self, cmd_id: int, timeout: float = 60) -> dict[str, Any]:
        return self.wait_event("command_done", cmd_id, timeout)

    def _finish(self, timeout: float = 15):
        until = time.monotonic() + timeout
        try:
            self._proc.wait(timeout=max(0.01, until - time.monotonic()))
            self._converter.wait(timeout=max(0.01, until - time.monotonic()))
        except subprocess.TimeoutExpired:
            raise ProtocolError("process_exit_timeout") from None
        for thread in self._threads:
            thread.join(max(0, until - time.monotonic()))
        _require(not any(t.is_alive() for t in self._threads), "process_reader_timeout")
        if self._error:
            raise self._error
        _require(self._converter.returncode == 0, "test2json_failed")

    def _report(self, result: str):
        if self._closed:
            return
        value = {"protocol": PROTOCOL, "run_id": self.run_id, "case_id": self.case_id, "instance": self.instance,
                 "role": self.role, "mode": self.mode, "pid": self.pid, "source_sha": self.binary.source_sha,
                 "binary_sha256": self.binary.sha256, "returncode": self.returncode, "result": result,
                 "test_run": self._test_run, "test_pass": self._test_pass, "test_bad": self._test_bad,
                 "event_count": len(self._events), "error_code": None if self._error is None else self._error.code}
        if self._owns_artifact_dir:
            hashes = {}
            for key, stream in self._files.items():
                stream.flush()
                with self._paths[key].open("rb") as source:
                    hashes[self._paths[key].name] = hashlib.file_digest(source, "sha256").hexdigest()
            value["artifact_sha256"] = hashes
            with _private_file(self._dir / "process.json") as output:
                output.write(_encoded(value) + b"\n")
        for stream in self._files.values():
            stream.close()
        self._closed = True

    def stop(self, timeout: float = 15) -> dict[str, Any]:
        _require(0 < timeout <= 15, "invalid_wait")
        until = time.monotonic() + timeout
        cmd = self.send("stop")
        event = self.wait_done(cmd, max(0.001, until - time.monotonic()))
        self._finish(max(0.001, until - time.monotonic()))
        _require(self.returncode == 0 and self._stopped and self._test_run and self._test_pass and not self._test_bad and len(self._done) == len(self._commands), "normal_process_not_passed")
        self._report("passed")
        return event

    def _kill(self):
        _require(_POSIX and self._proc is not None and self.returncode is None, "invalid_expected_kill")
        self._proc.kill()  # Popen owns this exact unreaped PID; no caller PID.
        self._finish()
        _require(self.returncode == -9 and self._test_run and not self._test_bad and not self._stopped, "unexpected_kill_exit")
        self.expected_crash_verified = True
        self._report("expected_crash_verified")

    def kill_expected(self, barrier: str, owner_cmd_id: int):
        with self._condition:
            _require(self._barrier is not None and self._run_active == owner_cmd_id and self._barrier["data"]["owner_cmd_id"] == owner_cmd_id and self._barrier["data"]["barrier"] == barrier and barrier != "start", "kill_without_barrier")
            self._verify_event_artifact(self._barrier["data"]["verification_file"], self._barrier["data"]["verification_sha256"])
        self._kill()

    def kill_after_mock_hold(self, mock: MockArk, seq: int, snapshot_relpath: str, sha256: str):
        _require(self._run_active is not None and self._barrier is None and mock.run_id == self.run_id and mock.case_id == self.case_id, "kill_without_mock_barrier")
        status = mock.status()
        _require(any(r["request_seq"] == seq and r["instance"] == self.instance and r["method"] == "DELETE" and r["waiting"] and not r["responded"] for r in status["requests"]), "kill_without_mock_barrier")
        snapshot = self._verify_event_artifact(snapshot_relpath, sha256)
        _require(snapshot.get("mutation_attempt", {}).get("count") == 1 and snapshot.get("inbox", {}).get("count") == 0, "kill_without_registered_snapshot")
        self._kill()

    def abort_owned(self):
        self._aborted = True
        until = time.monotonic() + 5
        for proc in (self._proc, self._converter):
            if proc is not None and proc.poll() is None:
                try:
                    proc.kill()
                except OSError:
                    self._error = ProtocolError("owned_cleanup_failed")
        for proc in (self._proc, self._converter):
            if proc is not None:
                try:
                    proc.wait(timeout=max(0.001, until - time.monotonic()))
                except subprocess.TimeoutExpired:
                    self._error = ProtocolError("owned_cleanup_failed")
        for thread in self._threads:
            thread.join(max(0, until - time.monotonic()))
        if any(t.is_alive() for t in self._threads):
            self._error = ProtocolError("owned_cleanup_failed")
        self._report("aborted")

    def close(self):
        if not self._closed:
            self.abort_owned()
