#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Drive only the disposable guest launched by run-hosted.sh; retain partial rows on failure."""
import base64
import copy
import hashlib
import json
import pathlib
import secrets
import subprocess
import sys
import time

import oracle

SCOPE="real-NM; actual-guard-account; injected-probe; root-library-fixture; production-admission-unqualified"
class Guest:
    def __init__(self,out):
        self.out=out;self.ssh=json.loads((out/'ssh.json').read_text());self.rows=[];self.serial=0
    def run(self,command,check=True,timeout=180):
        p=subprocess.run(self.ssh+[command],capture_output=True,text=True,timeout=timeout)
        if check and p.returncode: raise RuntimeError("guest command failed: "+command+"\n"+p.stderr)
        return p
    def boot(self):
        old=self.run('cat /proc/sys/kernel/random/boot_id').stdout.strip()
        self.run('sudo systemctl reboot',False,20)
        limit=time.monotonic()+240
        while time.monotonic()<limit:
            time.sleep(3)
            p=self.run('cat /proc/sys/kernel/random/boot_id',False,10)
            if p.returncode==0 and p.stdout.strip()!=old:
                self.run('sudo cloud-init status --wait',False,180)
                return old,p.stdout.strip()
        raise RuntimeError('guest reboot not measured')
    def request(self,value,allow_error=False):
        encoded=base64.b64encode(json.dumps(value).encode()).decode()
        script="import socket,json,base64; s=socket.socket(socket.AF_UNIX); s.settimeout(40); s.connect('/run/olivares-network-core/control.sock'); s.sendall(base64.b64decode('"+encoded+"')); s.shutdown(socket.SHUT_WR); b=b''\nwhile True:\n c=s.recv(65536)\n if not c:break\n b+=c\nprint(b.decode())"
        command="sudo python3 -c "+__import__('shlex').quote(script)
        p=self.run(command,timeout=50);answer=json.loads(p.stdout)
        self.serial+=1;name=f'wire-{self.serial:04d}.json';(self.out/name).write_text(json.dumps({'request':value,'response':answer},indent=2)+'\n')
        if answer.get('error') and not allow_error: raise RuntimeError(answer['error'])
        return answer,name
    def observe(self): return self.request({'Action':'observe'})[0]['observation']
    def status(self,op): return self.request({'Action':'status','OperationID':op})[0]['status']
    def wait(self,op,state,seconds=100):
        end=time.monotonic()+seconds
        while time.monotonic()<end:
            got=self.status(op)
            if got['state']==state:return got
            time.sleep(1)
        raise RuntimeError('network window did not reach '+state)
    def apply(self,last=20,ipv6=None,dns=None):
        before=self.observe();family=copy.deepcopy(before['Profile']['ipv4']);family['addresses']=[f'10.0.3.{last}/24'];family['never_default']=True
        if dns is not None:family['dns']=dns;family['search']=['example.test']
        change={'operation_id':secrets.token_hex(16),'profile_uuid':before['Profile']['uuid'],'interface':'nic1','ipv4':family,'window_seconds':60}
        if ipv6 is not None:change={'operation_id':secrets.token_hex(16),'profile_uuid':before['Profile']['uuid'],'interface':'nic1','ipv6':ipv6,'window_seconds':60}
        answer,receipt=self.request({'Action':'apply','Change':change});assert answer['status']['state']=='awaiting_confirmation'
        return before,change,answer,receipt
    def confirm(self,change,answer,**updates):
        c={'operation_id':change['operation_id'],'digest':answer['digest'],'token':answer['token'],'confirmation_class':'non_management','probe_witness':'fixture:'+answer['digest']+':nic1'}
        c.update(updates)
        return self.request({'Action':'confirm','Confirmation':c},True)
    def row(self,name,facts,assertions):
        if not assertions or not all(v is True for v in assertions): raise AssertionError(name+' failed')
        receipt=name+'.json';(self.out/receipt).write_text(json.dumps(facts,indent=2)+'\n')
        self.rows.append({'id':name,'observed':True,'assertions':assertions,'scope':SCOPE,'receipts':[receipt]});self.save()
    def save(self,error=None):
        p=self.run('getenforce',False,10)
        avc=self.run('sudo ausearch -m AVC,USER_AVC -ts boot',False,20)
        (self.out/'avcs.txt').write_text(avc.stdout+avc.stderr)
        value={'schema':'network-guest-cases/v1','head':(self.out/'head.txt').read_text().strip(),'selinux':p.stdout.strip(),'avcs':avc.stdout.splitlines(),'cases':self.rows,'unqualified_composition':['HM-02-C','A2'],'error':error}
        (self.out/'cases.json').write_text(json.dumps(value,indent=2)+'\n')

