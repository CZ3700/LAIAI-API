"""Independent raw event/command/exit verifier, not only a passed-marker check."""
import collections,datetime,hashlib,json,pathlib,re,sys
OLD='9a6fd9426e33b2e1c269b6cb77a170e8890073f2'
TEST='TestMySQLExecutionInputBinaryHandoffProcess'
INSTANCES={'N01':['writer-old','sender-new','recovery-new'],'N02':['writer-old','first-new'],'N03-create':['writer-new','sender-old'],'N03-action':['writer-new','first-old']}
for kind in ('schema','runtime'):
    for first in ('new','old'):INSTANCES['N04-'+kind+'-'+first]=['writer-old','first-'+first,'purger-new','second-'+('old' if first=='new' else 'new')]
def need(ok,code):
    if not ok:raise ValueError(code)
def strict_json(raw):
    def pairs(items):
        out={}
        for key,value in items:
            need(key not in out,'duplicate_json_key');out[key]=value
        return out
    return json.loads(raw,object_pairs_hook=pairs,parse_constant=lambda _: need(False,'invalid_json_constant'))
def load(path):return strict_json(path.read_text(encoding='utf-8'))
PRICE={'retail_usd_per_1m':'8','supplier_cny_per_1m':'46','fx_usd_per_cny':'0.14','hold_usd':'0.86400000','supplier_reserved_cny':'4.96800000','effective_ratio':'1','rule_version':'handoff-v1','fx_version':'handoff-v1'}
FINANCIAL_KEYS={'accounts','holds','journals','ledger_movements','ledger_operations','ledger_events','billing_entries','quota_reservations','quota_buckets','key_usage'}
def timestamp(value):return datetime.datetime.fromisoformat(value.replace('Z','+00:00'))
def finance_equal(a,b):
    for key in ('financial_sha256','financial_fingerprints','finance','actual_amounts'):
        need(a[key]==b[key],'financial_replay_'+key)
def fixed_identity(a,b,execution=True):
    for key in ('org_id','key_id','task_id','billing_snapshot_sha256','binding_sha256','price'):
        need(a[key]==b[key],'fixed_identity_'+key)
    if execution:
        for key in ('attempt_sha256','receipt_sha256'):
            need(a[key]==b[key],'fixed_evidence_'+key)
        need(a['input']['identity_sha256']==b['input']['identity_sha256'],'fixed_input_identity')
def check_finance(proof,settled):
    need(proof['price']==PRICE,'literal_original_price')
    fingerprints=proof['financial_fingerprints']
    need(set(fingerprints)==FINANCIAL_KEYS|{'financial_all'},'financial_fingerprint_coverage')
    need(all(isinstance(v,str) and re.fullmatch('[0-9a-f]{64}',v) for v in fingerprints.values()),'financial_hash_shape')
    digest=hashlib.sha256(json.dumps({k:fingerprints[k] for k in FINANCIAL_KEYS},sort_keys=True,separators=(',',':')).encode()).hexdigest()
    need(digest==fingerprints['financial_all']==proof['financial_sha256'],'financial_fingerprint_digest')
    expected={'credit_usd':'100.00000000','available_usd':'99.99920000' if settled else '99.13600000','frozen_usd':'0.00000000' if settled else '0.86400000','hold_usd':'0.86400000','captured_usd':'0.00080000' if settled else '0.00000000','released_usd':'0.86320000' if settled else '0.00000000','hold_state':'captured' if settled else 'active','capture_journal_count':int(settled),'billing_entry_count':int(settled),'ledger_drift_count':0}
    for key,value in expected.items():need(proof['finance'][key]==value,'literal_finance_'+key)
    need(proof['actual_amounts']=={'supplier_cny':'0.00460000' if settled else '0.00000000','key_used_usd':'0.00080000' if settled else '0.00000000','provider_reserved_cny':'0.00000000' if settled else '4.96800000','provider_consumed_cny':'0.00460000' if settled else '0.00000000'},'literal_supplier_quota_key')
    if settled:need(proof['task_status']=='succeeded' and proof['billing_state']=='settled','literal_settled_status')
def event_proofs(events):
    for event in events:
        name,data=event['event'],event['data']
        if name in ('seeded','inspected','receipt_committed','action_pending'):yield data
        elif name=='cycle_completed':yield data['proof']
        elif name in ('purged','unknown_injected'):
            yield data['before'];yield data['after']
