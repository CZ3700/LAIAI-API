#!/usr/bin/env python3
"""Release safety regression tests; never access production or a real Docker daemon.

Run on Linux as root in a disposable container:
  python3 infra/release/tests/test_release.py
Only the state tests are portable to non-Linux hosts. CLI cases copy the real
coordinator/state program into a temporary directory, replace network/process
boundaries with deterministic executable fixtures, and assert observable event
order and refusal behavior. Database assertions and encryption have separate
module suites; their probes are stubbed here to exercise the coordinator.
"""
import copy
import hashlib
import json
import os
from pathlib import Path
import platform
import shlex
import shutil
import subprocess
import sys
import tempfile
import time
import unittest


RELEASE_DIR = Path(__file__).resolve().parents[1]
STATE_PY = RELEASE_DIR / "state.py"
OLD_SHA, TARGET_SHA, NEXT_SHA = (c * 40 for c in "123")
OLD_IMAGES = {role: "sha256:" + digit * 64 for role, digit in zip(("core", "caddy", "mysql", "redis"), "abcd")}
NEW_IMAGES = {"core": "sha256:" + "e" * 64, "caddy": "sha256:" + "f" * 64}
SECRET = "PRIVATE_TEST_SECRET_DO_NOT_PRINT_0123456789"


MOCK_CODE = r'''#!/usr/bin/env python3
import hashlib, json, os, pathlib, sys
p=pathlib.Path(os.environ['MOCK_CONTROL'])
c=json.loads(p.read_text()); args=sys.argv[1:]
name=pathlib.Path(sys.argv[0]).name
def save(): p.write_text(json.dumps(c))
def event(v):
    with open(os.environ['MOCK_EVENTS'], 'a') as f: f.write(v+'\n')
def fail(): sys.exit(7)
def snap():
    out=[]
    for role in ('core','caddy','mysql','redis'):
        env=c['environments'][role]
        out.append({'Id':c['ids'][role], 'Image':c['images'][role],
          'Config':{'Labels':{'com.docker.compose.project':'infra','com.docker.compose.service':role,
            'org.opencontainers.image.revision':c['target'] if c['images'][role] in c['new_images'].values() else c['old']},
            'Env':[k+'='+v for k,v in env.items()]}, 'HostConfig':{'ReadonlyRootfs':True},
          'State':{'Running': c.get(role+'_running',True),'Health':{'Status':'healthy'}},
          'Mounts':c['mounts'][role]})
    return out
def compose():
    index=args.index('compose'); a=args[index+1:]
    positions=[i for i,v in enumerate(a) if v=='-f']; raw=pathlib.Path(a[positions[-1]+1]).read_text()
    if raw.lstrip().startswith('{'): overlay=json.loads(raw)
    else:
        overlay={'services':{}}; role=None
        for line in raw.splitlines():
            if line.startswith('  ') and not line.startswith('    '):
                role=line.strip().rstrip(':'); overlay['services'][role]={}
            elif line.startswith('    '):
                key,_,value=line.strip().partition(': ')
                overlay['services'][role][key]=None if value.startswith('!reset') else json.loads(value)
    if 'config' in a:
        result={'name':'infra','services':{},'volumes':{}}
        for role in ('core','caddy','mysql','redis'):
            service={'image':overlay['services'][role]['image'],'pull_policy':'never',
               'environment':dict(c['environments'][role]), 'volumes':[]}
            if role=='core' and c.get('compose_hmac'): service['environment']['LAIAI_VIDEO_HMAC_SECRET']=c['compose_hmac']
            for m in c['mounts'][role]:
                if role=='caddy' and m['Destination']=='/etc/caddy/Caddyfile':
                    service['volumes']+=overlay['services']['caddy']['volumes']; continue
                service['volumes'].append({'type':m['Type'],'source':m['Source'],'target':m['Destination'],'read_only':not m['RW']})
            result['services'][role]=service
        print(json.dumps(result)); return
    if 'stop' in a:
        event('stop-core'); c['core_running']=False; save(); return
    if 'up' in a:
        role=a[-1]; event('up-'+role); c['images'][role]=overlay['services'][role]['image']
        generation=c.setdefault('generations',{}).get(role,0)+1; c['generations'][role]=generation
        c['ids'][role]=hashlib.sha256((role+':'+str(generation)).encode()).hexdigest()
        if role=='core':
            c['core_running']=True
            if c.get('image_mismatch'): c['images'][role]='sha256:'+'9'*64
            if c.get('running_hmac_changed'): c['environments']['core']['LAIAI_VIDEO_HMAC_SECRET']='changed-after-start-fixture-secret-0123456789'
        if role=='caddy':
            c['caddy_running']=True
            c['caddy_config_path']=overlay['services']['caddy']['volumes'][0]['source']
            c['active_config']=json.loads(pathlib.Path(c['caddy_config_path']).read_text())
            c['mounts']['caddy']=[{'Type':'bind','Source':c['caddy_config_path'],'Destination':'/etc/caddy/Caddyfile','RW':False}]
        save(); return
    if 'run' in a:
        tool=a[a.index('--entrypoint')+1].split('/')[-1]; event(tool)
        if tool=='migrate':
            if c.get('migration_fails'): fail()
            c['migrated']=True; save(); print('migration successful'); return
        if tool=='schema-doctor': print('schema valid'); return
        if tool=='backfill-funds':
            count=c.get('backfill_calls',0); c['backfill_calls']=count+1; save()
            cutoff=a[a.index('-before')+1]
            if count < c.get('batch_limit_count',0):
                print('batch limit reached: 100 source rows projected; continue with the same cutoff'); return
            n=5 if count==c.get('batch_limit_count',0) or c.get('nonempty_replay') else 0
            print(f'complete: {n} source rows projected before {cutoff}'); return
    raise RuntimeError('unexpected compose operation')
if name=='git':
    if args[0]=='ls-remote': print(c['remote']+'\trefs/heads/main')
    elif args[0]=='init': pathlib.Path(args[-1]).mkdir(parents=True,exist_ok=True)
    elif 'fetch' in args:
        c['fetched']=c['remote']
        if c.get('main_moves_during_fetch'): c['fetched']=c['next']
        save()
    elif args[0]=='clone':
        d=pathlib.Path(args[-1]); d.mkdir(parents=True,exist_ok=True)
        for f in ('infra/core.Dockerfile','infra/caddy.Dockerfile','infra/docker-compose.yml','core/cmd/backfill-funds/main.go','core/cmd/schema-doctor/main.go','migrations/0001_old.sql','migrations/0002_new.sql'):
            dest=d/f; dest.parent.mkdir(parents=True,exist_ok=True); dest.write_text('fixture\n')
    elif 'checkout' in args:
        if '--detach' not in args or args[-1] != c['target']: raise RuntimeError('source checkout is not pinned to the planned SHA')
    elif 'rev-parse' in args:
        if args[0]=='-C': print(c['old'] if args[1]==c['live'] else c['target'])
        else: print(c.get('fetched',c['remote']))
    elif 'status' in args:
        if args[1]==c['live'] and c.get('dirty'): print(' M infra/Caddyfile')
        elif args[1]!=c['live'] and c.get('source_dirty'): print('?? unknown.txt')
    elif 'rev-list' in args: print('0\t1')
    elif 'diff' in args: print(' 1 file changed')
    elif 'merge-base' in args:
        if c.get('diverged'): fail()
    elif 'cat-file' in args: pass
    else: raise RuntimeError('unexpected git operation')
elif name=='docker':
    if args[0]=='info': print('/tmp')
    elif args[:3]==['compose','version','--short']: print(c.get('compose_version','2.29.7'))
    elif args[0]=='ps':
        role=next(v.split('=')[-1] for v in args if 'com.docker.compose.service=' in v)
        print(c['ids'][role])
    elif args[0]=='inspect':
        if '--format' not in args: print(json.dumps(snap()))
        else:
            fmt=args[args.index('--format')+1]; role=next(k for k,v in c['ids'].items() if v==args[-1])
            if fmt=='{{.Image}}': print(c['images'][role])
            elif 'revision' in fmt: print(c['old'])
            elif fmt=='{{.State.Running}}': print('true' if c.get(role+'_running',True) else 'false')
            elif fmt=='{{.State.Health.Status}}': print('healthy')
            else: raise RuntimeError('unexpected inspect format')
    elif args[:2]==['image','inspect']:
        image=args[-1]
        if image.startswith('sha256:'): print(image)
        elif image in c.get('tags',{}): print(c['tags'][image])
        else: sys.exit(1)
    elif args[0]=='build':
        role='core' if args[args.index('-f')+1].endswith('/core.Dockerfile') else 'caddy'; event('build-'+role)
        if c.get('build_fails')==role: print(c['secret']); fail()
        c.setdefault('tags',{})[args[args.index('-t')+1]]=c['new_images'][role]; save()
    elif args[:2]==['image','save']:
        if '-o' in args: pathlib.Path(args[args.index('-o')+1]).write_bytes(b'image archive fixture')
        else: print('image archive fixture')
    elif args[:2]==['image','load']: event('image-load')
    elif args[0]=='start':
        role=next(k for k,v in c['ids'].items() if v==args[-1]); event('start-original' if role=='core' else 'start-'+role)
        c[role+'_running']=True
        if role=='caddy' and c.get('caddy_config_path'): c['active_config']=json.loads(pathlib.Path(c['caddy_config_path']).read_text())
        save()
    elif args[0]=='stop':
        if args[-1] != c['ids']['caddy']: fail()
        event('stop-caddy')
        if c.get('stop_caddy_fails'): fail()
        c['caddy_running']=False; save()
    elif args[0]=='run': pass
    elif args[0]=='exec':
        cid=next(v for v in args[1:] if not v.startswith('-'))
        if cid not in c['ids'].values(): fail()
        if c['ids']['caddy'] in args and not c.get('caddy_running',True): fail()
        if 'adapt' in args or 'wget' in args: print(json.dumps(c.get('active_config',{'apps':{'http':{'servers':{'srv0':{'routes':[]}}}}})))
        elif 'reload' in args:
            content=sys.stdin.read(); c['active_config']=json.loads(content); c['maintenance']='Maintenance in progress' in content; save(); event('maintenance' if c['maintenance'] else 'open')
            if not c['maintenance']:
                c['opened_attempt']=True; save()
                if c.get('open_reload_fails'): fail()
            elif c.get('opened_attempt') and c.get('maintenance_restore_fails'): fail()
        elif 'validate' in args:
            content=sys.stdin.read()
            if c.get('open_validate_fails') and 'Maintenance in progress' not in content: event('open-validation-failed'); fail()
        else: raise RuntimeError('unexpected docker exec')
    elif args[0]=='compose': compose()
    else: raise RuntimeError('unexpected Docker operation')
elif name=='curl':
    if c.get('health_fails'): fail()
    if c.get('open_health_fails') and c.get('opened_attempt') and not c.get('maintenance'): fail()
    if '-w' in args: print('503' if c.get('maintenance') else '200',end='')
elif name=='df': print('Filesystem 1048576-blocks Used Available Capacity Mounted on\nfixture 100000 1 '+str(c.get('disk_free',50000))+' 1% /')
elif name=='openssl': pass
elif name=='probe':
    action=args[0]
    if action=='ci':
        event('ci:'+args[1]);
        if c.get('ci_fails'): fail()
        print('https://github.com/CZ3700/LAIAI-API/actions/runs/1')
    elif action=='approval':
        event('approval:'+args[1])
        if args[1] in c.get('deny_approvals',[]): fail()
    elif action=='data':
        phase,root=args[1],pathlib.Path(args[2]); event('data:'+phase)
        if c.get('data_fails')==phase: fail()
        (root/('data-'+phase+'.json')).write_text(json.dumps({'funds_fingerprint':'fixed'}))
        (root/'schema.txt').write_text('0001_old\n'+('0002_new\n' if c.get('migrated') else ''))
    elif action=='backup':
        root=pathlib.Path(args[1]); event('final-backup' if root.name=='final-backup' else 'backup')
        if c.get('backup_fails')==root.name: print(c['secret'],file=sys.stderr); fail()
        (root/'images.tar').write_bytes(b'old images')
        (root/'checksums.sha256').write_text(hashlib.sha256(b'old images').hexdigest()+'  images.tar\n')
        (root/'backup.ok').write_text('verified\n')
else: raise RuntimeError('unexpected mock command '+name)
'''


class StateTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="laiai-state-test-")
        self.root = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def run_state(self, *args, value=None, ok=True):
        result = subprocess.run([sys.executable, str(STATE_PY), *map(str, args)],
                                input=None if value is None else json.dumps(value), text=True, capture_output=True)
        self.assertNotIn(SECRET, result.stdout + result.stderr)
        if ok:
            self.assertEqual(result.returncode, 0, result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0)
        return result

    def test_ci_latest_exact_main_run_must_succeed(self):
        good = {"head_sha": TARGET_SHA, "head_branch": "main", "event": "push", "path": ".github/workflows/ci.yml", "id": 1,
                "status": "completed", "conclusion": "success", "html_url": "https://github.com/test/runs/1"}
        self.run_state("ci", TARGET_SHA, value={"workflow_runs": [good]})
        bad = dict(good, id=2, conclusion="failure")
        self.run_state("ci", TARGET_SHA, value={"workflow_runs": [good, bad]}, ok=False)
        self.run_state("ci", NEXT_SHA, value={"workflow_runs": [good]}, ok=False)
        self.run_state("ci", TARGET_SHA, value={"workflow_runs": [dict(good, event="pull_request")]}, ok=False)

    def test_compose_version_requires_reset_support(self):
        for version in ("2.24.4", "v2.29.7", "2.29.7-desktop.1", "5.6.0"):
            self.run_state("compose-version", version)
        for version in ("2.24.3", "1.29.2", "latest", "2.29"):
            self.run_state("compose-version", version, ok=False)

    def test_schema_rejects_unknown_and_pending_exact(self):
        migrations = self.root / "migrations"
        migrations.mkdir()
        (migrations / "0001_old.sql").write_text("fixture")
        (migrations / "0002_new.sql").write_text("fixture")
        applied = self.root / "applied.txt"
        applied.write_text("0001_old\n")
        self.run_state("schema", applied, migrations, "subset")
        self.run_state("schema", applied, migrations, "exact", ok=False)
        applied.write_text("0001_old\n9999_unexpected\n")
        self.run_state("schema", applied, migrations, "subset", ok=False)

    def test_funds_change_blocks_release(self):
        before, after = self.root / "before.json", self.root / "after.json"
        before.write_text(json.dumps({"funds_fingerprint": "same"}))
        after.write_text(json.dumps({"funds_fingerprint": "changed"}))
        self.run_state("funds", before, after, ok=False)

    def test_inspect_stores_hashes_without_environment_secrets(self):
        values = []
        for role in OLD_IMAGES:
            values.append({"Id": role + "-container", "Image": OLD_IMAGES[role],
                           "Config": {"Labels": {"com.docker.compose.service": role}, "Env": ["TOKEN=" + SECRET]},
                           "HostConfig": {"ReadonlyRootfs": True}, "State": {"Running": True, "Health": {"Status": "healthy"}}, "Mounts": []})
        snapshot = self.root / "snapshot.json"
        self.run_state("inspect", snapshot, value=values)
        self.assertNotIn(SECRET, snapshot.read_text())
        self.assertEqual(json.loads(snapshot.read_text())["core"]["env_hashes"]["TOKEN"], hashlib.sha256(SECRET.encode()).hexdigest())
        self.run_state("inspect", snapshot, value=values + [values[0]], ok=False)
        values[2]["State"]["Health"]["Status"] = "unhealthy"
        self.run_state("inspect", snapshot, value=values, ok=False)

    def test_approval_binds_stage_commit_time_and_unchanged_evidence(self):
        plan = self.root / "plan.json"
        plan.write_text(json.dumps({"commit": TARGET_SHA}))
        evidence = self.root / "evidence.txt"
        evidence.write_text("Reviewed restore rehearsal and customer acceptance evidence.\n")
        self.run_state("approve", plan, "before-open", evidence, ok=False)
        self.run_state("approve", plan, "before-maintenance", evidence)
        self.run_state("approval-check", plan, "before-maintenance")
        record = self.root / "approvals/before-maintenance.json"
        saved = json.loads(record.read_text())
        for changes in ({"commit": NEXT_SHA}, {"stage": "before-open"}, {"created_at": int(time.time()) - 86401}, {"created_at": int(time.time()) + 60}):
            record.write_text(json.dumps(dict(saved, **changes)))
            self.run_state("approval-check", plan, "before-maintenance", ok=False)
        record.write_text(json.dumps(saved))
        evidence.write_text("The reviewed evidence has subsequently changed.\n")
        self.run_state("approval-check", plan, "before-maintenance", ok=False)

    def test_maintenance_preserves_routes_and_health(self):
        original, active = self.root / "original.json", self.root / "active.json"
        route = {"handle": [{"handler": "reverse_proxy", "upstreams": [{"dial": "core:8080"}]}]}
        original.write_text(json.dumps({"apps": {"http": {"servers": {"s1": {"routes": [route]}, "s2": {"routes": []}}}}}))
        self.run_state("maintenance", original, active)
        result = json.loads(active.read_text())
        for server in result["apps"]["http"]["servers"].values():
            self.assertEqual(server["routes"][0]["handle"][0]["status_code"], 503)
            self.assertEqual(server["routes"][0]["match"], [{"not": [{"path": ["/healthz"]}]}])
        self.assertEqual(result["apps"]["http"]["servers"]["s1"]["routes"][1], route)


