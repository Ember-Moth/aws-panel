-- Explicit local preview fixture. No AWS observations or create calls.
-- smoke.py keeps cloud writes disabled and all fixture connections disabled.
DO $$
DECLARE
  actor bigint;
  account_id uuid;
  connection_id uuid;
  plan_id uuid := gen_random_uuid();
  stamp timestamptz := clock_timestamp();
  items jsonb := '[]';
  targets jsonb := '[]';
  spec jsonb;
  target jsonb;
  idx integer;
BEGIN
  SELECT id INTO STRICT actor FROM members WHERE username='admin';
  SELECT a.id,c.id INTO STRICT account_id,connection_id FROM aws_accounts a JOIN connections c ON c.account_id=a.id WHERE a.aws_account_id='333333333333' AND a.deleted_at IS NULL LIMIT 1;
  FOR idx IN 1..2 LOOP
    target := jsonb_build_object('connection_id',connection_id::text,'region','us-east-1','name','preview-web-0'||idx,'zone','us-east-1a','blueprint_id','debian_12','bundle_id',CASE WHEN idx=1 THEN 'small_3_0' ELSE 'small_ipv6_3_0' END,'ip_type',CASE WHEN idx=1 THEN 'dualstack' ELSE 'ipv6' END,'key_pair_name','preview-key');
    spec := jsonb_build_object('target',target,'blueprint',jsonb_build_object('id','debian_12','name','Debian 12','version','12.9','min_power',100), 'bundle',jsonb_build_object('id',target->>'bundle_id','name','Small','cpu',2,'ram_gb',2,'disk_gb',60,'power',500,'monthly_cents',CASE WHEN idx=1 THEN 500 ELSE 350 END,'public_ipv4',idx=1), 'quota',jsonb_build_object('known',idx=1,'limit_vcpu',100,'used_vcpu',4),'checked_at',stamp);
    targets := targets || jsonb_build_array(target);
    items := items || jsonb_build_array(jsonb_build_object('target',jsonb_build_object('key',account_id::text||'/us-east-1/'||(target->>'name'),'label','333333333333 · us-east-1 · '||(target->>'name'),'lock_key','lightsail/instance/'||(target->>'name'),'binding',jsonb_build_object('account_id',account_id::text,'connection_id',connection_id::text,'aws_account_id','333333333333','partition','aws','region','us-east-1','fingerprint','browser-fixture')), 'steps',jsonb_build_array(jsonb_build_object('kind','create','label','创建并确认实例归属','writes_cloud',true,'input',spec),jsonb_build_object('kind','ready','label','等待实例运行','writes_cloud',false,'input',spec))));
  END LOOP;
  INSERT INTO plans(id,actor_id,operation,executor_version,input_version,input,input_hash,items,created_at,expires_at)
  VALUES(plan_id,actor,'lightsail.create',1,1,jsonb_build_object('targets',targets),'browser-lightsail-preview-only',items,stamp,stamp+interval '2 hours');
END $$;