def durable_evidence_chain(events):
    # The writer can switch from the create operation to a cancel operation.
    # Bind each immutable capsule by operation ID, not by only the task or the
    # last pair of consumer snapshots. A schema fault changes metadata identity
    # explicitly; it never changes the registered attempt or durable receipt.
    facts={};inputs={}
    for event in events:
        for proof in event_proofs([event]):
            operation=proof['operation_id']
            for count_key,hash_key in (('attempt_count','attempt_sha256'),('receipt_count','receipt_sha256')):
                key=(operation,hash_key);count=proof[count_key]
                if key in facts:need(facts[key]==(count,proof[hash_key]),'durable_chain_'+hash_key)
                elif count>0:facts[key]=(count,proof[hash_key])
            current=proof['input']
            if not current['payload_ref']:continue
            identity=(current['payload_ref'],current['identity_sha256'])
            fault_after=event['event']=='unknown_injected' and event['data']['kind']=='schema' and proof is event['data']['after']
            if fault_after:
                before=event['data']['before'];need(before['operation_id']==operation,'schema_fault_operation')
                need(inputs.get(operation)==(before['input']['payload_ref'],before['input']['identity_sha256']),'schema_fault_prior_identity')
                need(identity[0]==before['input']['payload_ref'] and identity[1]!=before['input']['identity_sha256'],'schema_fault_identity')
                inputs[operation]=identity
            elif operation in inputs:need(inputs[operation]==identity,'durable_chain_input_identity')
            else:inputs[operation]=identity

def verify_case_proofs(case,result,events):
    def matching(name):return [e for e in events if e['event']==name]
    durable_evidence_chain(events)
    seeds=matching('seeded');need(len(seeds)==1,'raw_seed_count');seed=seeds[0]['data']
    need(result['seed']==seed,'result_seed_not_raw')
    final_instance={'N01':'recovery-new','N03-create':'sender-old','N02':'first-new','N03-action':'first-old'}.get(case)
    if final_instance is None:final_instance='second-'+('old' if case.endswith('-new') else 'new')
    cycles=[e['data'] for e in matching('cycle_completed') if e['instance']==final_instance]
    need(len(cycles)>=2 and all(c['error_class']=='none' and c['barrier_reached'] is False for c in cycles),'raw_recovery_cycles')
    terminal=case in ('N01','N03-create')
    if terminal:
        settled_cycles=[c['proof'] for c in cycles if c['proof']['billing_state']=='settled']
        need(len(settled_cycles)>=2,'settled_and_replay');final=settled_cycles[0]
    else:final=cycles[-1]['proof']
    need(result['final']==final,'result_final_not_raw')
    durable=matching('receipt_committed')
    expected_operation=durable[0]['data']['operation_id'] if durable else seed['operation_id']
    need(final['operation_id']==expected_operation,'final_operation_not_durable')
    for cycle in cycles:need(cycle['proof']['operation_id']==expected_operation,'consumer_operation_not_durable')
    check_finance(seed,False);check_finance(final,terminal)
    for proof in event_proofs(events):
        fixed_identity(seed,proof,False)
        is_settled=terminal and proof['billing_state']=='settled'
        check_finance(proof,is_settled)
        finance_equal(final if is_settled else seed,proof)
    producer='sender-new' if case=='N01' else ('writer-new' if case.startswith('N03') else 'writer-old')
    for event in matching('receipt_committed'):
        need(event['instance']==producer,'raw_commit_instance')
        receipt=event['data'];need(receipt['attempt_count']==receipt['receipt_count']==1 and receipt['application_state']=='pending' and receipt['applied_at'] is None,'raw_committed_pending')
        need(timestamp(receipt['database_time'])<timestamp(receipt['input']['expires_at']),'receipt_before_expiry')
        completed=[e['data'] for e in matching('cycle_completed') if e['instance']==event['instance'] and e['cmd_id']==event['cmd_id']]
        need(len(completed)==1 and completed[0]['error_class']=='cancelled' and completed[0]['barrier_reached'] is True,'raw_cancelled_cycle')
        after=completed[0]['proof'];fixed_identity(receipt,after);finance_equal(receipt,after)
        need(after['input_successful_reads']==receipt['input_successful_reads'],'cancelled_decryption')
        need(after['application_state']=='pending' and after['applied_at'] is None,'raw_cancelled_application')
    for event in matching('purged'):
        expected_purger='recovery-new' if case=='N01' else ('first-new' if case=='N02' else 'purger-new')
        need(event['instance']==expected_purger,'raw_purge_instance')
        before,after=event['data']['before'],event['data']['after']
        fixed_identity(before,after);finance_equal(before,after)
        need(timestamp(before['database_time'])>=timestamp(before['input']['expires_at']),'purge_after_expiry')
        need(after['input']['capture_status']=='expired' and after['input']['key_present'] is False and after['input']['nonce_present'] is False and after['input']['staging_count']==0,'actual_purge')
        need(final['input_successful_reads']==after['input_successful_reads'],'post_purge_decryption')
    for event in matching('unknown_injected'):
        before,after=event['data']['before'],event['data']['after'];finance_equal(before,after)
        for key in ('attempt_sha256','receipt_sha256','billing_snapshot_sha256'):need(before[key]==after[key],'unknown_evidence_changed')
        if '-schema-' in case:need(event['data']['kind']=='schema' and before['input']['schema']==2 and after['input']['schema']==99,'raw_schema_fault')
        else:need(event['data']['kind']=='runtime' and before['runtime_version']!='future-runtime' and after['runtime_version']=='future-runtime','raw_runtime_fault')
        need(final['application_state']!='applied' and final['applied_at'] is None,'raw_unknown_applied')
    for read in matching('action_read'):
        need(case=='N03-action' and read['instance']=='first-old' and read['data']['bytes']>0 and read['data']['payload_ref']==final['input']['payload_ref'],'raw_old_typed_read')
    if case.startswith('N04'):
        first='first-'+('new' if case.endswith('-new') else 'old')
        first_cycles=[e['data'] for e in matching('cycle_completed') if e['instance']==first]
        need(len(first_cycles)>=2,'raw_first_unknown_recovery')
        for cycle in first_cycles:
            p=cycle['proof'];need(cycle['error_class']=='none' and p['application_state']!='applied' and p['applied_at'] is None,'raw_first_unknown_applied')
            finance_equal(seed,p)
    if not terminal and not case.startswith('N04'):
        need(final['application_state']=='applied' and final['operation_status']=='unknown' and final['task_status']=='queued','raw_cancel_ack')
    # The final consumer's second successful application must preserve both
    # immutable receipt identity and the complete money/Key/quota fingerprint.
    tail=cycles[-2:];fixed_identity(tail[0]['proof'],tail[1]['proof']);finance_equal(tail[0]['proof'],tail[1]['proof'])