@unittest.skipUnless(platform.system() == "Linux" and hasattr(os, "geteuid") and os.geteuid() == 0,
                     "CLI tests require a disposable root Linux environment")
class CLITests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="laiai-release-test-")
        self.root = Path(self.tmp.name)
        self.tools = self.root / "tool copy"
        (self.tools / "infra").mkdir(parents=True)
        shutil.copy(RELEASE_DIR.parent / "release.sh", self.tools / "infra/release.sh")
        shutil.copytree(RELEASE_DIR, self.tools / "infra/release", ignore=shutil.ignore_patterns("tests", "__pycache__"))
        self.live = self.root / "live source"
        for f in ("infra/.env", "infra/Caddyfile", "infra/docker-compose.yml", "infra/mysql/my.cnf", "infra/redis/redis.conf"):
            p = self.live / f
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_text(SECRET)
            p.chmod(0o600)
        self.key = self.root / "backup key"
        self.key.write_text("a" * 64 + "\n")
        self.key.chmod(0o600)
        self.state_dir = self.root / "state records"
        self.control = self.root / "control.json"
        self.events = self.root / "events.txt"
        self.bin = self.root / "bin"
        self.bin.mkdir()
        for name in ("git", "docker", "curl", "df", "openssl", "probe"):
            p = self.bin / name
            p.write_text(MOCK_CODE)
            p.chmod(0o700)
        self.env = dict(os.environ, PATH=str(self.bin) + os.pathsep + os.environ["PATH"],
                        MOCK_CONTROL=str(self.control), MOCK_EVENTS=str(self.events))
        environments = {"core": {k: SECRET for k in ("LAIAI_DSN", "LAIAI_AUTH_SECRET", "LAIAI_DATA_KEYS", "LAIAI_DATA_KEY_ACTIVE", "LAIAI_REDIS_ADDR", "LAIAI_REDIS_PASSWORD", "LAIAI_VIDEO_HMAC_SECRET")},
                        "caddy": {"LAIAI_DOMAIN": "example.test"}, "mysql": {k: SECRET for k in ("MYSQL_ROOT_PASSWORD", "MYSQL_DATABASE", "MYSQL_USER", "MYSQL_PASSWORD")},
                        "redis": {"REDIS_PASSWORD": SECRET}}
        mounts = {role: [] for role in OLD_IMAGES}
        mounts["caddy"] = [{"Type": "bind", "Source": str(self.live / "infra/Caddyfile"), "Destination": "/etc/caddy/Caddyfile", "RW": False}]
        self.set_control({"remote": TARGET_SHA, "target": TARGET_SHA, "old": OLD_SHA, "next": NEXT_SHA,
                          "live": str(self.live), "secret": SECRET, "images": OLD_IMAGES, "new_images": NEW_IMAGES,
                          "environments": environments, "mounts": mounts,
                          "ids": {role: digit * 64 for role, digit in zip(OLD_IMAGES, "1234")}})
        module = self.tools / "infra/release"
        (module / "backup.sh").write_text('release_backup() { probe backup "$RUN_DIR" 2>/dev/null; }\n')
        (module / "check-data.sh").write_text('check_data() { probe data "$1" "$RUN_DIR"; }\n')
        (module / "rollback-snapshot.sh").write_text('freeze_rollback_runtime() { printf "{}\\n" > "$RUN_DIR/config/rollback-compose.json"; (cd "$RUN_DIR/config" && sha256sum rollback-compose.json > rollback-compose.sha256); }\n')
        self.ci_hook = self.root / "ci-hook.sh"
        self.ci_hook.write_text('#!/usr/bin/env bash\nexec probe ci "$@"\n')
        self.check_hook = self.root / "check-hook.sh"
        self.check_hook.write_text('#!/usr/bin/env bash\nexec probe approval "$@"\n')
        self.ci_hook.chmod(0o700)
        self.check_hook.chmod(0o700)
        config = {"LIVE_DIR": str(self.live), "STATE_DIR": str(self.state_dir), "PUBLIC_ORIGIN": "https://example.test",
                  "BACKUP_KEY_FILE": str(self.key), "MIN_FREE_MB": "1", "MIN_AVAILABLE_MB": "1",
                  "CI_CHECK_HOOK": str(self.ci_hook), "CHECK_HOOK": str(self.check_hook)}
        config.update({k: "fixture@sha256:" + "a" * 64 for k in ("GO_IMAGE", "ALPINE_IMAGE", "NODE_IMAGE", "CADDY_BASE_IMAGE")})
        self.config = self.root / "release.conf"
        self.config.write_text("\n".join(k + "=" + shlex.quote(v) for k, v in config.items()) + "\n")
        self.config.chmod(0o600)
        self.release_id = None

    def tearDown(self):
        self.tmp.cleanup()

    def set_control(self, value):
        self.control.write_text(json.dumps(value))

    def change(self, **values):
        c = json.loads(self.control.read_text())
        c.update(values)
        self.set_control(c)

    def log(self):
        return self.events.read_text().splitlines() if self.events.exists() else []

    def call(self, command, ok=True, include_release=True, error=None):
        args = ["bash", str(self.tools / "infra/release.sh"), "--config", str(self.config), command, "--yes"]
        if include_release and self.release_id:
            args.extend(["--release", self.release_id])
        result = subprocess.run(args, text=True, capture_output=True, env=self.env, timeout=40)
        self.assertNotIn(SECRET, result.stdout + result.stderr)
        if ok:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        if error:
            self.assertIn(error, result.stdout + result.stderr)
        return result

    def prepare(self, through="build"):
        self.call("preflight")
        self.release_id = next((self.state_dir / "releases").iterdir()).name
        if through in ("backup", "build"):
            self.call("backup")
        if through == "build":
            self.call("build")

    def run_dir(self):
        return self.state_dir / "releases" / self.release_id

    def assert_no_stop_or_up(self):
        self.assertFalse(any(v == "stop-core" or v.startswith("up-") for v in self.log()), self.log())

    def test_main_changes_while_fetching_block_preflight(self):
        self.change(main_moves_during_fetch=True)
        self.call("preflight", ok=False, error="main moved during preflight")
        self.assert_no_stop_or_up()

    def test_dirty_live_checkout_is_never_overwritten(self):
        self.change(dirty=True)
        self.call("preflight", ok=False, error="Live checkout has unreviewed changes")
        self.assert_no_stop_or_up()

    def test_insufficient_capacity_blocks_before_backup_or_build(self):
        self.change(disk_free=0)
        self.call("preflight", ok=False, error="Insufficient release/build capacity")
        self.assertFalse(any(v.startswith("build-") or v == "backup" for v in self.log()))
        self.assert_no_stop_or_up()

    def test_old_compose_version_is_rejected_before_deployment(self):
        self.change(compose_version="2.23.3")
        self.call("preflight", ok=False, error="Docker Compose >=2.24.4 is required")
        self.assert_no_stop_or_up()

    def test_backup_key_read_only_mode_rejected_during_preflight(self):
        self.key.chmod(0o400)
        self.call("preflight", ok=False, error="Backup key must have mode 0600")
        self.assert_no_stop_or_up()
        self.assertNotIn("backup", self.log())

    def test_short_backup_passphrase_first_line_rejected_during_preflight(self):
        self.key.write_text("short\n" + "a" * 64 + "\n")
        self.call("preflight", ok=False, error="Backup key first line must contain at least 32 characters")
        self.assert_no_stop_or_up()
        self.assertNotIn("backup", self.log())

    def test_existing_hmac_cannot_change_in_candidate_compose(self):
        self.change(compose_hmac="changed-fixture-HMAC-secret-0123456789")
        self.call("preflight", ok=False, error="existing runtime configuration changed: core/LAIAI_VIDEO_HMAC_SECRET")
        self.assert_no_stop_or_up()

    def test_stage_order_requires_backup_before_build(self):
        self.prepare("preflight")
        self.call("build", ok=False, error="Missing successful stage: backup")
        self.assertFalse(any(v.startswith("build-") for v in self.log()))
        self.assert_no_stop_or_up()

    def test_backup_failure_does_not_stop_services(self):
        self.prepare("preflight")
        self.change(backup_fails="backup")
        self.call("backup", ok=False)
        self.assert_no_stop_or_up()
        self.assertFalse((self.run_dir() / "backup.ok").exists())
        self.assertEqual((self.run_dir() / "failed-stage").read_text().strip(), "backup")

    def test_build_failure_does_not_stop_services_or_print_build_secret(self):
        self.prepare("backup")
        self.change(build_fails="core")
        self.call("build", ok=False, error="Core build failed")
        self.assert_no_stop_or_up()
        self.assertFalse((self.run_dir() / "build.ok").exists())
        self.assertEqual((self.run_dir() / "failed-stage").read_text().strip(), "build")

    def test_pin_does_not_follow_main_and_deploy_refuses_drift(self):
        self.prepare("backup")
        self.change(remote=NEXT_SHA)
        self.call("build")
        plan = json.loads((self.run_dir() / "plan.json").read_text())
        self.assertEqual(plan["commit"], TARGET_SHA)
        self.call("deploy", ok=False, error="main changed after preflight")
        self.assert_no_stop_or_up()

    def test_successful_deploy_orders_maintenance_backup_migration_backfill_start(self):
        self.prepare()
        self.change(batch_limit_count=1)
        self.call("deploy")
        events = self.log()
        keys = ["maintenance", "stop-core", "final-backup", "migrate", "schema-doctor", "backfill-funds", "up-core", "up-caddy"]
        positions = [events.index(k) for k in keys]
        self.assertEqual(positions, sorted(positions), events)
        self.assertEqual(events.count("migrate"), 2)
        self.assertEqual(events.count("backfill-funds"), 3)
        self.assertNotIn("image-load", events)

    def test_migration_failure_keeps_maintenance_without_auto_restore(self):
        self.prepare()
        self.change(migration_fails=True)
        self.call("deploy", ok=False)
        self.assertIn("final-backup", self.log())
        self.assertIn("migrate", self.log())
        self.assertFalse(any(v.startswith("up-") or v == "image-load" or v == "open" for v in self.log()))
        self.assertTrue((self.run_dir() / "migration-started.ok").exists())

    def test_nonempty_backfill_replay_blocks_start(self):
        self.prepare()
        self.change(nonempty_replay=True)
        self.call("deploy", ok=False)
        self.assertGreaterEqual(self.log().count("backfill-funds"), 2)
        self.assertFalse(any(v.startswith("up-") for v in self.log()))

    def test_rollback_after_migration_requires_compatibility_evidence(self):
        self.prepare()
        self.change(migration_fails=True)
        self.call("deploy", ok=False)
        before = len(self.log())
        self.change(deny_approvals=["before-rollback"])
        self.call("rollback", ok=False)
        after = self.log()[before:]
        self.assertIn("approval:before-rollback", after)
        self.assertFalse(any(v.startswith("up-") or v in ("image-load", "stop-core") for v in after))

    def test_running_image_mismatch_blocks_success(self):
        self.prepare()
        self.change(image_mismatch=True)
        self.call("deploy", ok=False)
        self.assertIn("up-core", self.log())
        self.assertFalse((self.run_dir() / "verify.ok").exists())
        self.assertFalse((self.run_dir() / "open.ok").exists())

    def test_running_hmac_change_blocks_opening(self):
        self.prepare()
        self.change(running_hmac_changed=True)
        self.call("deploy", ok=False, error="running protected configuration changed: core/LAIAI_VIDEO_HMAC_SECRET")
        self.assertIn("up-core", self.log())
        self.assertNotIn("open", self.log())
        self.assertFalse((self.run_dir() / "open.ok").exists())

    def test_open_health_failure_restores_maintenance(self):
        self.prepare()
        self.change(open_health_fails=True)
        self.call("deploy", ok=False, error="maintenance was restored and publicly verified")
        self.assertEqual(self.log()[-2:], ["open", "maintenance"])
        self.assertTrue(json.loads(self.control.read_text())["maintenance"])
        self.assertFalse((self.run_dir() / "open.ok").exists())

    def test_open_reload_failure_restores_maintenance(self):
        self.prepare()
        self.change(open_reload_fails=True)
        self.call("deploy", ok=False, error="maintenance was restored and publicly verified")
        self.assertEqual(self.log()[-2:], ["open", "maintenance"])
        self.assertTrue(json.loads(self.control.read_text())["maintenance"])
        self.assertFalse((self.run_dir() / "open.ok").exists())

    def test_open_validation_failure_never_reloads_open_routes(self):
        self.prepare()
        self.change(open_validate_fails=True)
        self.call("deploy", ok=False, error="maintenance was restored and publicly verified")
        self.assertIn("open-validation-failed", self.log())
        self.assertNotIn("open", self.log())
        self.assertTrue(json.loads(self.control.read_text())["maintenance"])
        self.assertFalse((self.run_dir() / "open.ok").exists())

    def test_unconfirmed_maintenance_recovery_stops_public_ingress(self):
        self.prepare()
        self.change(open_health_fails=True, maintenance_restore_fails=True)
        self.call("deploy", ok=False, error="Caddy was stopped to close ingress")
        self.assertEqual(self.log()[-3:], ["open", "maintenance", "stop-caddy"])
        self.assertFalse(json.loads(self.control.read_text())["caddy_running"])
        self.assertFalse((self.run_dir() / "open.ok").exists())

    def stop_failed_candidate_ingress(self):
        self.prepare()
        self.change(open_health_fails=True, maintenance_restore_fails=True)
        self.call("deploy", ok=False, error="Caddy was stopped to close ingress")
        self.change(open_health_fails=False, maintenance_restore_fails=False)

    def test_stopped_caddy_rollback_restores_maintenance_before_draining(self):
        self.stop_failed_candidate_ingress()
        before = len(self.log())
        self.call("rollback")
        events = self.log()[before:]
        order = [events.index(k) for k in ("up-caddy", "data:drained", "stop-core", "up-core", "open")]
        self.assertEqual(order, sorted(order), events)
        control = json.loads(self.control.read_text())
        self.assertTrue(control["caddy_running"])
        self.assertEqual(control["images"]["core"], OLD_IMAGES["core"])
        self.assertEqual(control["images"]["caddy"], OLD_IMAGES["caddy"])
        self.assertTrue((self.run_dir() / "rolled-back.ok").exists())

    def test_stopped_caddy_rollback_still_rejects_changed_data_container(self):
        self.stop_failed_candidate_ingress()
        control = json.loads(self.control.read_text())
        control["ids"]["mysql"] = "8" * 64
        self.set_control(control)
        before = len(self.log())
        self.call("rollback", ok=False, error="data service changed since this release: mysql")
        self.assertEqual(self.log()[before:], [])
        self.assertFalse(json.loads(self.control.read_text())["caddy_running"])

    def test_stopped_unknown_caddy_mount_cannot_be_restarted(self):
        self.stop_failed_candidate_ingress()
        control = json.loads(self.control.read_text())
        control["mounts"]["caddy"][0]["Source"] = "/unrelated/runtime-config.json"
        self.set_control(control)
        before = len(self.log())
        self.call("rollback", ok=False, error="stopped Caddy is not an unchanged original or this release configuration")
        self.assertEqual(self.log()[before:], [])
        self.assertFalse(json.loads(self.control.read_text())["caddy_running"])

    def test_pre_migration_stopped_caddy_recovers_before_original_core(self):
        self.prepare()
        self.change(backup_fails="final-backup")
        self.call("deploy", ok=False)
        self.change(caddy_running=False)
        before = len(self.log())
        self.call("rollback")
        events = self.log()[before:]
        self.assertLess(events.index("up-caddy"), events.index("start-original"))
        self.assertNotIn("up-core", events)
        self.assertNotIn("migrate", events)
        self.assertTrue((self.run_dir() / "rolled-back.ok").exists())

    def test_already_open_is_verified_without_reloading_routes(self):
        self.prepare()
        self.call("deploy")
        before = len(self.log())
        self.call("open")
        self.assertNotIn("open", self.log()[before:])
        self.assertNotIn("maintenance", self.log()[before:])
        self.assertTrue((self.run_dir() / "open.ok").exists())

    def test_stale_active_release_cannot_roll_back_runtime(self):
        self.prepare()
        self.change(migration_fails=True)
        self.call("deploy", ok=False)
        (self.state_dir / "active-release").write_text("20990101T000000Z-999999999999\n")
        before = len(self.log())
        self.call("rollback", ok=False, error="Another release owns the runtime")
        self.assertEqual(self.log()[before:], [])

    def test_rollback_rejects_changed_data_container(self):
        self.prepare()
        self.change(migration_fails=True)
        self.call("deploy", ok=False)
        c = json.loads(self.control.read_text())
        c["ids"]["mysql"] = "8" * 64
        self.set_control(c)
        before = len(self.log())
        self.call("rollback", ok=False, error="data service changed since this release: mysql")
        self.assertEqual(self.log()[before:], [])

    def test_rollback_rejects_changed_caddy_routes(self):
        self.prepare()
        self.change(migration_fails=True)
        self.call("deploy", ok=False)
        self.change(active_config={"apps": {"http": {"servers": {"unrelated": {"routes": []}}}}})
        before = len(self.log())
        self.call("rollback", ok=False, error="active Caddy configuration was changed outside this release")
        self.assertEqual(self.log()[before:], [])

    def test_missing_post_deploy_acceptance_keeps_maintenance(self):
        self.prepare()
        self.change(deny_approvals=["before-open"])
        self.call("deploy", ok=False)
        self.assertTrue((self.run_dir() / "deployed.ok").exists())
        self.assertFalse((self.run_dir() / "open.ok").exists())
        self.assertNotIn("open", self.log())
        self.assertTrue(json.loads(self.control.read_text())["maintenance"])
        # Repeating deploy must not migrate or backfill again.
        before = len(self.log())
        self.call("deploy", ok=False)
        self.assertNotIn("migrate", self.log()[before:])
        self.change(deny_approvals=[])
        self.call("release")
        self.assertTrue((self.run_dir() / "open.ok").exists())
        self.assertNotIn("migrate", self.log()[before:])

    def test_preflight_and_release_resume_preserve_built_images(self):
        self.prepare()
        before = (self.run_dir() / "images.json").read_bytes()
        self.call("preflight")
        self.assertEqual((self.run_dir() / "images.json").read_bytes(), before)
        self.call("release")
        self.assertEqual(self.log().count("backup"), 1)
        self.assertEqual(self.log().count("build-core"), 1)
        self.assertEqual(self.log().count("build-caddy"), 1)
        self.assertTrue((self.run_dir() / "deployed.ok").exists())

    def test_final_backup_failure_never_migrates_or_recreates_services(self):
        self.prepare()
        self.change(backup_fails="final-backup")
        self.call("deploy", ok=False)
        self.assertIn("stop-core", self.log())
        self.assertFalse(any(v in ("migrate", "image-load") or v.startswith("up-") for v in self.log()))
        self.assertFalse((self.run_dir() / "migration-started.ok").exists())
        self.call("rollback")
        self.assertIn("start-original", self.log())
        self.assertFalse(any(v.startswith("up-") for v in self.log()))


if __name__ == "__main__":
    unittest.main(verbosity=2)
