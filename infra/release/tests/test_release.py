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
    overlay_path=pathlib.Path(a[positions[-1]+1])
    online=overlay_path.name=='online-compose.yml'
    candidate=overlay_path.name=='new-compose.yml' or online
    media_mounts=[{'Type':'bind','Source':'/var/lib/laiai/video-media','Destination':'/var/lib/laiai/video-media','RW':True},
                  {'Type':'bind','Source':'/var/lib/clamav','Destination':'/var/lib/clamav','RW':False}]
    if online:
        overlay={'services':{role:{'image':c['new_images'].get(role,c['images'][role])} for role in ('core','caddy','mysql','redis')}}
        overlay['services']['caddy']['volumes']=[{'type':'bind','source':str(overlay_path.parent/'config/active.json'),'target':'/etc/caddy/Caddyfile','read_only':True}]
        online_core=next(line.strip().rstrip(':') for line in raw.splitlines() if line.startswith('  online-core-'))
        online_web=next(line.strip().rstrip(':') for line in raw.splitlines() if line.startswith('  online-web-'))
    elif raw.lstrip().startswith('{'): overlay=json.loads(raw)
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
                if role=='core' and not candidate and m['Destination'] in ('/var/lib/laiai/video-media','/var/lib/clamav'):
                    continue
                if role=='caddy' and m['Destination']=='/etc/caddy/Caddyfile':
                    service['volumes']+=overlay['services']['caddy']['volumes']; continue
                service['volumes'].append({'type':m['Type'],'source':m['Source'],'target':m['Destination'],'read_only':not m['RW']})
            if role=='core' and candidate:
                for m in media_mounts:
                    if not any(v['target']==m['Destination'] for v in service['volumes']):
                        service['volumes'].append({'type':m['Type'],'source':m['Source'],'target':m['Destination'],'read_only':not m['RW']})
            result['services'][role]=service
        if online:
            result['services'][online_core]=dict(result['services']['core'])
            result['services'][online_core]['restart']='unless-stopped'
            result['services'][online_web]={'image':c['new_images']['caddy'],'pull_policy':'never',
                'restart':'unless-stopped','read_only':True,
                'security_opt':['no-new-privileges:true'],
                'command':['caddy','run','--config','/etc/caddy/Caddyfile'],
                'healthcheck':{'test':['CMD','wget','-q','-O','/dev/null','http://127.0.0.1:8081/']},
                'volumes':[{'type':'bind','source':str(overlay_path.parent/'config/online-web.Caddyfile'),
                            'target':'/etc/caddy/Caddyfile','read_only':True}],
                'networks':{'laiai':None}}
        print(json.dumps(result)); return
    if 'ps' in a and online:
        print(c.get('candidate_ids',{}).get(a[-1],'')); return
    if 'up' in a and online:
        if c.get('candidate_up_fails'): fail()
        for role in (online_core,online_web):
            event('up-online-core' if role==online_core else 'up-online-web')
            c.setdefault('candidate_ids',{})[role]=hashlib.sha256(role.encode()).hexdigest()
            c.setdefault('candidate_images',{})[role]=c['new_images']['core' if role==online_core else 'caddy']
            c.setdefault('candidate_revisions',{})[role]=c['target']
        save(); return
    if 'stop' in a:
        event('stop-core'); c['core_running']=False; save(); return
    if 'up' in a:
        role=a[-1]; event('up-'+role); c['images'][role]=overlay['services'][role]['image']
        generation=c.setdefault('generations',{}).get(role,0)+1; c['generations'][role]=generation
        c['ids'][role]=hashlib.sha256((role+':'+str(generation)).encode()).hexdigest()
        if role=='core':
            c['core_running']=True
            c['mounts']['core']=sorted(media_mounts,key=lambda m:m['Destination']) if candidate else []
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
            if c.get('migration_fails'):
                print('migration read denied; '+c['secret'], file=sys.stderr); fail()
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
        files=['infra/core.Dockerfile','infra/caddy.Dockerfile','infra/docker-compose.yml','infra/Caddyfile','core/cmd/backfill-funds/main.go','core/cmd/schema-doctor/main.go']
        if c.get('carryover_0153'):
            files += ['migrations/%04d_fixture.sql' % i for i in range(1,151)]
            files += ['migrations/'+name+'.sql' for name in ('0151_callback_hint_revision','0152_video_user_task_limits','0153_video_group_asset_projects')]
        else: files += ['migrations/0001_old.sql','migrations/0002_new.sql']
        for f in files:
            dest=d/f; dest.parent.mkdir(parents=True,exist_ok=True)
            dest.write_text(c['secret'] if f=='infra/Caddyfile' else 'fixture\n')
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
        print(c['ids'][role] if role in c['ids'] else c.get('candidate_ids',{}).get(role,''))
    elif args[0]=='inspect':
        if '--format' not in args: print(json.dumps(snap()))
        else:
            fmt=args[args.index('--format')+1]
            role=next((k for k,v in c['ids'].items() if v==args[-1]),None)
            if role is None: role=next(k for k,v in c.get('candidate_ids',{}).items() if v==args[-1])
            if fmt=='{{.Image}}': print(c['images'][role] if role in c['images'] else c['candidate_images'][role])
            elif 'revision' in fmt: print(c['candidate_revisions'][role] if role.startswith('online-core-') else c['old'])
            elif fmt=='{{.State.Running}}': print('true' if c.get(role+'_running',True) else 'false')
            elif fmt=='{{.State.Health.Status}}': print('healthy')
            elif fmt=='{{.State.ExitCode}}:{{.State.OOMKilled}}': print('0:false')
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
    elif args[0]=='run':
        if c['new_images']['core'] in args and c.get('image_read_fails'):
            print('cannot read migration: '+c['secret'], file=sys.stderr); fail()
    elif args[0]=='exec':
        cid=next(v for v in args[1:] if not v.startswith('-'))
        if cid not in c['ids'].values() and cid not in c.get('candidate_ids',{}).values(): fail()
        if c['ids']['caddy'] in args and not c.get('caddy_running',True): fail()
        if cid==c['ids']['mysql'] and any('SELECT version FROM schema_migrations' in part for part in args):
            print('0001_old\n'+('0002_new' if c.get('app_only') else ''))
        elif cid in c.get('candidate_ids',{}).values():
            event('probe-online')
        elif 'adapt' in args or 'wget' in args: print(json.dumps(c['active_config']))
        elif 'reload' in args:
            content=sys.stdin.read(); c['active_config']=json.loads(content); c['maintenance']='Maintenance in progress' in content; save(); event('maintenance' if c['maintenance'] else 'open')
            if not c['maintenance']:
                c['opened_attempt']=True; save()
                if c.get('open_reload_fails'): fail()
            elif c.get('opened_attempt') and c.get('maintenance_restore_fails'): fail()
        elif any('mysqldump' in item for item in args):
            count=c.get('business_fingerprint_calls',0)+1
            c['business_fingerprint_calls']=count; save(); event('business-fingerprint')
            print('changed business rows' if c.get('business_changes') and count>1 else 'stable business rows')
        elif 'validate' in args:
            content=sys.stdin.read()
            if c.get('open_validate_fails') and 'Maintenance in progress' not in content: event('open-validation-failed'); fail()
        else: raise RuntimeError('unexpected docker exec')
    elif args[0]=='compose': compose()
    else: raise RuntimeError('unexpected Docker operation')
