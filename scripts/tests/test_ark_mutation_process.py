"""Fast local protocol/security tests; these are not the 39 binary scenarios."""
from __future__ import annotations

import copy
import hashlib
import http.client
import importlib.util
import io
import json
import os
from pathlib import Path
import queue
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from unittest import mock

MODULE = Path(__file__).resolve().parents[1] / "lib" / "ark_mutation_process.py"
spec = importlib.util.spec_from_file_location("ark_mutation_process", MODULE)
mp = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = mp
spec.loader.exec_module(mp)

RUN = "0123456789abcdef"
CASE = "N3-cancel-Ca"
NOW = "2026-10-26T12:34:56.123Z"
TOKEN = "synthetic-provider-" + "p" * 32
CONTROL = "synthetic-control-" + "c" * 32


def private_json(path, value):
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    data = mp._encoded(value)
    with mp._private_file(path) as out:
        out.write(data)
    return hashlib.sha256(data).hexdigest()


class MockHTTPTests(unittest.TestCase):
    def setUp(self):
        self.mock = mp.MockArk(RUN, CASE, TOKEN, CONTROL).start()
        self.port = int(self.mock.origin.rsplit(":", 1)[1])

    def tearDown(self):
        try:
            self.mock.close()
        except mp.ProtocolError:
            if not getattr(self, "expect_mock_failure", False):
                raise

    def business(self, method, instance="sender-p", suffix=None, body=None, headers=None):
        connection = http.client.HTTPConnection("127.0.0.1", self.port, timeout=3)
        path = "/api/v3/contents/generations/tasks"
        if method != "POST":
            path += "/" + (suffix or self.mock.upstream_id)
        values = {"Authorization": "Bearer " + TOKEN, "X-A2C-Run": RUN, "X-A2C-Case": CASE, "X-A2C-Instance": instance}
        values.update(headers or {})
        data = b'{"model":"synthetic","content":[{"type":"text","text":"never log body"}]}' if method == "POST" and body is None else body
        try:
            connection.request(method, path, body=data, headers=values)
            response = connection.getresponse()
            return response.status, response.getheader("Content-Type"), response.read(), response.getheader("X-Request-Id")
        finally:
            connection.close()

    def raw_control(self, body, auth=CONTROL):
        conn = http.client.HTTPConnection("127.0.0.1", self.port, timeout=3)
        try:
            conn.request("POST", "/__a2c/control", body, {"Authorization": "Bearer " + auth, "Content-Type": "application/json"})
            response = conn.getresponse()
            return response.status, response.read()
        finally:
            conn.close()

    def test_case_isolation_fixed_responses_and_counts_never_reset(self):
        self.mock.set(["queued", "succeeded", "cancelled"], "ack204")
        status, content, body, req = self.business("POST", "seed-p")
        self.assertEqual((status, content, req), (200, "application/json", "a2c-request-1"))
        self.assertEqual(json.loads(body), {"id": self.mock.upstream_id})
        results = [json.loads(self.business("GET")[2]) for _ in range(4)]
        self.assertEqual([r["status"] for r in results], ["queued", "succeeded", "cancelled", "cancelled"])
        self.assertEqual(results[1]["usage"], {"completion_tokens": 100, "total_tokens": 100})
        self.assertNotIn("usage", results[2])
        self.assertEqual(self.business("DELETE")[:3], (204, None, b""))
        self.mock.set(["queued"], "not_found404")
        self.assertEqual(self.business("DELETE")[0], 404)
        self.mock.set(["queued"], "upstream502")
        self.assertEqual(self.business("DELETE")[0], 502)
        state = self.mock.status()
        self.assertEqual((state["post"], state["get"], state["delete"], state["seq"]), (1, 4, 3, 8))
        self.assertEqual(state["by_instance"]["sender-p"], {"post": 0, "get": 4, "delete": 3})
        serialized = json.dumps(state)
        for secret in (TOKEN, CONTROL, "never log body", "/api/v3/"):
            self.assertNotIn(secret, serialized)

    def test_eof_is_actual_socket_close_after_count(self):
        self.mock.set(["queued"], "eof")
        with self.assertRaises(http.client.RemoteDisconnected):
            self.business("DELETE")
        state = self.mock.status()
        self.assertEqual(state["delete"], 1)
        self.assertEqual(state["requests"][0]["outcome"], "eof")
        self.assertFalse(state["requests"][0]["responded"])

    def test_hold_precise_release_and_double_release_rejected(self):
        self.mock.set(["queued"], "hold")
        results = queue.Queue()
        thread = threading.Thread(target=lambda: results.put(self.business("DELETE")))
        thread.start()
        waiting = self.mock.wait_delete("sender-p", 3)
        self.assertTrue(waiting["waiting"])
        self.assertEqual(self.mock.status()["pending_deletes"], [1])
        self.mock.release_delete(1, "not_found404")
        thread.join(3)
        self.assertFalse(thread.is_alive())
        self.assertEqual(results.get_nowait()[0], 404)
        with self.assertRaises(mp.ProtocolError):
            self.mock.release_delete(1, "ack204")
        self.assertEqual(self.mock.status()["delete"], 1)

    def test_controls_are_strict_private_and_never_business_counts(self):
        values = [b'{"command":"set","query_sequence":["queued"],"delete_response":"ack204","extra":1}',
                  b'{"command":"set","command":"set","query_sequence":["queued"],"delete_response":"ack204"}',
                  b'{"command":"set","query_sequence":["queued"],"delete_response":[]}',
                  b'{"command":"set","query_sequence":null,"delete_response":"ack204"}',
                  b'{"command":"release_delete","request_seq":true,"response":"ack204"}', b'[]']
        for body in values:
            self.assertEqual(self.raw_control(body)[0], 409)
        self.assertEqual(self.raw_control(b'{}', TOKEN)[0], 409)
        self.assertEqual(self.mock.status()["seq"], 0)

    def test_wrong_case_target_auth_or_json_fails_closed(self):
        self.expect_mock_failure = True
        self.assertEqual(self.business("DELETE", headers={"X-A2C-Case": "N3-delete-Ca"})[0], 409)
        with self.assertRaises(mp.ProtocolError):
            self.mock.status()

    def test_request_cap_is_case_failure(self):
        for _ in range(100):
            self.business("GET")
        self.expect_mock_failure = True
        self.assertEqual(self.business("GET")[0], 409)
        with self.assertRaises(mp.ProtocolError):
            self.mock.status()

    def test_pending_drop_requires_real_verified_dead_process(self):
        with self.assertRaises(mp.ProtocolError):
            self.mock.drop_waiting(1, type("Fake", (), {"returncode": None, "expected_crash_verified": False})())


