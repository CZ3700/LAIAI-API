"""B1b four-family controller. PREPARATION ONLY until final N is committed.

The outer, separately audited controller must create/migrate each private schema,
provide synthetic keys in LAIAI_* and B1B_DSN_TEMPLATE, hold ci.lock, and verify
the two binaries against real git-archive/build manifests. This file never drops
databases, changes leases/expiry, starts production, or contacts provider hosts.
"""
from __future__ import annotations
import argparse, datetime as dt, hashlib, http.server, json, os, pathlib
import queue, re, secrets, subprocess, threading, time

PROTOCOL = "execution-clock-handoff-v1"
OLD = "9a6fd9426e33b2e1c269b6cb77a170e8890073f2"
TEST = "TestMySQLExecutionInputBinaryHandoffProcess"
CASES = ("N01", "N02", "N03-create", "N03-action", "N04-schema-new", "N04-schema-old", "N04-runtime-new", "N04-runtime-old")
EVENTS = {"ready","seeded","clock_set","action_pending","inspected","cycle_completed","receipt_committed","cancel_requested","action_read","purged","unknown_injected","stop_ack","drained","failed"}

def require(condition, code):
    if not condition: raise RuntimeError(code)

def strict_json(raw):
    def pairs(items):
        result={}
        for key,value in items:
            require(key not in result,"duplicate_json_key")
            result[key]=value
        return result
    return json.loads(raw,object_pairs_hook=pairs,parse_constant=lambda _: (_ for _ in ()).throw(RuntimeError("invalid_json_constant")))

def utc(value):
    return dt.datetime.fromisoformat(value.replace("Z", "+00:00"))

def immutable(before, after, input_identity=True):
    for key in ("billing_snapshot_sha256", "binding_sha256", "attempt_sha256", "receipt_sha256"):
        require(before[key] == after[key], "immutable_" + key)
    if input_identity:
        require(before["input"]["identity_sha256"] == after["input"]["identity_sha256"], "input_identity_changed")

def no_financial_change(before, after):
    require(before["financial_sha256"] == after["financial_sha256"], "financial_fingerprint_changed")
    require(before["actual_amounts"] == after["actual_amounts"], "financial_amount_changed")

def settled(proof):
    require(proof["task_status"] == "succeeded" and proof["billing_state"] == "settled", "not_settled")
    finance = proof["finance"]
    for key, value in {"captured_usd":"0.00080000", "released_usd":"0.86320000", "frozen_usd":"0.00000000", "capture_journal_count":1, "billing_entry_count":1, "ledger_drift_count":0}.items():
        require(finance[key] == value, "settlement_"+key)
    require(proof["actual_amounts"] == {"supplier_cny":"0.00460000", "key_used_usd":"0.00080000", "provider_reserved_cny":"0.00000000", "provider_consumed_cny":"0.00460000"}, "supplier_key_quota_amounts")
    require(proof["price"] == {"retail_usd_per_1m":"8", "supplier_cny_per_1m":"46", "fx_usd_per_cny":"0.14", "hold_usd":"0.86400000", "supplier_reserved_cny":"4.96800000", "effective_ratio":"1", "rule_version":"handoff-v1", "fx_version":"handoff-v1"}, "original_price_fx")