COMMAND_EVENTS={'seed':'seeded','clock':'clock_set','action':'action_pending','inspect':'inspected','run_once':'cycle_completed','cancel_active':'cancel_requested','read_action':'action_read','purge':'purged','unknown':'unknown_injected','stop':'stop_ack'}
def command_ownership(commands,events):
    by_id={c['cmd_id']:c for c in commands}
    for command in commands:
        kind=command['command'];need(kind in COMMAND_EVENTS,'unknown_command')
        owned=[e for e in events if e['cmd_id']==command['cmd_id']]
        names=collections.Counter(e['event'] for e in owned)
        expected=collections.Counter({COMMAND_EVENTS[kind]:1})
        if kind=='run_once' and command.get('barrier')=='receipt_committed':expected['receipt_committed']=1
        need(names==expected,'command_completion_ownership')
        if kind=='run_once':need(command.get('barrier') in (None,'receipt_committed'),'command_barrier')
        if kind=='cancel_active':
            owner=by_id.get(command['owner_cmd_id'])
            need(owner is not None and owner['command']=='run_once' and owner.get('barrier')=='receipt_committed','cancel_command_owner')
            need(owned[0]['data']['owner_cmd_id']==owner['cmd_id'],'cancel_event_owner')
    need(all(e['event'] in ('ready','drained') for e in events if e['cmd_id']==0),'unowned_event')
