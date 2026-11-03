"""HTTP permissions, validation and non-secret form behavior without real AWS."""
import json
from identity import Client, MEMBER_PASSWORD
from inventory import payload


def check_compute(admin):
    options=payload(admin,'/ec2/options')
    assert options['accounts'] and not options['writes_enabled']
    connection=next(a for a in options['accounts'] if a['aws_account_id']=='333333333333')['connections'][0]['id']
    for route,token in [('/wavelength','data-wavelength-form'),('/ec2','data-ec2-form'),('/quotas','配额检测')]:
        status,_,page=admin.request(route)
        assert status==200 and token in page,(route,status)
    for route in ('/wavelength/zones','/ec2/catalogue','/ec2/instances','/addresses/catalogue'):
        status,_,raw=admin.request('/api/v1'+route+f'?connection_id={connection}&region=us-east-1&service=ec2')
        assert status==409 and json.loads(raw)['error']['code']=='CONNECTION_DISABLED',(route,status)
    for route in ('ec2','addresses'):
        assert admin.request(f'/api/v1/{route}/plans','POST',data={'targets':[]})[0]==400
        assert admin.request(f'/api/v1/{route}/plans','POST',data={'targets':[]},csrf=False)[0]==403
    password='HTTP compute password fixture 2026!'
    form={'connection_id':connection,'region':'us-east-1','action':'create','name':'http-instance','image_id':'ami-11111111','instance_type':'t3.micro','subnet_id':'subnet-11111111','security_group_ids':'sg-11111111','disk_gb':'16','login_method':'root_password','root_password':password,'root_password_confirm':'mismatch'}
    status,_,body=admin.request('/ec2/plans','POST',form=form,headers={'HX-Request':'true'})
    assert status==400 and 'id="ec2-form"' in body and password not in body and 'mismatch' not in body
    status,_,body=admin.request('/ec2?connection_id='+connection+'&region=us-east-1&wavelength_zone_id=use1-wl1-bos-wlz1&action=create')
    assert status==200 and 'Wavelength 实例' in body and '加密 gp2' in body
    status,headers,body=admin.request('/addresses')
    assert status==303 and headers.get('Location')=='/ec2'
    for path in ('/ec2/instances/i-11111111/ip','/lightsail/instances/fixture-name/ip'):
        status,_,body=admin.request(path+'?connection_id='+connection+'&region=us-east-1')
        assert status==409 and '连接' in body
    ip_form={'connection_id':connection,'region':'us-east-1','service':'ec2','instance_id':'i-11111111'}
    status,_,body=admin.request('/instances/ip/plans','POST',form=ip_form,headers={'HX-Request':'true'})
    assert status==409 and 'id="address-form"' in body and '自动释放' in body and 'name="release_previous"' not in body
    assert admin.request('/instances/ip/plans','POST',form=ip_form,csrf=False)[0]==403
    optin={'connection_id' :connection,'region':'us-east-1','action':'wavelength_opt_in','wavelength_zone_id':'use1-wl1-bos-wlz1'}
    status,_,body=admin.request('/ec2/plans','POST',form=optin,headers={'HX-Request':'true'})
    assert status==409 and 'id="wavelength-workspace"' in body
    assert admin.request('/ec2/plans','POST',form=optin,csrf=False)[0]==403
    assert admin.request('/api/v1/quotas?connection_id='+connection+'&region=invalid&service=ec2')[0]==400
    reader=Client(admin.origin)
    assert reader.login('account-reader',MEMBER_PASSWORD)[0]==200
    for path in ('/wavelength','/api/v1/wavelength/zones?connection_id='+connection+'&region=us-east-1','/ec2','/addresses','/api/v1/ec2/options','/api/v1/ec2/instances?connection_id='+connection+'&region=us-east-1','/api/v1/addresses/options'):
        assert reader.request(path)[0]==403,path
    assert reader.request('/quotas')[0]==200
    for kind in ('ec2_instance','ec2_address','lightsail_static_ip'):
        assert reader.request('/resources?kind='+kind)[0]==200,kind