elif name=='curl':
    if c.get('health_fails'): fail()
    if c.get('candidate_health_fails') and 'online-core-' in json.dumps(c.get('active_config',{})): fail()
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
        if c.get('carryover_0153'):
            versions=['%04d_fixture' % i for i in range(1,151)]
            if c.get('migrated'): versions+=['0151_callback_hint_revision','0152_video_user_task_limits','0153_video_group_asset_projects']
            (root/'schema.txt').write_text('\n'.join(versions)+'\n')
        else:
            (root/'schema.txt').write_text('0001_old\n'+('0002_new\n' if c.get('migrated') or c.get('app_only') else ''))
    elif action=='backup':
        root=pathlib.Path(args[1]); event('final-backup' if root.name=='final-backup' else 'backup')
        if c.get('backup_fails')==root.name: print(c['secret'],file=sys.stderr); fail()
        (root/'images.tar').write_bytes(b'old images')
        (root/'checksums.sha256').write_text(hashlib.sha256(b'old images').hexdigest()+'  images.tar\n')
        (root/'backup.ok').write_text('verified\n')
else: raise RuntimeError('unexpected mock command '+name)
'''

CARRYOVER_STUB = r'''#!/usr/bin/env python3
import json, os, pathlib, sys
p=pathlib.Path(os.environ['MOCK_CONTROL'])
c=json.loads(p.read_text())
command=sys.argv[1]
with open(os.environ['MOCK_EVENTS'],'a') as out: out.write('carryover:'+command+'\n')
if command=='prepare':
    if c.get('duplicate_hints'): sys.exit(2)
    pathlib.Path(sys.argv[3]).write_text('{}')