def persistent(o):
    p=o['Profile'];return p['filename'].startswith('/etc/NetworkManager/system-connections/') and not p['flags']&1 and o['RuntimeUsable'] is True

def main():
    g=Guest(pathlib.Path(sys.argv[1]))
    try:
        g.boot()
        g.run('sudo touch /run/olivares-network-guest-authorized; sudo bash /var/tmp/network-payload/setup-guest.sh qualify',timeout=900)
        for _ in range(60):
            p=g.run('sudo test -S /run/olivares-network-core/control.sock',False,10)
            if p.returncode==0:break
            time.sleep(2)
        baseline=g.observe()
        record=json.loads(g.run('sudo cat /var/lib/olivares-appliance/state.json').stdout)
        g.row('G-N0',{'baseline':baseline,'record':record},[persistent(baseline),record.get('host_settings_owner')=='appliance'])
        negative=g.run('sudo cat /var/lib/olivares-network-guest/second-owner-negative.txt').stdout
        g.row('G-N0-second-owner',{'refusal':negative},['second_network_owner' in negative])
        profile=baseline['Profile']
        introspection=g.run('sudo busctl --system introspect org.freedesktop.NetworkManager '+profile['connection']+' org.freedesktop.NetworkManager.Settings.Connection').stdout
        credentials=g.run('sudo busctl --system call org.freedesktop.DBus /org/freedesktop/DBus org.freedesktop.DBus GetConnectionCredentials s org.freedesktop.NetworkManager').stdout
        g.row('installed-interface-identity',{'interface':introspection,'credentials':credentials,'baseline':baseline},['VersionId' in introspection,'ProcessFD' in credentials,profile['profile_version']>0,profile['applied_version']>0])
        before,change,answer,_=g.apply(19);s=g.wait(change['operation_id'],'rolled_back');after=g.observe()
        g.row('G-N1',{'before':before,'after':after,'status':s},[persistent(after),after['Profile']['ipv4']==before['Profile']['ipv4'],s['final_known_boottime_ns']>=s['last_observation_boottime_ns'],s['pending_calls']==0])
        before,change,answer,_=g.apply(20);confirmed,_=g.confirm(change,answer);assert not confirmed.get('error');s=g.wait(change['operation_id'],'confirmed');preboot=g.observe()
        keyfiles=g.run('sudo sh -c "sha256sum /etc/NetworkManager/system-connections/*"').stdout
        g.boot();after=g.observe()
        g.row('G-N2-core',{'status':s,'before_reboot':preboot,'after_reboot':after},[persistent(after),after['Profile']['ipv4']==preboot['Profile']['ipv4']])
        keyfiles_after=g.run('sudo sh -c "sha256sum /etc/NetworkManager/system-connections/*"').stdout
        g.row('G-N5',{'before':keyfiles,'after':keyfiles_after},[keyfiles==keyfiles_after])
        before,change,answer,_=g.apply(30);time.sleep(30);old=g.run('systemctl show NetworkManager -p MainPID --value').stdout
        g.run('sudo systemctl restart NetworkManager');time.sleep(3);new=g.run('systemctl show NetworkManager -p MainPID --value').stdout
        s=g.wait(change['operation_id'],'rolled_back',120);after=g.observe();journal,_=g.request({'Action':'journal','OperationID':change['operation_id']})
        g.row('G-N3',{'old_pid':old,'new_pid':new,'status':s,'journal':journal,'before':before,'after':after},[persistent(after),after['Profile']['ipv4']==before['Profile']['ipv4'],s['pending_calls']==0]+oracle.restored_through_netrestore(journal['window'],int(old),int(new),before,after))
        keyfiles_before=g.run('sudo sh -c "sha256sum /etc/NetworkManager/system-connections/*"').stdout
        before,change,answer,_=g.apply(40);time.sleep(30);during=g.observe()
        keyfiles_during=g.run('sudo sh -c "sha256sum /etc/NetworkManager/system-connections/*"').stdout
        boot_before,boot_after=g.boot();s=g.wait(change['operation_id'],'rolled_back',120);after=g.observe()
        g.row('G-N4',{'status':s,'before':before,'during':during,'after':after,'keyfiles_before':keyfiles_before,'keyfiles_during':keyfiles_during,'boot_before':boot_before,'boot_after':boot_after},[persistent(after),after['Profile']['ipv4']==before['Profile']['ipv4']]+oracle.in_memory_only(during,keyfiles_before,keyfiles_during)+oracle.reboot_rollback(s,boot_before,boot_after))
        before,change,answer,_=g.apply(20,dns=['10.0.3.3']);confirmed,_=g.confirm(change,answer);assert not confirmed.get('error');g.wait(change['operation_id'],'confirmed');after=g.observe()
        dns=g.run('sudo busctl --system get-property org.freedesktop.NetworkManager /org/freedesktop/NetworkManager/DnsManager org.freedesktop.NetworkManager.DnsManager Configuration').stdout
        resolution=g.run("python3 -c \"import socket,struct; s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.setsockopt(socket.SOL_SOCKET,socket.SO_BINDTODEVICE,b'nic1\\0');s.bind(('10.0.3.20',0));s.settimeout(5);s.sendto(b'\\x12\\x34\\x01\\x00\\x00\\x01\\x00\\x00\\x00\\x00\\x00\\x00\\x07example\\x03com\\x00\\x00\\x01\\x00\\x01',('10.0.3.3',53));r=s.recv(4096);assert r[:2]==b'\\x12\\x34' and r[3]&15==0 and int.from_bytes(r[6:8],'big')>0;print('device-bound DNS answer')\"").stdout
        g.row('G-N6-core',{'observation':after,'dns_manager':dns,'resolution':resolution},[after['Applied']['ipv4']['search']==['example.test'],'10.0.3.3' in dns,'device-bound DNS answer' in resolution])
        failures=[]
        for updates in [{'token':'0'*32},{'digest':'0'*64},{'operation_id':'f'*32},{'probe_witness':'fixture:foreign:nic1'},{'probe_witness':''}]:
            # A refused confirm leaves its window to expire at the deadline; nothing reverts it.
            before,change,answer,_=g.apply(50);refused,_=g.confirm(change,answer,**updates);s=g.wait(change['operation_id'],'rolled_back',120);failures.append(oracle.refused_then_expired(refused,s))
        before,change,answer,_=g.apply(51);good,_=g.confirm(change,answer);again,_=g.confirm(change,answer);failures.append(not good.get('error') and bool(again.get('error')))
        before,change,answer,_=g.apply(52);g.wait(change['operation_id'],'rolled_back');late,_=g.confirm(change,answer);failures.append(bool(late.get('error')))
        g.row('G-N8-core',{'refusals':failures},failures)
        # Installed access controls, under actual accounts and transient units. No cgroup impersonation.
        no_authority=g.run("sudo systemd-run --quiet --wait --pipe --uid=olivares-portal /usr/bin/python3 -c \"import os,signal; paths=['/run/olivares-network/network.lock','/var/lib/olivares-net-guard/acts']; results=[]\nfor p in paths:\n try: f=os.open(p,os.O_RDONLY);os.close(f);results.append(False)\n except PermissionError:results.append(True)\nassert all(results);print('protected paths refused')\"").stdout
        permissions=g.run('sudo systemd-run --quiet --wait --pipe --uid=olivares-portal /usr/bin/busctl --system call org.freedesktop.NetworkManager /org/freedesktop/NetworkManager org.freedesktop.NetworkManager GetPermissions').stdout
        g.row('portal-has-no-writer-authority',{'custody':no_authority,'permissions':permissions},['protected paths refused' in no_authority,not any('"org.freedesktop.NetworkManager.'+a+'" "yes"' in permissions for a in ['settings.modify.system','network-control','checkpoint-rollback'])])
        negatives=[]
        for helper in ['netprobe','netrestore']:
            document={'operation_id':secrets.token_hex(16)}
            if helper=='netrestore':document['connection_uuid']=profile['uuid']
            encoded=base64.b64encode(json.dumps(document).encode()).decode()
            script="import socket,base64,json;s=socket.socket(socket.AF_UNIX);s.settimeout(5);s.connect('/run/olivares-helpers/"+helper+".sock');s.sendall(base64.b64decode('"+encoded+"'));s.shutdown(socket.SHUT_WR);r=json.loads(s.recv(4096));assert r['result']=='refused';print(json.dumps(r))"
            p=g.run('sudo systemd-run --quiet --wait --pipe /usr/bin/python3 -c '+__import__('shlex').quote(script));negatives.append(p.stdout)
        g.row('transient-driver-refused',{'real_helper_refusals':negatives},[len(negatives)==2,all('refused' in v for v in negatives)])
        # G-N7 is driven by the explicit IPv6 router fixture, with packet receipts. Its DHCPv6 lease is
        # acquired before the change and must renew after the revert. The reboots above cleared /run,
        # so the fixture authorization is recreated before each step.
        g.run('sudo touch /run/olivares-network-guest-authorized; sudo bash /var/tmp/network-payload/setup-guest.sh ipv6-fixture',timeout=240)
        addr6=lambda:g.run('ip -j -6 addr show dev nic1').stdout
        route6=lambda:g.run('ip -j -6 route show default dev nic1').stdout
        clock=lambda:int(g.run('date +%s').stdout)
        before6=addr6();routes_before=route6();started=clock()
        before,change,answer,_=g.apply(60);during6=addr6();routes_during=route6();g.wait(change['operation_id'],'rolled_back')
        ended=clock();after6=addr6();routes_after=route6()
        g.run('sudo touch /run/olivares-network-guest-authorized; sudo bash /var/tmp/network-payload/setup-guest.sh ipv6-renewal',timeout=240)
        lease=json.loads(g.run('sudo cat /var/lib/olivares-network-guest/dhcp6-measured.json').stdout)
        packets=g.run('sudo tcpdump -n -r /var/lib/olivares-network-guest/dhcp6.pcap',timeout=30).stdout
        ipv6_static={'method':'manual','addresses':['fd00:33::20/64'],'gateway':'','routes':[],'dns':[],'search':[],'never_default':True}
        before,change,answer,_=g.apply(ipv6=ipv6_static);g.wait(change['operation_id'],'rolled_back');after=g.observe()
        slaac=[oracle.slaac_addresses(v) for v in (before6,during6,after6)];defaults=[oracle.default_routes(v) for v in (routes_before,routes_during,routes_after)]
        g.row('G-N7',{'before_ipv6':before6,'during_ipv6':during6,'after_ipv6':after6,'default_before':routes_before,'default_during':routes_during,'default_after':routes_after,'change_started':started,'change_ended':ended,'dhcp6':lease,'packets':packets,'static_before':before,'static_after':after},[bool(defaults[0]),defaults[0]==defaults[1]==defaults[2],after['Profile']['ipv6']==before['Profile']['ipv6'],'dhcp6 solicit' in packets.lower(),'dhcp6 renew' in packets.lower()]+oracle.slaac_kept(*slaac)+oracle.dhcp6_renewed_across(lease,started,ended))
        g.save();return 0
    except Exception as error:
        try:g.save(str(error))
        except Exception:pass
        print('UNQUALIFIED: '+str(error),file=sys.stderr);return 2
if __name__=='__main__':sys.exit(main())