class BytePipe:
    def __init__(self):
        self.items = queue.Queue()
        self.closed = False

    def put(self, value):
        self.items.put(value)

    def readline(self, limit):
        return self.items.get(timeout=5)

    def close(self):
        if not self.closed:
            self.closed = True
            self.put(b"")


class FakeInput:
    def __init__(self, write, close=None):
        self._write, self._close = write, close

    def write(self, value):
        self._write(value)
        return len(value)

    def flush(self):
        pass

    def close(self):
        if self._close:
            self._close()
            self._close = None


class FakeProcess:
    def __init__(self, pid):
        self.pid, self.returncode = pid, None
        self.stdout, self.stderr = BytePipe(), BytePipe()
        self.stdin = None
        self.done = threading.Event()

    def poll(self):
        return self.returncode

    def exit(self, code):
        self.returncode = code
        self.stdout.close()
        self.stderr.close()
        self.done.set()

    def wait(self, timeout):
        if not self.done.wait(timeout):
            raise subprocess.TimeoutExpired("synthetic", timeout)
        return self.returncode

    def kill(self):
        self.exit(-9)


class ProcessTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name).resolve()
        self.root.chmod(0o700)
        self.binary = self.root / "synthetic-test-binary"
        self.binary.write_bytes(b"not-a-real-Go-binary: unit-control-test-only")
        self.go = self.root / "synthetic-go-tool"
        self.go.write_bytes(b"not-executed")
        self.spec = mp.BinarySpec(self.binary, mp.P_SOURCE, hashlib.sha256(self.binary.read_bytes()).hexdigest())
        upstream, database = mp._identity(RUN, CASE)
        self.env = {"A2C_PROTOCOL": mp.PROTOCOL, "A2C_RUN_ID": RUN, "A2C_CASE_ID": CASE, "A2C_INSTANCE": "sender-p", "A2C_MODE": "worker", "A2C_BUILD_ROLE": "Pon", "A2C_SOURCE_SHA": mp.P_SOURCE, "A2C_CASE_DIR": str(self.root), "A2C_CLOCK_MODE": "logical", "A2C_INITIAL_TIME": NOW, "A2C_MOCK_ORIGIN": "http://127.0.0.1:12345", "A2C_UPSTREAM_ID": upstream, "A2C_PROVIDER_TOKEN": TOKEN,
                    "LAIAI_DSN": "root:synthetic123@tcp(127.0.0.1:12346)/" + database + "?parseTime=true&loc=UTC&timeout=5s", "LAIAI_AUTH_SECRET": "auth-" + "a"*32, "LAIAI_DATA_KEYS": "1:"+"d"*64, "LAIAI_DATA_KEY_ACTIVE": "1", "LAIAI_VIDEO_HMAC_SECRET": "hmac-"+"h"*32, "LAIAI_VIDEO_RESPONSE_INBOX": "false", "LAIAI_VIDEO_MUTATION_RESPONSE_INBOX": "true", "LAIAI_DB_MAX_OPEN_CONNS": "8", "LAIAI_DB_MAX_IDLE_CONNS": "4"}
        self.controller = None

    def tearDown(self):
        if self.controller:
            self.controller.close()
        self.temp.cleanup()

    def controller_for(self, env=None, binary=None):
        self.controller = mp.ProcessController(binary or self.spec, self.go, self.root, env or self.env)
        return self.controller

    def wire(self, event, data, cmd=0, seq=None):
        return {"protocol": mp.PROTOCOL, "run_id": RUN, "case_id": CASE, "instance": "sender-p", "role": "Pon", "mode": "worker", "pid": 12345, "seq": seq or self.controller._seq+1, "cmd_id": cmd, "event": event, "data": data}

    def ready(self):
        return self.wire("ready", {"source_sha": mp.P_SOURCE, "clock_mode": "logical", "clock": NOW, "db_time": NOW, "create_inbox": False, "mutation_field_present": True, "mutation_inbox": True, "org_id": "1", "task_id": "vt_synthetic", "operation_id": "op_synthetic"})

    def manual_ready(self):
        c = self.controller_for()
        c._proc = FakeProcess(12345)
        c._proc.stdin = FakeInput(lambda value: None)
        c._files["events"] = io.BytesIO()
        c._accept_event(self.ready())
        return c

    def test_env_allowlist_pins_and_no_parent_secret_inheritance(self):
        with mock.patch.dict(os.environ, {"HTTPS_PROXY": "never-inherit", "LAIAI_OTHER": "never-inherit", "ARK_TEST_MYSQL_DSN": "never-inherit", "GOFLAGS": "-modfile=bad"}):
            child = mp._child_environment(self.env, self.root, self.spec)
        self.assertNotIn("HTTPS_PROXY", child)
        self.assertNotIn("GOFLAGS", child)
        self.assertNotIn("ARK_TEST_MYSQL_DSN", child)
        self.assertEqual(child["TZ"], "UTC")
        for key, value in [("A2C_UNKNOWN", "x"), ("LAIAI_EXTRA", "x"), ("A2C_SOURCE_SHA", mp.CA_SOURCE), ("A2C_MOCK_ORIGIN", "http://example.com:12345"), ("LAIAI_VIDEO_RESPONSE_INBOX", "true"), ("LAIAI_DSN", self.env["LAIAI_DSN"].replace("127.0.0.1", "154.17.8.122")), ("LAIAI_DSN", self.env["LAIAI_DSN"]+"&parseTime=true")]:
            changed = {**self.env, key: value}
            with self.assertRaises(mp.ProtocolError):
                mp._child_environment(changed, self.root, self.spec)
        for role in ("Ca", "Poff", "Pon", "Pboth"):
            env = {**self.env, "A2C_BUILD_ROLE": role, "A2C_SOURCE_SHA": mp.CA_SOURCE if role == "Ca" else mp.P_SOURCE, "LAIAI_VIDEO_RESPONSE_INBOX": "true" if role == "Pboth" else "false"}
            if role == "Ca":
                env.pop("LAIAI_VIDEO_MUTATION_RESPONSE_INBOX")
            else:
                env["LAIAI_VIDEO_MUTATION_RESPONSE_INBOX"] = "false" if role == "Poff" else "true"
            binary = mp.BinarySpec(self.binary, env["A2C_SOURCE_SHA"], self.spec.sha256)
            mp._child_environment(env, self.root, binary)

    def test_json_artifact_hash_path_and_symlink_failures(self):
        for value in (b'{"a":1,"a":2}', b'\xef\xbb\xbf{}', b'[]', b'{"a":NaN}'):
            with self.assertRaises(mp.ProtocolError):
                mp._json(value)
        data = {"protocol": mp.PROTOCOL, "run_id": RUN, "case_id": CASE}
        digest = private_json(self.root / "snapshots" / "ok.json", data)
        self.assertEqual(mp.read_verified_artifact(self.root, "snapshots/ok.json", digest), data)
        for name in ("../outside", "/absolute", "snapshots//ok.json", "snapshots/../ok.json", "C:/absolute", "snapshots\\ok.json"):
            with self.assertRaises(mp.ProtocolError):
                mp.read_verified_artifact(self.root, name, digest)
        with self.assertRaises(mp.ProtocolError):
            mp.read_verified_artifact(self.root, "snapshots/ok.json", "0"*64)
        if os.name != "nt":
            (self.root / "link").symlink_to(self.root / "snapshots", target_is_directory=True)
            with self.assertRaises(mp.ProtocolError):
                mp.read_verified_artifact(self.root, "link/ok.json", digest)
            (self.root / "snapshots" / "ok.json").chmod(0o644)
            with self.assertRaises(mp.ProtocolError):
                mp.read_verified_artifact(self.root, "snapshots/ok.json", digest)

    def test_binary_hash_must_match_before_spawn(self):
        self.binary.write_bytes(b"changed")
        with mock.patch.object(mp.subprocess, "Popen") as popen, self.assertRaises(mp.ProtocolError):
            self.controller_for()
        popen.assert_not_called()

    def test_strict_event_identity_sequence_fields_and_command_links(self):
        c = self.manual_ready()
        command = c.send("run_once", barrier="none")
        valid = self.wire("run_started", {"barrier": "none", "clock": NOW}, command)
        for field, value in (("pid", 777), ("seq", 44), ("role", "Ca"), ("case_id", "N3-delete-Ca"), ("extra", "no"), ("cmd_id", 99)):
            event = copy.deepcopy(valid)
            event[field] = value
            with self.assertRaises(mp.ProtocolError):
                c._accept_event(event)
        c._accept_event(valid)
        with self.assertRaises(mp.ProtocolError):
            c._accept_event(self.wire("run_started", {"barrier": "none", "clock": NOW}, command))
        with self.assertRaises(mp.ProtocolError):
            c.send("run_once", barrier="none")
        c._accept_event(self.wire("command_done", {"command": "run_once", "clock": NOW, "db_time": NOW, "process_once_returned": True}, command))
        self.assertIsNone(c._run_active)
        with self.assertRaises(mp.ProtocolError):
            c.send("advance_clock", to="2026-10-25T12:34:56.123Z")

    def test_barrier_requires_real_hashed_artifact_and_release_is_precise(self):
        c = self.manual_ready()
        command = c.send("run_once", barrier="registered_before_http")
        c._accept_event(self.wire("run_started", {"barrier": "registered_before_http", "clock": NOW}, command))
        fields = {"barrier": "registered_before_http", "owner_cmd_id": command, "verification_file": "snapshots/proof.json", "verification_sha256": "0"*64, "clock": NOW, "db_time": NOW}
        with self.assertRaises(mp.ProtocolError):
            c._accept_event(self.wire("barrier_reached", fields, command))
        fields["verification_sha256"] = private_json(self.root / "snapshots/proof.json", {"protocol": mp.PROTOCOL, "run_id": RUN, "case_id": CASE})
        c._accept_event(self.wire("barrier_reached", fields, command))
        with self.assertRaises(mp.ProtocolError):
            c.send("release_barrier", barrier="registered_before_http", owner_cmd_id=command+1)
        released = c.send("release_barrier", barrier="registered_before_http", owner_cmd_id=command)
        self.assertIsNone(c._barrier)
        with self.assertRaises(mp.ProtocolError):
            c.send("release_barrier", barrier="registered_before_http", owner_cmd_id=command)
        c._accept_event(self.wire("command_done", {"command": "release_barrier"}, released))

    def test_stop_event_follows_done_and_counts_self(self):
        c = self.manual_ready()
        command = c.send("stop")
        with self.assertRaises(mp.ProtocolError):
            c._accept_event(self.wire("stopped", {"completed_commands": command}, command))
        c._accept_event(self.wire("command_done", {"command": "stop"}, command))
        c._accept_event(self.wire("stopped", {"completed_commands": command}, command))
        self.assertTrue(c._stopped)

    def fake_spawn(self, pass_kind="pass"):
        calls = []
        converter, child = FakeProcess(777), FakeProcess(12345)
        def converted(value):
            text = value.decode()
            if text.startswith("=== RUN"):
                converter.stdout.put(mp._encoded({"Action": "run", "Test": mp.TEST_NAME})+b"\n")
            elif text.startswith("--- PASS"):
                converter.stdout.put(mp._encoded({"Action": pass_kind, "Test": mp.TEST_NAME})+b"\n")
        converter.stdin = FakeInput(converted, lambda: converter.exit(0))
        def input_command(value):
            cmd = json.loads(value)
            if cmd["command"] == "stop":
                done = self.wire("command_done", {"command": "stop"}, cmd["cmd_id"], 2)
                stopped = self.wire("stopped", {"completed_commands": cmd["cmd_id"]}, cmd["cmd_id"], 3)
                child.stdout.put(b"A2C_EVENT "+mp._encoded(done)+b"\n")
                child.stdout.put(b"A2C_EVENT "+mp._encoded(stopped)+b"\n")
                child.stdout.put(b"--- PASS: "+mp.TEST_NAME.encode()+b" (0.00s)\nPASS\n")
                child.exit(0)
        child.stdin = FakeInput(input_command)
        def popen(args, **kwargs):
            calls.append((args, kwargs))
            if len(calls) == 1:
                return converter
            child.stdout.put(b"=== RUN   "+mp.TEST_NAME.encode()+b"\n")
            child.stdout.put(b"A2C_EVENT "+mp._encoded(self.ready())+b"\n")
            return child
        return popen, calls, child, converter

    def test_direct_child_converter_and_normal_exit_evidence(self):
        c = self.controller_for()
        popen, calls, child, converter = self.fake_spawn()
        with mock.patch.object(mp.subprocess, "Popen", side_effect=popen):
            c.start()
            c.stop()
        self.assertEqual(c.pid, child.pid)
        self.assertEqual(calls[1][0][0], str(self.binary))
        self.assertNotIn("test2json", calls[1][0])
        self.assertEqual(calls[0][0][1:3], ["tool", "test2json"])
        self.assertFalse(calls[1][1]["shell"])
        report = json.loads((self.root / "processes/sender-p/process.json").read_text())
        self.assertEqual(report["result"], "passed")
        self.assertTrue(report["test_run"] and report["test_pass"])
        self.assertNotIn(TOKEN, json.dumps(report))

    def test_skip_or_missing_test_pass_never_normal_success(self):
        c = self.controller_for()
        popen, _, _, _ = self.fake_spawn("skip")
        with mock.patch.object(mp.subprocess, "Popen", side_effect=popen):
            c.start()
            with self.assertRaises(mp.ProtocolError):
                c.stop()
        c.close()
        report = json.loads((self.root / "processes/sender-p/process.json").read_text())
        self.assertEqual(report["result"], "aborted")

    def test_timeout_and_unverified_kill_do_not_signal_unowned_pid(self):
        c = self.manual_ready()
        with self.assertRaises(mp.ProtocolError):
            c.wait_event("barrier_reached", timeout=0.02)
        with self.assertRaises(mp.ProtocolError):
            c.kill_expected("registered_before_http", 1)
        self.assertIsNone(c.returncode)

    def test_expected_crash_rejects_wrong_signal_even_at_verified_barrier(self):
        c = self.manual_ready()
        command = c.send("run_once", barrier="registered_before_http")
        c._accept_event(self.wire("run_started", {"barrier": "registered_before_http", "clock": NOW}, command))
        digest = private_json(self.root / "barriers/proof.json", {"protocol": mp.PROTOCOL, "run_id": RUN, "case_id": CASE})
        c._accept_event(self.wire("barrier_reached", {"barrier": "registered_before_http", "owner_cmd_id": command, "verification_file": "barriers/proof.json", "verification_sha256": digest, "clock": NOW, "db_time": NOW}, command))
        c._test_run = True
        with mock.patch.object(mp, "_POSIX", True), mock.patch.object(c, "_finish"), mock.patch.object(c._proc, "kill", side_effect=lambda: c._proc.exit(-15)):
            with self.assertRaises(mp.ProtocolError):
                c.kill_expected("registered_before_http", command)
        self.assertFalse(c.expected_crash_verified)

    def test_existing_process_artifact_directory_never_reused_or_modified(self):
        c = self.controller_for()
        directory = self.root / "processes" / "sender-p"
        directory.mkdir(mode=0o700, parents=True)
        private_json(directory / "process.json", {"original": True})
        before = (directory / "process.json").read_bytes()
        with mock.patch.object(mp.subprocess, "Popen") as popen, self.assertRaises(mp.ProtocolError):
            c.start()
        popen.assert_not_called()
        self.assertEqual((directory / "process.json").read_bytes(), before)


if __name__ == "__main__":
    unittest.main(verbosity=2)