def verify(evidence):
    root=evidence/'matrix';builds=load(evidence/'build-manifest.json');summary=load(root/'matrix-passed.json')
    need(not (root/'matrix-failed.json').exists(),'failure_marker')
    need(builds['old']['source']==OLD and re.fullmatch('[0-9a-f]{40}',builds['new']['source']) and builds['new']['source']!=OLD,'real_sources')
    need(summary['status']=='passed' and summary['families']==4 and summary['cases']==8 and summary['cases_passed']==list(INSTANCES),'summary')
    run_id=summary['run_id'];need(isinstance(run_id,str) and re.fullmatch('[0-9a-f]{16}',run_id),'summary_run_id')
    need(summary['old_source']==builds['old']['source'] and summary['new_source']==builds['new']['source'],'summary_sources')
    exits=load(root/'child-exits.json');need(len(exits)==25,'child_count')
    by_child={(x['case_id'],x['instance']):x for x in exits};need(len(by_child)==25,'duplicate_child')
    mock=[strict_json(s) for s in (root/'mock-requests.jsonl').read_text().splitlines()]
    need(all(r['case_id'] in INSTANCES and r['run_id']==run_id for r in mock),'mock_outside_run')
    need([r['sequence'] for r in mock]==list(range(1,len(mock)+1)),'mock_sequence')
    processes=0;events_total=0
    for case,instances in INSTANCES.items():
        result=load(root/(case+'-result.json'));need(result['status']=='passed' and result['case_id']==case,'case_result')
        real_calls=[r for r in mock if r['case_id']==case];need(result['http']==real_calls,'mock_result_mismatch')
        posts=[r for r in real_calls if r['method']=='POST'];deletes=[r for r in real_calls if r['method']=='DELETE']
        need(len(posts)==1 and posts[0]['body_sha256']==result['seed']['oracle']['request_body_sha256'],'original_post_once')
        action=case in ('N02','N03-action') or case.startswith('N04-schema')
        need(len(deletes)==int(action),'delete_once')
        if case in ('N01','N03-create'):need(any(r['method']=='GET' for r in real_calls),'real_settlement_get')
        for call in real_calls:
            need(call['instance'] in instances and call['method'] in ('POST','GET','DELETE'),'mock_identity')
            suffix='' if call['method']=='POST' else '/b1b-'+case.lower()
            need(call['path']=='/api/v3/contents/generations/tasks'+suffix,'mock_exact_target')
        case_events=[]
        for instance in instances:
            row=by_child[(case,instance)];role=instance.split('-')[-1]
            need(row['role']==role and row['source_sha']==builds[role]['source'] and row['binary_sha256']==builds[role]['sha256'],'child_source')
            need(row['exit_code']==0 and row['clean_exit'] is True and row['forced_stop'] is False,'child_exit')
            text=(root/(case+'-'+instance+'.log')).read_text(encoding='utf-8')
            need(not re.search(r'DATA RACE|--- (?:FAIL|SKIP):|^panic:',text,re.M),'test_failed')
            need(text.count('--- PASS: '+TEST)==1 and '\nPASS\n' in text,'actual_go_pass')
            events=[strict_json(line[10:]) for line in text.splitlines() if line.startswith('B1B_EVENT ')]
            commands=[strict_json(line) for line in (root/(case+'-'+instance+'-commands.jsonl')).read_text().splitlines()]
            need([c['cmd_id'] for c in commands]==list(range(1,len(commands)+1)),'command_sequence')
            need([x['seq'] for x in events]==list(range(1,len(events)+1)),'event_sequence')
            need(sum(e['event']=='ready' for e in events)==1 and sum(e['event']=='drained' for e in events)==1,'lifecycle_event_count')
            ready=events[0]['data'];producer=instance.startswith(('writer-','sender-'))
            need(ready['compiled_source']==builds[role]['source'] and ready['old_source']==OLD and ready['create_producer'] is producer and ready['mutation_producer'] is producer,'raw_ready_configuration')
            need(events[0]['run_id']==run_id,'child_run_id')
            for cmd in commands:
                need(cmd['protocol']=='execution-clock-handoff-v1' and cmd['run_id']==run_id and cmd['case_id']==case and cmd['instance']==instance,'raw_command_identity')
            need(events[0]['event']=='ready' and events[-1]['event']=='drained' and events[-1]['data']=={'active_work':False,'pools_closed':True},'actual_drain')
            need(sum(x['event']=='stop_ack' for x in events)==1,'stop_ack_count')
            for event in events:
                need(event['protocol']=='execution-clock-handoff-v1' and event['run_id']==run_id,'raw_event_protocol')
                need(event['case_id']==case and event['instance']==instance and event['role']==role and event['pid']==row['pid'] and event['source_sha']==builds[role]['source'],'raw_event_identity')
                need(event['event']!='failed','failed_event')
                need(event['cmd_id']==0 or event['cmd_id'] in {c['cmd_id'] for c in commands},'event_command_owner')
            command_ownership(commands,events)
            processes+=1;events_total+=len(events);case_events+=events
        verify_case_proofs(case,result,case_events)
        names=collections.Counter(x['event'] for x in case_events)
        need(names['seeded']==1,'seed_once')
        if case!='N03-create':need(names['receipt_committed']==1 and names['cancel_requested']==1,'real_commit_barrier')
        if case=='N03-action':need(names['action_read']==1,'old_typed_read')
        if case in ('N01','N02') or case.startswith('N04'):need(names['purged']==1,'real_purge')
        if case.startswith('N04'):need(names['unknown_injected']==1 and result['final']['applied_at'] is None,'unknown_preserved')
    return {'status':'passed','families':4,'cases':8,'actual_processes':processes,'raw_events':events_total,'old_source':OLD,'new_source':builds['new']['source']}
if __name__=='__main__':print(json.dumps(verify(pathlib.Path(sys.argv[1]))))
