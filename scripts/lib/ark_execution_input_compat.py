"""One host controller, two compiler adapters, eight genuine process handoffs.

No supplied DSN, credential, N alias, arbitrary old revision or production
cleanup. Docker compilation has source/output/cache mounts, never docker.sock.
The test processes run on the host; Docker is used only by this parent to own
its digest-pinned private MySQL and optional fixed-image compiler.
"""
from __future__ import annotations
import argparse,base64,datetime,hashlib,io,json,os,pathlib,re,secrets,signal,struct,subprocess,sys,tarfile,time
from ark_execution_input_verify import verify as verify_matrix
from ark_mutation_ci import OwnedProcess  # Existing audited host-session cleanup; no scenario reuse.

ROOT=pathlib.Path(__file__).resolve().parents[2]
OLD='9a6fd9426e33b2e1c269b6cb77a170e8890073f2'
GO='go version go1.26.10 linux/amd64'
TOOL_IMAGE='sha256:e5b54d386ea5dad8908135b5b2d65e1604f990e9628526125e9a01a36043c6ad'
MYSQL='docker.io/library/mysql@sha256:c36050afdca850f23cef85703f84c7531a5ae155a11b5ee1c60acb09937c4084'
LABEL='ai.laiai.execution-input-handoff.run'
DOCKER=('docker','--host','unix:///var/run/docker.sock')
HARNESS=('core/internal/video/execution_input_handoff_test.go','core/internal/video/execution_input_handoff_control_test.go')
CONTROLS=('scripts/ark-execution-input-compat.sh','scripts/lib/ark_execution_input_compat.py','scripts/lib/ark_execution_input_matrix.py','scripts/lib/ark_execution_input_verify.py','scripts/lib/ark_mutation_ci.py')
CASES=('N01','N02','N03-create','N03-action','N04-schema-new','N04-schema-old','N04-runtime-new','N04-runtime-old')
def need(ok,code):
    if not ok:raise RuntimeError(code)