elif command=='fingerprint': print('a'*64)
elif command=='baseline':
    if c.get('feature_enabled'): sys.exit(2)
    pathlib.Path(sys.argv[3]).write_text('{}')
elif command=='guard':
    c['guard_calls']=c.get('guard_calls',0)+1
    p.write_text(json.dumps(c))
    if not pathlib.Path(sys.argv[3]).exists() or c.get('feature_enabled') or c.get('fail_guard_on_call')==c['guard_calls']:
        sys.exit(2)
else: sys.exit(2)
'''


class StateTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="laiai-state-test-")
        self.root = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def run_state(self, *args, value=None, ok=True):
        result = subprocess.run([sys.executable, str(STATE_PY), *map(str, args)],
                                input=None if value is None else json.dumps(value), text=True, capture_output=True,
                                env=dict(os.environ, COMPOSE_PROJECT='infra'))
        self.assertNotIn(SECRET, result.stdout + result.stderr)
        if ok:
            self.assertEqual(result.returncode, 0, result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0)
        return result

    def test_ci_observes_exact_main_run_without_waiting_for_all_jobs(self):
        good = {"head_sha": TARGET_SHA, "head_branch": "main", "event": "push", "path": ".github/workflows/ci.yml", "id": 1,
                "status": "completed", "conclusion": "success", "html_url": "https://github.com/test/runs/1"}
        self.run_state("ci", TARGET_SHA, value={"workflow_runs": [good]})
        bad = dict(good, id=2, conclusion="failure")
        self.run_state("ci", TARGET_SHA, value={"workflow_runs": [good, bad]})
        self.run_state("ci", TARGET_SHA, value={"workflow_runs": [dict(good, status="in_progress", conclusion=None)]})
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

    def test_carryover_requires_exact_watermark_expansion(self):
        migrations = self.root / "migrations"
        migrations.mkdir()
        names = [f"{i:04d}_fixture" for i in range(1, 140)]
        names += ["0140_video_watermark", "0141_video_watermark_users"]
        for name in names:
            (migrations / f"{name}.sql").write_text("fixture")
        applied = self.root / "applied.txt"
        applied.write_text("\n".join(names[:139]) + "\n")
        plan = self.root / "plan.json"
        plan.write_text(json.dumps({"carryover": 1}))
        self.run_state("carryover-eligible", plan, applied, migrations)
        plan.write_text(json.dumps({"carryover": 0}))
        self.run_state("carryover-eligible", plan, applied, migrations, ok=False)
        plan.write_text(json.dumps({"carryover": 1}))
        applied.write_text("\n".join(names[:138]) + "\n")
        self.run_state("carryover-eligible", plan, applied, migrations, ok=False)
        applied.write_text("\n".join(names[:139]) + "\n")
        (migrations / "0141_video_watermark_users.sql").rename(migrations / "0141_other.sql")
        self.run_state("carryover-eligible", plan, applied, migrations, ok=False)

    def test_carryover_video_0153_requires_exact_expansion(self):
        migrations = self.root / "migrations"
        migrations.mkdir()
        names = [f"{i:04d}_fixture" for i in range(1, 151)]
        names += ["0151_callback_hint_revision", "0152_video_user_task_limits",
                  "0153_video_group_asset_projects"]
        for name in names:
            (migrations / f"{name}.sql").write_text("fixture")
        applied = self.root / "applied.txt"
        applied.write_text("\n".join(names[:150]) + "\n")
        plan = self.root / "plan.json"
        plan.write_text(json.dumps({"carryover": 2}))
        self.run_state("carryover-eligible", plan, applied, migrations)
        for policy in (0, 1, 3):
            plan.write_text(json.dumps({"carryover": policy}))
            self.run_state("carryover-eligible", plan, applied, migrations, ok=False)
        plan.write_text(json.dumps({"carryover": 2}))
        applied.write_text("\n".join(names[:149]) + "\n")
        self.run_state("carryover-eligible", plan, applied, migrations, ok=False)
        applied.write_text("\n".join(names[:150]) + "\n")
        (migrations / "0153_video_group_asset_projects.sql").rename(migrations / "0153_other.sql")
        self.run_state("carryover-eligible", plan, applied, migrations, ok=False)
        (migrations / "0153_other.sql").rename(migrations / "0153_video_group_asset_projects.sql")
        (migrations / "0154_extra.sql").write_text("fixture")
        self.run_state("carryover-eligible", plan, applied, migrations, ok=False)

    def test_carryover_business_rows_must_remain_identical(self):
        plan = self.root / "plan.json"
        plan.write_text(json.dumps({"carryover": 1}))
        before, after = self.root / "before.sha256", self.root / "after.sha256"
        before.write_text("a" * 64 + "\n")
        after.write_text("a" * 64 + "\n")
        self.run_state("business", plan, before, after)
        after.write_text("b" * 64 + "\n")
        self.run_state("business", plan, before, after, ok=False)
        plan.write_text(json.dumps({"carryover": 0}))
        after.write_text("a" * 64 + "\n")
        self.run_state("business", plan, before, after, ok=False)
        plan.write_text(json.dumps({"carryover": 2}))
        self.run_state("business", plan, before, after)
        plan.write_text(json.dumps({"carryover": 3}))
        self.run_state("business", plan, before, after)

    def test_app_only_carryover_requires_identical_migrations(self):
        migrations = self.root / "migrations"
        migrations.mkdir()
        for name in ("0154_before", "0155_current"):
            (migrations / f"{name}.sql").write_text("fixture")
        applied = self.root / "applied.txt"
        plan = self.root / "plan.json"
        plan.write_text(json.dumps({"carryover": 3}))
        applied.write_text("0154_before\n0155_current\n")
        self.run_state("carryover-eligible", plan, applied, migrations)
        applied.write_text("0154_before\n")
        self.run_state("carryover-eligible", plan, applied, migrations, ok=False)
        applied.write_text("0154_before\n0155_current\n9999_unknown\n")
        self.run_state("carryover-eligible", plan, applied, migrations, ok=False)
        applied.write_text("")
        self.run_state("carryover-eligible", plan, applied, migrations, ok=False)
        applied.write_text("0154_before\n0155_current\n")
        plan.write_text(json.dumps({"carryover": 0}))
        self.run_state("carryover-eligible", plan, applied, migrations, ok=False)

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

    def test_only_reviewed_core_media_and_scanner_mounts_are_allowed(self):
        env = {
            'core': {k: SECRET for k in ('LAIAI_DSN','LAIAI_AUTH_SECRET','LAIAI_DATA_KEYS','LAIAI_DATA_KEY_ACTIVE','LAIAI_REDIS_ADDR','LAIAI_REDIS_PASSWORD','LAIAI_VIDEO_HMAC_SECRET')},
            'caddy': {'LAIAI_DOMAIN': 'example.test'},
            'mysql': {k: SECRET for k in ('MYSQL_ROOT_PASSWORD','MYSQL_DATABASE','MYSQL_USER','MYSQL_PASSWORD')},
            'redis': {'REDIS_PASSWORD': SECRET},
        }
        old = {role: {'image': OLD_IMAGES[role], 'mounts': [],
                      'env_hashes': {k: hashlib.sha256(v.encode()).hexdigest() for k,v in env[role].items()}}
               for role in OLD_IMAGES}
        plan = self.root / 'plan.json'
        plan.write_text(json.dumps({'snapshot': old}))
        mounts = [
            {'type':'bind','source':'/var/lib/clamav','target':'/var/lib/clamav','read_only':True},
            {'type':'bind','source':'/var/lib/laiai/video-media','target':'/var/lib/laiai/video-media','read_only':False},
        ]
        config = {'name':'infra','services':{},'volumes':{}}
        for role in OLD_IMAGES:
            config['services'][role] = {'image': NEW_IMAGES.get(role,OLD_IMAGES[role]),
                                        'pull_policy':'never','environment':env[role],
                                        'volumes':mounts if role=='core' else []}
        self.run_state('check-compose', plan, 'new', value=config)
        for replacement in (
            {'type':'bind','source':'/other','target':'/var/lib/laiai/video-media','read_only':False},
            {'type':'bind','source':'/var/lib/clamav','target':'/var/lib/clamav','read_only':False},
            {'type':'bind','source':'/tmp/extra','target':'/extra','read_only':False},
        ):
            bad = copy.deepcopy(config)
            bad_mounts = bad['services']['core']['volumes']
            if replacement['target']=='/extra': bad_mounts.append(replacement)
            else: bad_mounts[next(i for i,m in enumerate(bad_mounts) if m['target']==replacement['target'])]=replacement
            self.run_state('check-compose', plan, 'new', value=bad, ok=False)
        current = copy.deepcopy(old)
        current['core']['image'] = NEW_IMAGES['core']
        current['caddy']['image'] = NEW_IMAGES['caddy']
        current['core']['mounts'] = [
            {'type':'bind','source':m['source'],'destination':m['target'],'rw':not m['read_only']} for m in mounts]
        actual = self.root / 'actual.json'
        images = self.root / 'images.json'
        actual.write_text(json.dumps(current))
        images.write_text(json.dumps(NEW_IMAGES))
        self.run_state('running', plan, actual, images)
        current['core']['mounts'][0]['rw']=True
        actual.write_text(json.dumps(current))
        self.run_state('running', plan, actual, images, ok=False)

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
        coordinator = self.tools / "infra/release.sh"
        media_dir = self.root / 'private-media'
        clam_dir = self.root / 'clamav-signatures'
        media_dir.mkdir(mode=0o700)
        os.chown(media_dir, 10001, -1)
        clam_dir.mkdir()
        (clam_dir / 'main.cvd').write_text('fixture')
        (clam_dir / 'daily.cvd').write_text('fixture')
        coordinator.write_text(coordinator.read_text().replace('/var/lib/laiai/video-media', str(media_dir)).replace('/var/lib/clamav', str(clam_dir)))
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
        proxy = {"handler": "reverse_proxy", "dynamic_upstreams": {"name": "core", "port": "8080"}}
        routes = [{"match": [{"path": [path]}], "handle": [{"handler": "subroute", "routes": [{"handle": [copy.deepcopy(proxy)]}]}]}
                  for path in ("/healthz", "/api/*", "/v1/*")]
        routes.append({"handle": [{"handler": "subroute", "routes": [{"handle": [{"handler": "file_server"}]}]}]})
        active_config = {"apps": {"http": {"servers": {"srv0": {"routes": [{"handle": [{"handler": "subroute", "routes": routes}]}]}}}}}
        self.set_control({"remote": TARGET_SHA, "target": TARGET_SHA, "old": OLD_SHA, "next": NEXT_SHA,
                          "live": str(self.live), "secret": SECRET, "images": OLD_IMAGES, "new_images": NEW_IMAGES,
                          "environments": environments, "mounts": mounts, "active_config": active_config,
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

    def call(self, command, ok=True, include_release=True, error=None, carryover=False, app_only=False):
        args = ["bash", str(self.tools / "infra/release.sh"), "--config", str(self.config), command, "--yes"]
        if carryover:
            args.append("--carryover-video-0153")
        if app_only:
            args.append("--carryover-app-only")
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

    def prepare(self, through="build", carryover=False, app_only=False):
        if app_only:
            self.change(app_only=True)
        self.call("preflight", carryover=carryover, app_only=app_only)
        self.release_id = next((self.state_dir / "releases").iterdir()).name
        if through in ("backup", "build"):
            self.call("backup")
        if through == "build":
            self.call("build")

    def run_dir(self):
        return self.state_dir / "releases" / self.release_id

    def assert_no_stop_or_up(self):
        self.assertFalse(any(v == "stop-core" or v.startswith("up-") for v in self.log()), self.log())

    def enable_0153_carryover(self):
        self.change(carryover_0153=True)
        (self.tools / "infra/release/carryover_0153.py").write_text(CARRYOVER_STUB)

    def test_schema_changing_release_refuses_before_backup_or_container_changes(self):
        self.call("preflight", ok=False, error="No-maintenance policy")
        self.assertNotIn("backup", self.log())
        self.assert_no_stop_or_up()

    def test_online_release_keeps_old_services_running(self):
        self.prepare(app_only=True)
        self.call("deploy")
        events = self.log()
        for event in ("backup", "build-core", "build-caddy", "up-online-core", "up-online-web", "open"):
            self.assertIn(event, events)
        self.assertLess(events.index("backup"), events.index("up-online-core"))
        self.assertLess(events.index("up-online-web"), events.index("open"))
        for forbidden in ("maintenance", "stop-core", "final-backup", "up-core", "up-caddy",
                          "migrate", "schema-doctor", "backfill-funds", "business-fingerprint", "data:preflight"):
            self.assertNotIn(forbidden, events)
        control = json.loads(self.control.read_text())
        self.assertEqual(control["images"]["core"], OLD_IMAGES["core"])
        self.assertIn("online-core-", json.dumps(control["active_config"]))
        self.assertTrue((self.run_dir() / "deployed.ok").exists())
        self.assertTrue((self.run_dir() / "open.ok").exists())
        self.call("verify")

    def test_candidate_start_failure_leaves_old_routes(self):
        self.prepare(app_only=True)
        original = json.loads(self.control.read_text())["active_config"]
        self.change(candidate_up_fails=True)
        self.call("deploy", ok=False)
        self.assertEqual(json.loads(self.control.read_text())["active_config"], original)
        self.assertNotIn("open", self.log())
        self.assertNotIn("maintenance", self.log())
        self.assertNotIn("stop-core", self.log())

    def test_public_candidate_failure_restores_previous_routes(self):
        self.prepare(app_only=True)
        original = json.loads(self.control.read_text())["active_config"]
        self.change(candidate_health_fails=True)
        self.call("deploy", ok=False, error="original public routes restored")
        self.assertEqual(json.loads(self.control.read_text())["active_config"], original)
        self.assertNotIn("maintenance", self.log())
        self.assertNotIn("stop-core", self.log())

    def test_online_rollback_only_reloads_routes(self):
        self.prepare(app_only=True)
        original = json.loads(self.control.read_text())["active_config"]
        self.call("deploy")
        self.call("rollback")
        self.assertEqual(json.loads(self.control.read_text())["active_config"], original)
        self.assertNotIn("stop-core", self.log())
        self.assertNotIn("maintenance", self.log())
        self.assertTrue((self.run_dir() / "rolled-back.ok").exists())

    def test_next_preflight_reads_routed_candidate_revision(self):
        self.prepare(app_only=True)
        self.call("deploy")
        self.change(remote=NEXT_SHA, target=NEXT_SHA)
        result = self.call("preflight", include_release=False, app_only=True)
        self.assertIn("Running version: " + TARGET_SHA, result.stdout)
        self.assertNotIn("stop-core", self.log())


class OnlineRouteTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="laiai-online-route-")
        self.root = Path(self.tmp.name)
        proxy = {"handler": "reverse_proxy", "dynamic_upstreams": {"name": "core", "port": "8080"}}
        routes = [{"match": [{"path": [path]}], "handle": [{"handler": "subroute", "routes": [{"handle": [copy.deepcopy(proxy)]}]}]}
                  for path in ("/healthz", "/api/*", "/v1/*")]
        routes.append({"handle": [{"handler": "subroute", "routes": [{"handle": [{"handler": "file_server"}]}]}]})
        self.original = {"apps": {"http": {"servers": {"srv0": {"routes": [{"handle": [{"handler": "subroute", "routes": routes}]}]}}}}}
        (self.root / "original.json").write_text(json.dumps(self.original))

    def tearDown(self):
        self.tmp.cleanup()

    def run_online(self, *args, input_text=None, ok=True):
        result = subprocess.run([sys.executable, str(RELEASE_DIR / "online.py"), *map(str, args)],
                                input=input_text, text=True, capture_output=True)
        self.assertEqual(result.returncode == 0, ok, result.stderr)
        return result

    def test_two_successive_live_switches_replace_only_expected_routes(self):
        first = self.root / "first.json"
        second = self.root / "second.json"
        first_id = "20261116T140000Z-aaaaaaaaaaaa"
        second_id = "20261116T150000Z-bbbbbbbbbbbb"
        self.run_online("route", self.root / "original.json", first, first_id)
        self.run_online("route", first, second, second_id)
        first_config, second_config = first.read_text(), second.read_text()
        self.assertEqual(json.loads((self.root / "original.json").read_text()), self.original)
        self.assertIn("online-core-20261116-140000-aaaaaaaaaaaa", first_config)
        self.assertNotIn("online-core-20261116-140000-aaaaaaaaaaaa", second_config)
        self.assertIn("online-web-20261116-150000-bbbbbbbbbbbb", second_config)
        self.assertEqual(self.run_online("active-core", input_text=second_config).stdout.strip(),
                         "online-core-20261116-150000-bbbbbbbbbbbb")

    def test_unknown_edge_layout_refuses_route_change(self):
        broken = copy.deepcopy(self.original)
        broken["apps"]["http"]["servers"]["srv0"]["routes"][0]["handle"][0]["routes"].pop(0)
        (self.root / "broken.json").write_text(json.dumps(broken))
        output = self.root / "candidate.json"
        self.run_online("route", self.root / "broken.json", output,
                        "20261116T140000Z-aaaaaaaaaaaa", ok=False)
        self.assertFalse(output.exists())

    def test_referenced_lists_only_online_candidate_dials(self):
        config = {"apps": {"http": {"servers": {"srv0": {"routes": [
            {"handle": [{"handler": "reverse_proxy", "upstreams": [{"dial": "online-core-20261116-140000-aaaaaaaaaaaa:8080"}]}]},
            {"handle": [{"handler": "reverse_proxy", "upstreams": [{"dial": "online-web-20261116-150000-bbbbbbbbbbbb:8081"}]}]},
            {"handle": [{"handler": "reverse_proxy", "upstreams": [{"dial": "core:8080"}]}]},
            {"handle": [{"handler": "reverse_proxy", "dynamic_upstreams": {"name": "core", "port": "8080"}}]},
            {"handle": [{"handler": "reverse_proxy", "upstreams": [{"dial": "online-core-20261116-140000-aaaaaaaaaaaa:8080"}, {"dial": "online-core-20261116-120000-999999999999:8080"}]}]},
            {"handle": [{"handler": "file_server"}]},
        ]}}}}}
        (self.root / "live.json").write_text(json.dumps(config))
        out = self.run_online("referenced", self.root / "live.json").stdout
        self.assertEqual(out.splitlines(), [
            "online-core-20261116-120000-999999999999",
            "online-core-20261116-140000-aaaaaaaaaaaa",
            "online-web-20261116-150000-bbbbbbbbbbbb",
        ])

    def test_referenced_merges_live_and_rollback_records(self):
        live = {"handle": [{"handler": "reverse_proxy", "upstreams": [{"dial": "online-core-20261116-150000-bbbbbbbbbbbb:8080"}]}]}
        rollback = {"handle": [{"handler": "reverse_proxy", "upstreams": [{"dial": "online-core-20261116-140000-aaaaaaaaaaaa:8080"}, {"dial": "online-web-20261116-140000-aaaaaaaaaaaa:8081"}]}]}
        (self.root / "live.json").write_text(json.dumps(live))
        (self.root / "rollback.json").write_text(json.dumps(rollback))
        out = self.run_online("referenced", self.root / "live.json", self.root / "rollback.json").stdout
        self.assertEqual(out.splitlines(), [
            "online-core-20261116-140000-aaaaaaaaaaaa",
            "online-core-20261116-150000-bbbbbbbbbbbb",
            "online-web-20261116-140000-aaaaaaaaaaaa",
        ])
        empty = self.run_online("referenced", self.root / "original.json").stdout
        self.assertEqual(empty, "")

    def test_frontend_overlay_preserves_caddy_executable_capability(self):
        run = self.root / "20261116T140000Z-aaaaaaaaaaaa"
        (run / "config").mkdir(parents=True)
        (run / "plan.json").write_text('{"carryover": 3}')
        (run / "images.json").write_text(json.dumps(NEW_IMAGES))
        self.run_online("overlay", run / "plan.json", run / "images.json",
                        RELEASE_DIR.parent.parent, run / "online-compose.yml")
        overlay = (run / "online-compose.yml").read_text()
        self.assertIn('security_opt: ["no-new-privileges:true"]', overlay)
        self.assertIn('read_only: true', overlay)
        self.assertNotIn('cap_drop:', overlay)


if __name__ == "__main__":
    unittest.main(verbosity=2)
