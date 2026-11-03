-- Disposable browser/HTTP cache fixtures only. These are not AWS observations.
DO $$
DECLARE
    account_a uuid;
    account_b uuid := gen_random_uuid();
    connection_a uuid;
    actor bigint;
    east_scope uuid := gen_random_uuid();
    west_scope uuid := gen_random_uuid();
    other_scope uuid := gen_random_uuid();
    account_regions uuid := gen_random_uuid();
    lightsail_regions uuid := gen_random_uuid();
    region_plan uuid := gen_random_uuid();
    stamp timestamptz := clock_timestamp();
BEGIN
    SELECT id INTO STRICT account_a FROM aws_accounts WHERE aws_account_id='333333333333' AND deleted_at IS NULL;
    SELECT id INTO STRICT connection_a FROM connections WHERE account_id=account_a LIMIT 1;
    SELECT id INTO STRICT actor FROM members WHERE username='admin';
    INSERT INTO aws_accounts(id,partition,aws_account_id,name,created_at,updated_at)
    VALUES(account_b,'aws','555555555555','预览辅助账号',stamp,stamp);
    INSERT INTO inventory_scopes(id,account_id,kind,region,generation,status,last_attempt_at,last_success_at,last_error_code,last_connection_id,resource_count,created_at)
    VALUES
    (east_scope,account_a,'lightsail_instance','us-east-1',1,'ready',stamp,stamp,'',connection_a::text,1,stamp),
    (west_scope,account_a,'lightsail_instance','us-west-2',1,'failed',stamp,stamp-interval '1 hour','AWS_ACCESS_DENIED',connection_a::text,1,stamp),
    (other_scope,account_b,'lightsail_instance','us-east-1',1,'ready',stamp,stamp,'','',1,stamp),
    (account_regions,account_a,'account_region','global',1,'ready',stamp,stamp,'',connection_a::text,2,stamp),
    (lightsail_regions,account_a,'lightsail_region','global',1,'ready',stamp,stamp,'',connection_a::text,2,stamp);
    INSERT INTO resource_snapshots(id,scope_id,resource_id,name,region,state,details,observed_at,generation)
    VALUES
    (gen_random_uuid(),east_scope,'arn:aws:lightsail:us-east-1:333333333333:Instance/preview-web','预览 Web','us-east-1','running',
    '{"instance":{"blueprint_id":"debian_12","blueprint_name":"Debian 12","bundle_id":"small_3_0","availability_zone":"us-east-1a","cpu":2,"ram_gb":2,"ip_type":"ipv4","public_ip":"127.0.0.10","private_ip":"10.4.0.10","static_ip":true,"username":"admin","ssh_key_name":"preview-only","created_at":"2026-11-01T00:00:00Z","disks":[{"size_gb":60,"system":true}],"ports":[{"protocol":"tcp","from":443,"to":443,"cidrs":["0.0.0.0/0"]}]}}',stamp,1),
    (gen_random_uuid(),west_scope,'arn:aws:lightsail:us-west-2:333333333333:Instance/preview-cache','旧快照 <b>原样文本</b>','us-west-2','stopped',
    '{"instance":{"blueprint_id":"ubuntu_24_04","blueprint_name":"Ubuntu 24.04","bundle_id":"nano_3_0","availability_zone":"us-west-2a","cpu":2,"ram_gb":0.5,"ip_type":"ipv4","private_ip":"10.4.0.11","username":"ubuntu"}}',stamp-interval '1 hour',1),
    (gen_random_uuid(),other_scope,'arn:aws:lightsail:us-east-1:555555555555:Instance/preview-private','受限预览实例','us-east-1','running',
    '{"instance":{"blueprint_name":"Debian 12","cpu":2,"ram_gb":1,"ip_type":"ipv4"}}',stamp,1),
    (gen_random_uuid(),account_regions,'us-east-1','us-east-1','us-east-1','ENABLED_BY_DEFAULT','{"region":{"opt_status":"ENABLED_BY_DEFAULT"}}',stamp,1),
    (gen_random_uuid(),account_regions,'ap-east-1','ap-east-1','ap-east-1','DISABLED','{"region":{"opt_status":"DISABLED"}}',stamp,1),
    (gen_random_uuid(),lightsail_regions,'us-east-1','Virginia','us-east-1','available','{"region":{"display_name":"Virginia","availability_zones":[{"name":"us-east-1a","state":"available"}]}}',stamp,1),
    (gen_random_uuid(),lightsail_regions,'us-west-2','Oregon','us-west-2','available','{"region":{"display_name":"Oregon","availability_zones":[{"name":"us-west-2a","state":"available"}]}}',stamp,1);
    INSERT INTO inventory_schedules(id,actor_id,name,request,interval_seconds,active,next_run_at,created_at,updated_at)
    SELECT gen_random_uuid(),actor,label,
    jsonb_build_object('targets',jsonb_build_array(jsonb_build_object('connection_id',connection_a::text,'kind','lightsail_instance','region','us-east-1'))),
    900,false,stamp+interval '15 minutes',stamp,stamp
    FROM (VALUES ('预览定时同步（已停用）'),('待删除的定时同步夹具')) AS labels(label);
    -- Read-only region preview fixture. The application's cloud-write gate is
    -- explicitly disabled by smoke.py, and the connection remains disabled.
    INSERT INTO plans(id,actor_id,operation,executor_version,input_version,input,input_hash,items,created_at,expires_at)
    VALUES(region_plan,actor,'regions.enable',1,1,
      jsonb_build_object('targets',jsonb_build_array(jsonb_build_object('connection_id',connection_a::text,'region','ap-east-1'))),
      'browser-region-preview-only',
      jsonb_build_array(jsonb_build_object('target',jsonb_build_object('key',account_a::text||'/ap-east-1','label','333333333333 · ap-east-1','lock_key','account/region/ap-east-1','binding',jsonb_build_object('account_id',account_a::text,'connection_id',connection_a::text,'aws_account_id','333333333333','partition','aws','region','us-east-1','fingerprint','browser-fixture')),
        'steps',jsonb_build_array(jsonb_build_object('kind','enable','label','启用区域并等待就绪','writes_cloud',true,'input',jsonb_build_object('region','ap-east-1','observed_state','DISABLED','checked_at',stamp))))),stamp,stamp+interval '2 hours');
END $$;