def sha(data):return hashlib.sha256(data).hexdigest()
def write(path,value):path.write_text(json.dumps(value,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
def load(path):return json.loads(path.read_text(encoding='utf-8'))

def elf_linkage(raw):
    """Read x86-64 ELF facts without trusting file/ldd's text or exit status."""
    need(len(raw)>=64 and raw[:7]==b'\x7fELF\x02\x01\x01','elf_header')
    kind,machine,version=struct.unpack_from('<HHI',raw,16)
    need(kind in (2,3) and machine==62 and version==1,'elf_target')
    phoff=struct.unpack_from('<Q',raw,32)[0]
    ehsize,phsize,phnum=struct.unpack_from('<HHH',raw,52)
    need(ehsize==64 and phsize==56 and 0<phnum<65535 and phoff>=64 and phoff+phsize*phnum<=len(raw),'elf_program_headers')
    interpreters=[];needed=0;dynamic=0;loads=0
    for index in range(phnum):
        ptype,_,offset,_,_,filesz,_,_=struct.unpack_from('<IIQQQQQQ',raw,phoff+index*phsize)
        need(offset<=len(raw) and filesz<=len(raw)-offset,'elf_segment_bounds')
        if ptype==1:loads+=1
        if ptype==3:
            need(filesz>1 and raw[offset+filesz-1]==0,'elf_interpreter')
            interpreters.append(raw[offset:offset+filesz-1].decode('ascii',errors='strict'))
        if ptype==2:
            dynamic+=1
            need(filesz>=16 and filesz%16==0,'elf_dynamic_bounds')
            terminated=False
            for position in range(offset,offset+filesz,16):
                tag,_=struct.unpack_from('<qQ',raw,position)
                if tag==0:terminated=True;break
                if tag==1:needed+=1
            need(terminated,'elf_dynamic_termination')
    need(loads>0 and len(interpreters)<=1 and dynamic<=1,'elf_segment_shape')
    return {'elf_class':64,'machine':'x86_64','interpreter':interpreters[0] if interpreters else None,'needed_entries':needed,'dynamic_segments':dynamic}

def build_link_flags(compiler,source=None):
    flags=['-linkmode=external','-extldflags=-static'] if compiler=='docker' else []
    if source is not None:flags+=['-X','github.com/laiai/laiai-next/core/internal/video.executionHandoffCompiledSource='+source]
    return ['-ldflags',' '.join(flags)] if flags else []
def clean_environment(inherited):
    need(not any(k.startswith(('LAIAI_','ARK_TEST_','B1B_')) for k in inherited),'inherited_business_environment')
    env={key:inherited[key] for key in ('PATH','HOME','TMPDIR','GOPROXY','GOMODCACHE','GOCACHE') if key in inherited}
    env.update(GOENV='off',GOTOOLCHAIN='local',GOMAXPROCS='3',GOMEMLIMIT='4GiB',CGO_ENABLED='1',GOOS='linux',GOARCH='amd64',PYTHONDONTWRITEBYTECODE='1')
    return env
def checked_archive(raw):
    files={};modes={}
    with tarfile.open(fileobj=io.BytesIO(raw),mode='r:') as archive:
        for member in archive:
            if member.isdir():continue
            path=pathlib.PurePosixPath(member.name)
            need(member.isfile() and not path.is_absolute() and '..' not in path.parts and member.name not in files,'archive_member')
            files[member.name]=archive.extractfile(member).read();modes[member.name]=member.mode
    return files,modes
def redact_export(raw,secret_values):
    for value in secret_values:
        if value:raw=raw.replace(value.encode(),b'[synthetic-secret-redacted]')
    return raw

class Runner:
    def __init__(self,args):
        self.args=args;self.env=clean_environment(os.environ);self.run_id=secrets.token_hex(8)
        self.root=pathlib.Path(args.output).resolve()
        need(not self.root.exists() and not ROOT.is_relative_to(self.root),'new_output_required')
        self.root.mkdir(parents=True,mode=0o700)
        self.deadline=time.monotonic()+args.timeout_seconds;self.compiler_containers=[];self.db=None
        self.secrets=[];self.report={'status':'running','run_id':self.run_id,'old_source':OLD,'official_calls':0,'compiler':args.compiler,'started_at':datetime.datetime.now(datetime.timezone.utc).isoformat()}
        self.evidence=self.root/'evidence';self.evidence.mkdir()
    def command(self,args,log=None,env=None,cwd=None,timeout=120,input_bytes=None,allowed_returncodes=(0,),exit_record=None):
        remaining=self.deadline-time.monotonic();need(remaining>0,'total_deadline')
        # Each command owns a new POSIX session. Until communicate has reaped
        # the direct child its PID cannot be reused; kill only that session.
        process=subprocess.Popen(args,cwd=cwd,env=self.env if env is None else env,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,start_new_session=True)
        try:
            out,err=process.communicate(input=input_bytes,timeout=min(timeout,remaining))
        except BaseException:
            try:os.killpg(process.pid,signal.SIGTERM)
            except ProcessLookupError:pass
            try:out,err=process.communicate(timeout=5)
            except subprocess.TimeoutExpired:
                try:os.killpg(process.pid,signal.SIGKILL)
                except ProcessLookupError:pass
                out,err=process.communicate(timeout=5)
            if log:log.write_bytes(out+err)
            raise RuntimeError('command_interrupted') from None
        if log:log.write_bytes(out+err)
        if exit_record:write(exit_record,{'exit_code':process.returncode})
        need(process.returncode in allowed_returncodes,'command_failed_'+(log.stem if log else 'private_tool'))
        return out
    def source(self):
        new=self.command(['git','-C',str(ROOT),'rev-parse','HEAD']).decode().strip()
        need(re.fullmatch('[0-9a-f]{40}',new) and new!=OLD,'real_new_commit')
        need(not self.command(['git','-C',str(ROOT),'status','--porcelain','--untracked-files=no']).strip(),'dirty_tracked_source')
        self.command(['git','-C',str(ROOT),'merge-base','--is-ancestor',OLD,new])
        self.report['new_source']=new;self.sources={};old_migrations={}
        # N is the genuine archive. It supplies the exactly identical tests
        # added to N-1; nothing in either production archive is overwritten.
        archives={}
        for role,commit in (('old',OLD),('new',new)):
            raw=self.command(['git','-C',str(ROOT),'archive','--format=tar',commit],timeout=60)
            (self.root/(role+'-original.tar')).write_bytes(raw);archives[role]=checked_archive(raw)
        for name in CONTROLS:
            need(name in archives['new'][0] and (ROOT/name).read_bytes()==archives['new'][0][name],'uncommitted_control')
        common={name:archives['new'][0][name] for name in HARNESS}
        for name,raw in common.items():need((ROOT/name).read_bytes()==raw,'uncommitted_common_harness')
        for role,commit in (('old',OLD),('new',new)):
            files,modes=archives[role];original={name:sha(raw) for name,raw in files.items()}
            if role=='old':old_migrations={name:raw for name,raw in files.items() if name.startswith('migrations/')}
            else:need(all(files.get(name)==raw for name,raw in old_migrations.items()),'historical_migration_changed')
            for name,raw in common.items():
                need(name not in files or files[name]==raw,'old_harness_collision');files[name]=raw
            directory=self.root/(role+'-source');directory.mkdir()
            for name,raw in files.items():
                target=directory/name;target.parent.mkdir(parents=True,exist_ok=True);target.write_bytes(raw);target.chmod(modes.get(name,0o644))
            manifest={name:sha(raw) for name,raw in files.items()};write(self.root/(role+'-source-manifest.json'),manifest)
            count=sum(bool(re.fullmatch(r'migrations/\d{4}_[^/]+\.sql',name)) for name in files)
            self.sources[role]={'source':commit,'path':str(directory),'original_sha256':original,'files':manifest,'migration_count':count,'common_harness_sha256':{name:sha(raw) for name,raw in common.items()}}
        write(self.root/'sources.json',self.sources)
    def compiler(self,args,role,log):
        source=pathlib.Path(self.sources[role]['path'])
        if self.args.compiler=='host':return self.command(args,self.evidence/log,cwd=source/'core',timeout=600)
        # Only fixed compiler commands are generated by this process. No Docker
        # socket, source write access or arbitrary shell is provided to it.
        cidfile=self.root/('compile-'+log+'.cid');name='laiai-b1b-compile-'+self.run_id+'-'+str(len(list(self.root.glob('compile-*.cid'))))
        converted=[arg.replace(str(self.root),'/work') for arg in args]
        self.compiler_containers.append({'name':name,'image':TOOL_IMAGE,'cidfile':cidfile})
        docker=[*DOCKER,'run','--name',name,'--cidfile',str(cidfile),'--label',LABEL+'='+self.run_id,'--cpus=3','--memory=6g','--memory-swap=8g','-e','GOENV=off','-e','GOTOOLCHAIN=local','-e','CGO_ENABLED=1','-e','GOMAXPROCS=3','-e','GOMEMLIMIT=4GiB','-v',str(self.root)+':/work','-v',str(self.root/'old-source')+':/work/old-source:ro','-v',str(self.root/'new-source')+':/work/new-source:ro','-v','laiai-ci-gomod:/go/pkg/mod','-v','laiai-ci-gocache:/root/.cache/go-build','-w','/work/'+role+'-source/core','--entrypoint',converted[0],TOOL_IMAGE,*converted[1:]]
        return self.command(docker,self.evidence/log,timeout=600)
    def build(self):
        if self.args.compiler=='host':need(self.command(['go','version']).decode().strip()==GO,'go_version')
        else:
            need(self.command([*DOCKER,'image','inspect',TOOL_IMAGE,'--format','{{.Id}}']).decode().strip()==TOOL_IMAGE,'tool_image')
            for volume in ('laiai-ci-gomod','laiai-ci-gocache'):
                need(self.command([*DOCKER,'volume','inspect',volume,'--format','{{.Name}}']).decode().strip()==volume,'existing_compiler_cache')
        self.builds={};self.tools={};bin_dir=self.root/'bin';bin_dir.mkdir()
        for role in ('old','new'):
            need(self.compiler(['go','version'],role,role+'-go-version.log').decode().strip()==GO,'compiler_go_version')
            src=self.sources[role]['source'];binary=bin_dir/(role+'-video.test')
            self.compiler(['go','test','-race','-c','-o',str(binary),*build_link_flags(self.args.compiler,src),'./internal/video'],role,role+'-build.log')
            self.builds[role]={'path':str(binary),'source':src,'sha256':sha(binary.read_bytes())};self.tools[role]={}
            for tool in ('migrate','schema-doctor'):
                target=bin_dir/(role+'-'+tool);self.compiler(['go','build',*build_link_flags(self.args.compiler),'-o',str(target),'./cmd/'+tool],role,role+'-'+tool+'-build.log');self.tools[role][tool]=str(target)
            for tool,path in {'video':str(binary),**self.tools[role]}.items():
                self.compiler(['go','version','-m',path],role,role+'-'+tool+'-build-info.log')
                if self.args.compiler=='docker':self.check_static_host_binary(path,role,tool)
            if self.args.compiler=='host':
                self.command(['ldd',str(binary)],self.evidence/(role+'-host-ldd.log'))
                listing=self.command([str(binary),'-test.list=^TestMySQLExecutionInputBinaryHandoffProcess$'],self.evidence/(role+'-host-test-list.log'))
                need(listing.decode().splitlines()==['TestMySQLExecutionInputBinaryHandoffProcess'],'host_elf_entrypoint')
        need(self.builds['old']['sha256']!=self.builds['new']['sha256'],'identical_binary')
        write(self.evidence/'build-manifest.json',self.builds)
        write(self.evidence/'build-artifacts.json',{p.name:{'bytes':p.stat().st_size,'sha256':sha(p.read_bytes())} for p in bin_dir.iterdir()})
        self.source_integrity()
    def check_static_host_binary(self,path,role,tool):
        prefix=role+'-'+tool+'-host'
        raw=pathlib.Path(path).read_bytes();facts=elf_linkage(raw)
        write(self.evidence/(prefix+'-elf.json'),dict(facts,sha256=sha(raw),bytes=len(raw),link_policy='docker_external_static'))
        self.command(['file','--brief',path],self.evidence/(prefix+'-file.log'))
        need(facts['interpreter'] is None and facts['needed_entries']==0,'host_elf_dynamic_dependency')
        # Static ELF is independently established above. ldd commonly returns
        # 1 for it; that exception never applies to a dynamically linked file.
        self.command(['ldd',path],self.evidence/(prefix+'-ldd.log'),allowed_returncodes=(0,1),exit_record=self.evidence/(prefix+'-ldd-exit.json'))
        if tool=='video':
            listing=self.command([path,'-test.list=^TestMySQLExecutionInputBinaryHandoffProcess$'],self.evidence/(prefix+'-test-list.log'))
            need(listing.decode().splitlines()==['TestMySQLExecutionInputBinaryHandoffProcess'],'host_elf_entrypoint')
        else:
            self.command([path,'-h'],self.evidence/(prefix+'-help.log'))
    def owned(self,entry):
        path=entry['cidfile']
        if not path.exists():return None
        cid=path.read_text().strip();need(re.fullmatch('[0-9a-f]{64}',cid),'owned_cid')
        row=json.loads(subprocess.check_output([*DOCKER,'inspect',cid],env=self.env,timeout=15))[0]
        need(row['Id']==cid and row['Name']=='/'+entry['name'] and row['Image']==entry['image'] and row['Config']['Labels'].get(LABEL)==self.run_id,'owned_container')
        return row
    def sql(self,statement):
        return self.command([*DOCKER,'exec','-i','-e','MYSQL_PWD',self.db['name'],'mysql','-uroot','--batch','--skip-column-names'],env=dict(self.env,MYSQL_PWD=self.password),input_bytes=statement.encode(),timeout=15)
    def database(self):
        try:image=self.command([*DOCKER,'image','inspect',MYSQL,'--format','{{.Id}}']).decode().strip()
        except RuntimeError:
            self.command([*DOCKER,'pull',MYSQL],self.evidence/'mysql-image-pull.log',timeout=240)
            image=self.command([*DOCKER,'image','inspect',MYSQL,'--format','{{.Id}}']).decode().strip()
        self.password=secrets.token_hex(24);self.secrets.append(self.password)
        self.db={'name':'laiai-b1b-mysql-'+self.run_id,'image':image,'cidfile':self.root/'database.cid'}
        self.command([*DOCKER,'run','-d','--name',self.db['name'],'--cidfile',str(self.db['cidfile']),'--label',LABEL+'='+self.run_id,'--cpus=1','--memory=1536m','--memory-swap=2g','-e','MYSQL_ROOT_PASSWORD','-e','MYSQL_ROOT_HOST=%','-p','127.0.0.1::3306',MYSQL,'--innodb-buffer-pool-size=268435456','--max-connections=80','--skip-log-bin'],env=dict(self.env,MYSQL_ROOT_PASSWORD=self.password),log=self.evidence/'mysql-start.log',timeout=120)
        row=self.owned(self.db);ports=row['NetworkSettings']['Ports']['3306/tcp'];need(len(ports)==1 and ports[0]['HostIp']=='127.0.0.1','mysql_loopback')
        self.port=int(ports[0]['HostPort']);need(0<self.port<65536,'mysql_port')
        write(self.evidence/'mysql-identity.json',{'id':row['Id'],'name':row['Name'],'image':row['Image'],'requested':MYSQL,'owner':self.run_id,'port':self.port})
        ready=False
        for _ in range(120):
            try:self.sql('SELECT 1;');ready=True;break
            except RuntimeError:time.sleep(1)
        need(ready,'mysql_start_timeout')
        for case in CASES:
            name='ark_test_exec_'+self.run_id+'_'+sha(case.encode())[:12]
            self.sql('CREATE DATABASE `'+name+'` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;')
            env=dict(self.env,LAIAI_DSN='root:'+self.password+'@tcp(127.0.0.1:'+str(self.port)+')/'+name+'?parseTime=true&loc=UTC')
            for role in ('old','new'):
                migrations=str(pathlib.Path(self.sources[role]['path'])/'migrations')
                for repeat in (1,2):self.command([self.tools[role]['migrate'],'-dir',migrations],self.evidence/(case+'-'+role+'-migration-'+str(repeat)+'.log'),env=env,timeout=120)
                output=self.command([self.tools[role]['schema-doctor'],'-dir',migrations],self.evidence/(case+'-'+role+'-doctor.log'),env=env)
                need(('OK: schema at head ('+str(self.sources[role]['migration_count'])+' applied,').encode() in output,'doctor_head')
    def matrix(self):
        secret=secrets.token_hex(32);hmac=secrets.token_hex(32);data_key='b1b:'+base64.b64encode(secrets.token_bytes(32)).decode();self.secrets.extend((secret,hmac,data_key,data_key.split(':')[1]))
        env=dict(self.env,LAIAI_AUTH_SECRET=secret,LAIAI_VIDEO_HMAC_SECRET=hmac,LAIAI_DATA_KEYS=data_key,LAIAI_DATA_KEY_ACTIVE='b1b',LAIAI_DB_MAX_OPEN_CONNS='8',LAIAI_DB_MAX_IDLE_CONNS='4',B1B_DSN_TEMPLATE='root:'+self.password+'@tcp(127.0.0.1:'+str(self.port)+')/{schema}?parseTime=true&loc=UTC')
        process=OwnedProcess([sys.executable,str(ROOT/'scripts/lib/ark_execution_input_matrix.py'),'--run-id',self.run_id,'--build-manifest',str(self.evidence/'build-manifest.json'),'--evidence',str(self.evidence/'matrix')],env,self.evidence/'matrix-controller.log')
        try:need(process.proc.wait(timeout=max(1,self.deadline-time.monotonic()))==0,'matrix_exit')
        finally:process.stop()  # Also checks descendants if the leader has exited.
        write(self.evidence/'matrix-verified.json',verify_matrix(self.evidence))
    def source_integrity(self):
        evidence={}
        for role,source in getattr(self,'sources',{}).items():
            directory=pathlib.Path(source['path']);need(not any(p.is_symlink() for p in directory.rglob('*')),'runtime_source_symlink');actual={p.relative_to(directory).as_posix():sha(p.read_bytes()) for p in directory.rglob('*') if p.is_file() and not p.is_symlink()}
            evidence[role]={'expected':source['files'],'actual':actual,'unchanged':actual==source['files']}
        write(self.evidence/'source-integrity.json',evidence)
        need(len(evidence)==2 and all(x['unchanged'] for x in evidence.values()),'source_changed_after_archive')
    def finish(self,passed):
        stopped=True;healthy=True
        entries=[('compiler-'+str(i),entry) for i,entry in enumerate(self.compiler_containers)]+[('database',self.db)]
        for label,entry in entries:
            if entry is None:continue
            try:
                row=self.owned(entry)
                if row and row['State']['Running']:
                    subprocess.run([*DOCKER,'stop','--time=30',row['Id']],env=self.env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,check=True,timeout=45)
                    row=self.owned(entry)
                if row is None:stopped=False;healthy=False
                if row:
                    write(self.evidence/(label+'-final.json'),{'id':row['Id'],'name':row['Name'],'image':row['Image'],'owner':self.run_id,'state':{k:row['State'][k] for k in ('Status','Running','ExitCode','OOMKilled','StartedAt','FinishedAt')},'mounts':row['Mounts']})
                    stopped=stopped and not row['State']['Running']
                    healthy=healthy and not row['State']['OOMKilled'] and row['State']['ExitCode']==0
            except Exception:stopped=False
        try:self.source_integrity()
        except Exception:passed=False
        # Capture extracted source trees too: if a tool changed or added a file,
        # keep the actual runtime bytes rather than only the pristine archive.
        for role in ('old','new'):
            directory=self.root/(role+'-source')
            if directory.exists():
                with tarfile.open(self.root/(role+'-runtime.tar'),'w') as archive:archive.add(directory,arcname=role+'-source')
        self.report.update(status='passed' if passed and stopped and healthy else 'failed',containers_stopped=stopped,containers_healthy=healthy,finished_at=datetime.datetime.now(datetime.timezone.utc).isoformat(),deletions=[])
        write(self.root/'report.json',self.report)
        self.export()
        return self.report['status']=='passed'
    def export(self):
        # No Env/DSN/secrets files. Preserve original archives and actual ELF;
        # safe text copies redact process-local synthetic values if a tool ever
        # reflected one. Raw originals remain in the private run directory.
        export=self.root/'export';export.mkdir();manifest={}
        for path in sorted(self.root.rglob('*')):
            if not path.is_file() or export in path.parents or any(p in ('old-source','new-source') for p in path.relative_to(self.root).parts):continue
            rel=path.relative_to(self.root);raw=path.read_bytes()
            need(not path.is_symlink(),'export_symlink')
            binary=path.suffix=='.tar' or 'bin' in rel.parts
            safe=raw if binary else redact_export(raw,self.secrets)
            target=export/rel;target.parent.mkdir(parents=True,exist_ok=True);target.write_bytes(safe)
            manifest[rel.as_posix()]={'original_sha256':sha(raw),'export_sha256':sha(safe),'bytes':len(safe),'redacted':safe!=raw}
        write(export/'evidence-manifest.json',manifest)

def main():
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--output',required=True);parser.add_argument('--compiler',choices=('host','docker'),default='host');parser.add_argument('--timeout-seconds',type=int,default=2400);args=parser.parse_args()
    need(sys.platform=='linux' and 1200<=args.timeout_seconds<=3600,'linux_or_budget_required')
    runner=None;passed=False
    def interrupted(*_):raise RuntimeError('host_signal')
    signal.signal(signal.SIGTERM,interrupted);signal.signal(signal.SIGINT,interrupted)
    try:
        runner=Runner(args)
        for phase in ('source','build','database','matrix'):
            runner.report['phase']=phase;write(runner.root/'progress.json',runner.report)
            print('B1B_HOST_STAGE '+phase,flush=True);getattr(runner,phase)()
        passed=True
    except Exception as exc:
        if runner is not None:
            code=str(exc);runner.report['safe_error']=code if re.fullmatch('[a-zA-Z0-9_]+',code) else 'host_failure'
        print('Execution input handoff failed; preserve private evidence.',file=sys.stderr)
    finally:
        signal.signal(signal.SIGTERM,signal.SIG_IGN);signal.signal(signal.SIGINT,signal.SIG_IGN)
        if runner is not None:
            try:passed=runner.finish(passed)
            except Exception:passed=False;print('Evidence finalization failed; retain the entire private run directory.',file=sys.stderr)
    raise SystemExit(0 if passed else 1)
if __name__=='__main__':main()