class Mock:
    def __init__(self, run, evidence):
        self.run, self.evidence = run, evidence
        self.token = "synthetic-"+secrets.token_hex(24)
        self.lock = threading.Lock()
        self.mode = {case:"queued" for case in CASES}
        self.requests = []
        owner = self
        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_): pass
            def handle_method(self):
                case = self.headers.get("X-B1B-Case", "")
                instance = self.headers.get("X-B1B-Instance", "")
                target = "b1b-"+case.lower()
                base = "/api/v3/contents/generations/tasks"
                valid = case in CASES and self.headers.get("X-B1B-Run")==run and re.fullmatch(r"[a-z][a-z0-9_-]{0,47}",instance) and self.headers.get("Authorization")=="Bearer "+owner.token
                valid = valid and ((self.command=="POST" and self.path==base) or (self.command in ("GET","DELETE") and self.path==base+"/"+target))
                length = int(self.headers.get("Content-Length","0"))
                if not valid or length<0 or length>1024*1024:
                    self.send_error(400); return
                body=self.rfile.read(length)
                with owner.lock:
                    mode=owner.mode[case]
                    record={"run_id":run,"case_id":case,"instance":instance,"method":self.command,"path":self.path,"body_sha256":hashlib.sha256(body).hexdigest(),"body_bytes":len(body),"sequence":len(owner.requests)+1}
                    owner.requests.append(record)
                    with (evidence/"mock-requests.jsonl").open("a",encoding="utf-8") as stream: stream.write(json.dumps(record,separators=(",",":"))+"\n")
                if self.command=="DELETE":
                    self.send_response(204); self.send_header("Content-Length","0"); self.end_headers(); return
                data={"id":target,"status":"queued" if self.command=="POST" else mode}
                if self.command=="GET" and mode=="succeeded": data["usage"]={"completion_tokens":100,"total_tokens":999}
                raw=json.dumps(data,separators=(",",":")).encode()
                self.send_response(200);self.send_header("Content-Type","application/json");self.send_header("Content-Length",str(len(raw)));self.end_headers();self.wfile.write(raw)
            do_POST=handle_method;do_GET=handle_method;do_DELETE=handle_method
        self.server=http.server.ThreadingHTTPServer(("127.0.0.1",0),Handler)
        self.server.daemon_threads=True
        self.thread=threading.Thread(target=self.server.serve_forever,daemon=True);self.thread.start()
        self.origin="http://127.0.0.1:"+str(self.server.server_port)
    def snapshot(self,case):
        with self.lock:return [r.copy() for r in self.requests if r["case_id"]==case]
    def close(self):self.server.shutdown();self.server.server_close();self.thread.join(3)

