"""Dashboard and account-default proxy HTTP acceptance; no real AWS calls."""
import json
import uuid
from identity import Client, MEMBER_PASSWORD
from inventory import payload


def check_dashboard(admin):
    before=payload(admin,'/dashboard')
    assert before['account_count']>=1 and not before['can_refresh']
    status,_,page=admin.request('/')
    assert status==200 and '账号池配额' in page and 'Bedrock 模型配额' in page and 'data-dashboard-refresh' in page
    assert admin.request('/partials/dashboard')[0]==200
    assert admin.request('/api/v1/dashboard/refresh','POST',data={'idempotency_key':str(uuid.uuid4())},csrf=False)[0]==403
    status,_,body=admin.request('/api/v1/dashboard/refresh','POST',data={'idempotency_key':str(uuid.uuid4())})
    assert status==409 and json.loads(body)['error']['code']=='NO_VERIFIED_CONNECTION'
    assert 'name="default_proxy_id"' in admin.request('/accounts/new')[2]
    assert 'value="http"' in admin.request('/proxies/new')[2]
    status,_,body=admin.request('/api/v1/proxies','POST',data={'name':'Dashboard HTTP proxy','protocol':'http','host':'127.0.0.1','port':10809})
    assert status==201
    proxy=json.loads(body)['data'];assert proxy['protocol']=='http'
    status,_,body=admin.request('/api/v1/accounts','POST',data={'name':'Dashboard pool fixture','partition':'aws','aws_account_id':'666666666666','default_route_mode':'proxy','default_proxy_id':proxy['id']})
    assert status==201
    account=json.loads(body)['data'];assert account['default_route_mode']=='http' and account['default_proxy_name']==proxy['name']
    assert 'value="inherit" selected' in admin.request('/accounts/'+account['id']+'/connections/new')[2]
    proxy_view=payload(admin,'/proxies/'+proxy['id'])
    assert proxy_view['accounts']==1 and proxy_view['connections']==0
    assert admin.request('/api/v1/proxies/'+proxy['id'],'DELETE',data={'version':proxy['version']})[0]==400
    current=payload(admin,'/dashboard');assert current['account_count']==before['account_count']+1
    reader=Client(admin.origin);assert reader.login('account-reader',MEMBER_PASSWORD)[0]==200
    assert '666666666666' not in reader.request('/api/v1/dashboard')[2]
    members=payload(admin,'/members')['items']
    member=next(m for m in members if m['username']=='account-reader')
    assert admin.request(f"/api/v1/members/{member['id']}/account-grants",'POST',data={'account_id':account['id'],'access':'read'})[0]==204
    visible=payload(reader,'/accounts/'+account['id'])
    assert not visible['account'].get('default_proxy_id') and not visible['account'].get('default_proxy_name') and not visible['account'].get('default_route_mode')
    assert 'Dashboard HTTP proxy' not in reader.request('/api/v1/dashboard')[2]
    assert admin.request('/api/v1/accounts/'+account['id'],'DELETE',data={'version':account['version']})[0]==204
    assert admin.request('/api/v1/proxies/'+proxy['id'],'DELETE',data={'version':proxy['version']})[0]==204
    assert payload(admin,'/dashboard')['account_count']==before['account_count']