class Child:
    def __init__(self,matrix,case,role,instance,producers):
        self.matrix,self.case,self.role,self.instance=matrix,case,role,instance
        self.seq,self.command_id=0,0
        self.events=queue.Queue();self.pending=[];self.reader_error=None;self.lines=[]
        self.clean_exit=False;self.schema_rejections=0
        case_dir=matrix.root/case;case_dir.mkdir(exist_ok=True)
        schema="ark_test_exec_"+matrix.run+"_"+hashlib.sha256(case.encode()).hexdigest()[:12]
        env=os.environ.copy()
        for key in list(env):
            if key.startswith("B1B_"):del env[key]
        env.update({"B1B_PROTOCOL":PROTOCOL,"B1B_RUN_ID":matrix.run,"B1B_CASE_ID":case,"B1B_INSTANCE":instance,"B1B_ROLE":role,"B1B_SOURCE_SHA":matrix.builds[role]["source"],"B1B_CASE_DIR":str(case_dir.resolve()),"B1B_MOCK_ORIGIN":matrix.mock.origin,"B1B_UPSTREAM_ID":"b1b-"+case.lower(),"B1B_PROVIDER_TOKEN":matrix.mock.token,"LAIAI_DSN":matrix.dsn_template.replace("{schema}",schema),"LAIAI_DSN_REPLICAS":"","LAIAI_VIDEO_RESPONSE_INBOX":str(producers).lower(),"LAIAI_VIDEO_MUTATION_RESPONSE_INBOX":str(producers).lower()})
        self.log=matrix.root/(case+"-"+instance+".log")
        self.process=subprocess.Popen([matrix.builds[role]["path"],"-test.run=^"+TEST+"$","-test.count=1","-test.v","-test.timeout=10m"],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True,encoding="utf-8",env=env,bufsize=1)
        matrix.children.append(self)
        self.thread=threading.Thread(target=self.read,daemon=True);self.thread.start()
        ready=self.wait(0,"ready",30)
        require(ready["compiled_source"]==matrix.builds[role]["source"] and ready["old_source"]==OLD,"ready_source")
        require(ready["create_producer"]==producers and ready["mutation_producer"]==producers,"producer_configuration")
    def read(self):
        try:
            with self.log.open("x",encoding="utf-8") as out:
                for line in self.process.stdout:
                    out.write(line);out.flush();self.lines.append(line)
                    require(len(line)<262144,"unbounded_process_line")
                    require("DATA RACE" not in line and "--- SKIP:" not in line and "--- FAIL:" not in line and not line.startswith("panic:"),"process_test_failure")
                    if not line.startswith("B1B_EVENT "):continue
                    event=strict_json(line[len("B1B_EVENT "):])
                    require(set(event)=={"protocol","run_id","case_id","instance","role","source_sha","pid","seq","cmd_id","event","data"} and event["event"] in EVENTS,"event_shape")
                    require(event["protocol"]==PROTOCOL and event["run_id"]==self.matrix.run and event["case_id"]==self.case and event["instance"]==self.instance and event["role"]==self.role and event["source_sha"]==self.matrix.builds[self.role]["source"] and event["pid"]==self.process.pid,"event_identity")
                    require(event["seq"]==self.seq+1,"event_sequence");self.seq+=1
                    require(event["event"]!="failed","child_failed")
                    self.events.put(event)
        except Exception as exc:self.reader_error=str(exc);self.events.put({"reader_failed":True})
    def wait(self,cmd,event,seconds=30):
        end=time.monotonic()+seconds
        while time.monotonic()<end:
            require(self.reader_error is None,"reader_failure")
            for i,item in enumerate(self.pending):
                if item["cmd_id"]==cmd and item["event"]==event:return self.pending.pop(i)["data"]
            try:item=self.events.get(timeout=min(0.2,max(.001,end-time.monotonic())))
            except queue.Empty:
                require(self.process.poll() is None,"process_exited_before_event");continue
            require("reader_failed" not in item,"reader_failure")
            self.pending.append(item)
        raise RuntimeError("event_timeout_"+event)
    def send(self,command,**fields):
        self.command_id+=1
        payload={"protocol":PROTOCOL,"run_id":self.matrix.run,"case_id":self.case,"instance":self.instance,"cmd_id":self.command_id,"command":command,**fields}
        with (self.matrix.root/(self.case+"-"+self.instance+"-commands.jsonl")).open("a",encoding="utf-8") as out:out.write(json.dumps(payload,separators=(",",":"))+"\n")
        self.process.stdin.write(json.dumps(payload,separators=(",",":"))+"\n");self.process.stdin.flush();return self.command_id
    def simple(self,command,event,**fields):return self.wait(self.send(command,**fields),event)
    def inspect(self,seconds=30):return self.wait(self.send("inspect"),"inspected",seconds)
    def cycle(self):
        before_http=self.matrix.mock.snapshot(self.case)
        out=self.wait(self.send("run_once"),"cycle_completed",150)
        schema_consumer=self.case in ("N04-schema-new","N04-schema-old") and self.role=="new" and self.instance in ("first-new","second-new")
        if schema_consumer:
            allowed=("schema_rejected",) if self.schema_rejections==0 else ("none","schema_rejected")
            require(out["error_class"] in allowed,"schema_recovery_error")
            proof=out["proof"]
            require(out["barrier_reached"] is False and proof["input"]["schema"]==99 and proof["application_state"]=="blocked" and proof["application_error"]=="execution_identity_unavailable" and proof["applied_at"] is None,"schema_rejection_state")
            if out["error_class"]=="schema_rejected":
                require(type(out["http_attempts"]) is int and out["http_attempts"]==0 and self.matrix.mock.snapshot(self.case)==before_http,"schema_rejection_http")
            self.schema_rejections+=int(out["error_class"]=="schema_rejected")
        else:require(out["error_class"]=="none","cycle_error")
        return out["proof"]
    def receipt(self):
        cmd=self.send("run_once",barrier="receipt_committed")
        proof=self.wait(cmd,"receipt_committed",60)
        require(utc(proof["database_time"])<utc(proof["input"]["expires_at"]),"late_receipt_precondition")
        self.wait(self.send("cancel_active",owner_cmd_id=cmd),"cancel_requested")
        done=self.wait(cmd,"cycle_completed",25)
        require(done["barrier_reached"] and done["error_class"]=="cancelled","caller_cancel_boundary")
        require(done["proof"]["application_state"]=="pending" and done["proof"]["applied_at"] is None,"producer_applied_after_cancel")
        immutable(proof,done["proof"]);no_financial_change(proof,done["proof"])
        return done["proof"]
    def stop(self):
        self.simple("stop","stop_ack");self.process.stdin.close()
        drained=self.wait(0,"drained",20)
        require(drained=={"active_work":False,"pools_closed":True},"drain_proof")
        require(self.process.wait(timeout=25)==0,"drain_exit")
        self.thread.join(3);require(not self.thread.is_alive() and self.reader_error is None,"drain_reader")
        require(not self.pending and self.events.empty(),"unconsumed_process_events")
        text="".join(self.lines)
        require(text.count("--- PASS: "+TEST)==1 and "\nPASS\n" in text,"missing_go_pass")
        self.clean_exit=True

class Matrix:
    def __init__(self,args):
        self.run=args.run_id;require(re.fullmatch(r"[0-9a-f]{16}",self.run),"run_id")
        self.root=pathlib.Path(args.evidence).resolve();self.root.mkdir(parents=True,exist_ok=False)
        self.builds=strict_json(pathlib.Path(args.build_manifest).read_text(encoding="utf-8"))
        require(set(self.builds)=={"old","new"},"build_roles")
        require(self.builds["old"]["source"]==OLD,"old_source")
        require(re.fullmatch(r"[0-9a-f]{40}",self.builds["new"]["source"]) and self.builds["new"]["source"]!=OLD,"new_source")
        for role,build in self.builds.items():
            path=pathlib.Path(build["path"]).resolve();require(path.is_file() and not path.is_symlink(),"binary_file")
            require(hashlib.sha256(path.read_bytes()).hexdigest()==build["sha256"],"binary_sha")
            build["path"]=str(path)
        self.dsn_template=os.environ["B1B_DSN_TEMPLATE"]
        require(self.dsn_template.count("{schema}")==1 and "loc=UTC" in self.dsn_template and "parseTime=true" in self.dsn_template,"dsn_template")
        self.children=[];self.mock=Mock(self.run,self.root);self.results=[]
    def child(self,case,role,label,producers=False):return Child(self,case,role,label,producers)
    def queued(self,child):
        proof=child.cycle();require(proof["task_status"]=="queued" and proof["upstream_id"]=="b1b-"+child.case.lower(),"real_create_binding");return proof
    def wait_due(self,child,proof,purge=False):
        end=time.monotonic()+260
        while True:
            proof=child.inspect();times=[utc(proof["database_time"])]
            if proof["lease_until"]:times.append(utc(proof["lease_until"]))
            if purge:times.append(utc(proof["input"]["expires_at"]))
            if times[0]>=max(times):return proof
            require(time.monotonic()<end,"real_database_lease_timeout");time.sleep(.25)
    def settle(self,child):
        self.mock.mode[child.case]="succeeded";end=time.monotonic()+45
        while True:
            proof=child.cycle()
            if proof["billing_state"]=="settled":settled(proof);return proof
            require(time.monotonic()<end and proof["task_status"] not in ("failed","cancelled"),"real_query_settlement_timeout");time.sleep(.2)
    def held_unchanged_replay(self,child,before):
        if child.case in ("N04-schema-new","N04-schema-old") and child.role=="new":
            # The old first consumer may have committed a real 30s backoff.
            # Wait by read-only DB observations; never rewrite its due time.
            end=time.monotonic()+40
            while True:
                remaining=end-time.monotonic()
                require(remaining>0,"schema_recovery_due_timeout")
                observed=child.inspect(seconds=min(10,remaining));due=observed["application_next_attempt_at"]
                require(due is not None,"schema_recovery_due_missing")
                require(time.monotonic()<end,"schema_recovery_due_timeout")
                if utc(observed["database_time"])>=utc(due):break
                time.sleep(min(.25,max(0,end-time.monotonic())))
        after=child.cycle();immutable(before,after);no_financial_change(before,after)
        require(after["input_successful_reads"]==before["input_successful_reads"],"recovery_decrypted_input")
        replay=child.cycle();immutable(after,replay);no_financial_change(after,replay)
        require(replay["input_successful_reads"]==after["input_successful_reads"],"replay_decrypted_input")
        return replay
    def run_case(self,case):
        print("B1B_STAGE "+case+" start",flush=True)
        near=case in ("N01","N04-runtime-new","N04-runtime-old")
        writer_role="new" if case.startswith("N03") else "old"
        writer=self.child(case,writer_role,"writer-"+writer_role,True)
        if near:writer.simple("clock","clock_set",clock_offset_ms=-86370000)
        seed=writer.simple("seed","seeded")
        if case in ("N01","N03-create"):
            writer.stop()
            consumer_role="new" if case=="N01" else "old"
            consumer=self.child(case,consumer_role,"sender-"+consumer_role,True)
            if case=="N01":
                persisted=consumer.receipt();consumer.stop()
                consumer=self.child(case,"new","recovery-new",False)
                self.wait_due(consumer,persisted,True)
                purged=consumer.simple("purge","purged")["after"]
                final=self.settle(consumer);immutable(persisted,final)
                require(final["input_successful_reads"]==purged["input_successful_reads"],"post_purge_create_decryption")
            else:final=self.settle(consumer)
            replay=consumer.cycle();immutable(final,replay);no_financial_change(final,replay);consumer.stop()
            require(seed["billing_snapshot_sha256"]==final["billing_snapshot_sha256"] and seed["binding_sha256"]==final["binding_sha256"] and seed["input"]["identity_sha256"]==final["input"]["identity_sha256"],"accepted_identity_rebuilt")
        else:
            if not case.startswith("N04-runtime"):
                self.queued(writer);writer.simple("action","action_pending")
                if writer_role=="old":writer.simple("clock","clock_set",clock_offset_ms=-86370000)
            persisted=writer.receipt()
            if case.startswith("N04"):
                fault="schema" if "-schema-" in case else "runtime"
                injected=writer.simple("unknown","unknown_injected",fault=fault);persisted=injected["after"]
            writer.stop()
            first="old" if case=="N03-action" or case.endswith("-old") else "new"
            consumer=self.child(case,first,"first-"+first,False)
            if case=="N03-action":
                read=consumer.simple("read_action","action_read")
                require(read["payload_ref"]==persisted["input"]["payload_ref"] and read["bytes"]>0,"old_schema2_read")
                persisted=consumer.inspect()
            self.wait_due(consumer,persisted,case!="N03-action")
            if case=="N02":persisted=consumer.simple("purge","purged")["after"]
            final=self.held_unchanged_replay(consumer,persisted)
            if case in ("N02","N03-action"):
                require(final["application_state"]=="applied" and final["operation_status"]=="unknown" and final["task_status"]=="queued","cancel_ack_state")
            consumer.stop()
            if case.startswith("N04"):
                purger=self.child(case,"new","purger-new",False)
                purged=purger.simple("purge","purged")["after"];purger.stop()
                second="old" if first=="new" else "new"
                consumer=self.child(case,second,"second-"+second,False)
                final=self.held_unchanged_replay(consumer,purged)
                require(final["application_state"]!="applied" and final["applied_at"] is None,"unknown_applied")
                consumer.stop()
        calls=self.mock.snapshot(case)
        posts=[r for r in calls if r["method"]=="POST"]
        deletes=[r for r in calls if r["method"]=="DELETE"]
        require(len(posts)==1 and posts[0]["body_sha256"]==seed["oracle"]["request_body_sha256"],"post_count_original_body")
        require(len(deletes)==(0 if case in ("N01","N03-create") or case.startswith("N04-runtime") else 1),"delete_count")
        result={"case_id":case,"status":"passed","seed":seed,"final":final,"http":calls}
        (self.root/(case+"-result.json")).write_text(json.dumps(result,indent=2)+"\n",encoding="utf-8");self.results.append(result)
        print("B1B_STAGE "+case+" passed",flush=True)
    def close(self):
        # Only child PIDs created by this controller; no name-based kill,
        # container removal, database removal, or production process control.
        states=[]
        for child in self.children:
            forced=False
            if child.process.poll() is None:
                forced=True
                child.process.terminate()
                try:child.process.wait(timeout=20)
                except subprocess.TimeoutExpired:child.process.kill();child.process.wait(timeout=5)
            states.append({"case_id":child.case,"instance":child.instance,"role":child.role,"source_sha":self.builds[child.role]["source"],"binary_sha256":self.builds[child.role]["sha256"],"pid":child.process.pid,"exit_code":child.process.returncode,"forced_stop":forced,"clean_exit":child.clean_exit})
        (self.root/"child-exits.json").write_text(json.dumps(states,indent=2)+"\n",encoding="utf-8")
        self.mock.close()

def main():
    parser=argparse.ArgumentParser();parser.add_argument("--run-id",required=True);parser.add_argument("--build-manifest",required=True);parser.add_argument("--evidence",required=True);args=parser.parse_args()
    matrix=None
    try:
        matrix=Matrix(args)
        for case in CASES:matrix.run_case(case)
        require(len(matrix.results)==8,"missing_case")
        (matrix.root/"matrix-passed.json").write_text(json.dumps({"status":"passed","run_id":matrix.run,"families":4,"cases":8,"old_source":OLD,"new_source":matrix.builds["new"]["source"],"cases_passed":[r["case_id"] for r in matrix.results]},indent=2)+"\n",encoding="utf-8")
    except Exception as exc:
        if matrix is not None:(matrix.root/"matrix-failed.json").write_text(json.dumps({"status":"failed","safe_code":str(exc) if re.fullmatch(r"[a-zA-Z0-9_]+",str(exc)) else "controller_failure","completed_cases":[r["case_id"] for r in matrix.results]},indent=2)+"\n",encoding="utf-8")
        raise SystemExit("B1b matrix failed; preserve evidence") from None
    finally:
        if matrix is not None:matrix.close()

if __name__=="__main__":main()
