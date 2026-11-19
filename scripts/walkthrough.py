#!/usr/bin/env python3
"""Run bounded production HTTP journeys against an isolated ephemeral service."""
import argparse, base64, concurrent.futures, copy, hashlib, json, os, pathlib, re, signal, socket, struct, subprocess, sys, tempfile, time, urllib.error, urllib.request, zlib
ROOT=pathlib.Path(__file__).resolve().parents[1]
FONT_SHA256='59cbdc7da5a229d8591ae3f352c52abbef537aea5aed63f3a17fd2943b80cdae'
def make_font(tmp):
    out=pathlib.Path(tmp)/'prism-demo-sans.ttf'
    subprocess.run([sys.executable,str(ROOT/'scripts/make_demo_font.py'),str(out)],check=True,capture_output=True)
    data=out.read_bytes()
    assert hashlib.sha256(data).hexdigest()==FONT_SHA256,'demo font digest changed'
    return data
def png_pixels(raw):
    assert raw[:8]==b'\x89PNG\r\n\x1a\n'
    pos=8;w=h=ct=None;data=b''
    while pos<len(raw):
        ln,=struct.unpack('>I',raw[pos:pos+4]);typ=raw[pos+4:pos+8];chunk=raw[pos+8:pos+8+ln];pos+=12+ln
        if typ==b'IHDR':
            w,h,bd,ct,_,_,inter=struct.unpack('>IIBBBBB',chunk);assert bd==8 and ct in (2,6) and inter==0
        elif typ==b'IDAT':data+=chunk
        elif typ==b'IEND':break
    bpp=3 if ct==2 else 4
    img=zlib.decompress(data);stride=w*bpp;px=bytearray(w*h*4);prev=bytearray(stride);o=0
    for y in range(h):
        f=img[o];o+=1;line=bytearray(img[o:o+stride]);o+=stride
        for i in range(stride):
            a=line[i-bpp] if i>=bpp else 0;b=prev[i];c=prev[i-bpp] if i>=bpp else 0
            if f==1:line[i]=(line[i]+a)&255
            elif f==2:line[i]=(line[i]+b)&255
            elif f==3:line[i]=(line[i]+(a+b)//2)&255
            elif f==4:
                p=a+b-c;pa,pb,pc=abs(p-a),abs(p-b),abs(p-c)
                line[i]=(line[i]+(a if (pa<=pb and pa<=pc) else (b if pb<=pc else c)))&255
        for x in range(w):
            px[(y*w+x)*4:(y*w+x)*4+3]=line[x*bpp:x*bpp+3]
            px[(y*w+x)*4+3]=line[x*bpp+3] if bpp==4 else 255
        prev=line
    return w,h,bytes(px)
def main():
    parser=argparse.ArgumentParser();parser.add_argument('--workflow',required=True);args=parser.parse_args()
    seed=json.loads((ROOT/'examples/amber-proof.json').read_text())
    with tempfile.TemporaryDirectory(prefix='prism-plate-') as tmp:
        base=pathlib.Path(tmp);binary=base/'prismplate';subprocess.run(['go','build','-o',str(binary),'./cmd/prismplate'],cwd=ROOT,check=True)
        sock=socket.socket();sock.bind(('127.0.0.1',0));port=sock.getsockname()[1];sock.close();url=f'http://127.0.0.1:{port}'
        log=(base/'service.log').open('w');proc=None
        def start():
            nonlocal proc
            proc=subprocess.Popen([str(binary),'-listen',f'127.0.0.1:{port}','-data',str(base/'state')],stdout=log,stderr=log)
            for _ in range(100):
                if proc.poll() is not None:raise RuntimeError('service exited')
                try:
                    urllib.request.urlopen(url+'/healthz',timeout=1).close();return
                except OSError:time.sleep(.05)
            raise RuntimeError('startup timed out')
        def stop():
            if proc and proc.poll() is None:
                proc.send_signal(signal.SIGTERM)
                try:proc.wait(timeout=45)
                except subprocess.TimeoutExpired:proc.kill();proc.wait();raise
        def call(method,path,data=None,status=200,raw=False,headers=None):
            body=data if isinstance(data,bytes) else (json.dumps(data).encode() if data is not None else None)
            req=urllib.request.Request(url+path,data=body,method=method,headers=headers or {'Content-Type':'application/json'})
            try:
                with urllib.request.urlopen(req,timeout=35) as r:code=r.status;payload=r.read();hs={k.lower():v for k,v in r.headers.items()}
            except urllib.error.HTTPError as e:code=e.code;payload=e.read();hs={k.lower():v for k,v in e.headers.items()}
            if code!=status:raise RuntimeError(f'{method} {path}: expected {status}, observed {code}: {payload[:200]!r}')
            return (payload,hs) if raw else json.loads(payload) if payload else None
        def render(fmt='png',revision=1):return call('POST',f'/v1/plates/amber-plate/render?revision={revision}&format={fmt}',status=201)
        try:
            start();created=call('POST','/v1/plates',seed,status=201);assert created['revisions'][0]['number']==1
            w=args.workflow
            if w=='create':
                assert call('GET','/v1/plates/amber-plate')['title']==seed['title'];call('POST','/v1/plates',seed,status=409)
                bad=copy.deepcopy(seed);bad['id']='bad-plate';bad['document']['layers'][0]['shapes'][0]['transform']=[0]*6;call('POST','/v1/plates',bad,status=400)
            elif w=='revise':
                q={'expected_revision':1,'document':copy.deepcopy(seed['document'])};q['document']['background']['r']=.2
                with concurrent.futures.ThreadPoolExecutor(2) as pool:
                    def attempt(_):
                        try:return call('PUT','/v1/plates/amber-plate',q)
                        except RuntimeError as e:
                            if 'observed 409' in str(e):return 'conflict'
                            raise
                    results=list(pool.map(attempt,range(2)))
                assert sum(x=='conflict' for x in results)==1;assert len(call('GET','/v1/plates/amber-plate')['revisions'])==2
                assert call('GET','/v1/plates/amber-plate')['revisions'][0]['document']==created['revisions'][0]['document']
            elif w=='branch':
                b=call('POST','/v1/plates/amber-plate/branch',{'revision':1,'id':'amber-fork','title':'琥珀新稿'},status=201);assert b['revisions'][0]['digest']==created['revisions'][0]['digest'];call('POST','/v1/plates/amber-plate/branch',{'revision':99,'id':'missing-fork','title':'无效稿'},status=404)
            elif w=='archive':
                call('POST','/v1/plates/amber-plate/archive');call('PUT','/v1/plates/amber-plate',{'expected_revision':1,'document':seed['document']},status=409);assert render()['revision']==1
            elif w in ('png','svg','artifact'):
                fmt='svg' if w=='svg' else 'png';a=render(fmt);raw,hs=call('GET','/v1/artifacts/'+a['id'],raw=True);assert hashlib.sha256(raw).hexdigest()==a['digest']
                if fmt=='png':assert raw[:8]==b'\x89PNG\r\n\x1a\n';assert struct.unpack('>II',raw[16:24])==(192,144)
                else:assert b'<svg' in raw and b'<linearGradient' in raw and b'clipPath' in raw
                assert render(fmt)['digest']==a['digest'];call('GET','/v1/artifacts/'+a['id'],status=304,raw=True,headers={'If-None-Match':hs['etag']});call('GET','/v1/artifacts/missing',status=404)
                if w=='artifact':call('DELETE','/v1/artifacts/'+a['id']);call('GET','/v1/artifacts/'+a['id'],status=409)
            elif w in ('exposure','cancel'):
                job=call('POST','/v1/plates/amber-plate/exposures?revision=1&format=png',status=202)
                if w=='cancel':call('DELETE','/v1/exposures/'+job['id'],status=202)
                for _ in range(200):
                    job=call('GET','/v1/exposures/'+job['id'])
                    if job['state'] in ('succeeded','cancelled','failed'):break
                    time.sleep(.02)
                assert job['state'] in (('succeeded','cancelled') if w=='cancel' else ('succeeded',))
                if job['state']=='succeeded':assert call('GET','/v1/artifacts/'+job['artifact'],raw=True)[0][:8]==b'\x89PNG\r\n\x1a\n'
                call('DELETE','/v1/exposures/absent',status=404)
            elif w=='proof':
                a=render();call('POST','/v1/proofs',{'id':'proof-amber','artifact':a['id']},status=201);assert call('PUT','/v1/proofs/proof-amber',{'state':'accepted','note':'叠色与镂空检查通过'})['state']=='accepted';call('DELETE','/v1/artifacts/'+a['id'],status=409);call('PUT','/v1/proofs/proof-amber',{'state':'rejected','note':'重复裁决'},status=409)
            elif w in ('bundle','restore'):
                a=render();sealed,_=call('GET','/v1/plates/amber-plate/bundle',raw=True);payload=json.loads(sealed);assert payload['plate']['revisions'][0]['digest']==a['scene_digest']
                if w=='bundle':
                    call('POST','/v1/bundles',sealed,status=409);payload['digest']='0'*64;call('POST','/v1/bundles',payload,status=400)
                    stop();os.rename(base/'state',base/'original');start();imported=call('POST','/v1/bundles',sealed,status=201);assert imported['revisions'][0]['digest']==a['scene_digest'];assert render()['digest']==a['digest']
                else:
                    stop();start();assert call('GET','/v1/artifacts/'+a['id'],raw=True)[0][:8]==b'\x89PNG\r\n\x1a\n';assert render()['digest']==a['digest']
            elif w=='isolation':
                lockproc=subprocess.run([str(binary),'-listen','127.0.0.1:0','-data',str(base/'state')],capture_output=True,timeout=3);assert lockproc.returncode!=0;assert b'locked' in lockproc.stderr
                call('POST','/v1/plates',b'{"id":"x"} true',status=400)
            elif w=='rejection':
                bad=copy.deepcopy(seed);bad['id']='oversize';bad['document']['width']=513;call('POST','/v1/plates',bad,status=400);call('GET','/v1/plates/oversize',status=404)
                bad=copy.deepcopy(seed);bad['id']='invalid-brush';bad['document']['layers'][0]['shapes'][0]['brush']['stops'][1]['at']=0;call('POST','/v1/plates',bad,status=400);assert render()['revision']==1
            elif w=='text':
                fb=make_font(base)
                meta=call('POST','/v1/fonts',{'id':'demo-sans','data':base64.b64encode(fb).decode()},status=201)
                assert meta['digest']==FONT_SHA256 and meta['family']=='Prism Demo Sans' and 'Open Font License' in meta['license'] and meta['glyphs']==10 and meta['units_per_em']==1000
                call('POST','/v1/fonts',{'id':'demo-sans','data':base64.b64encode(fb).decode()},status=409)
                call('POST','/v1/fonts',{'id':'truncated','data':base64.b64encode(fb[:200]).decode()},status=400)
                cff=bytearray(fb);cff[0:4]=b'OTTO';call('POST','/v1/fonts',{'id':'cff','data':base64.b64encode(bytes(cff)).decode()},status=400)
                got=call('GET','/v1/fonts/demo-sans');assert got['digest']==FONT_SHA256 and got['data'] is None
                call('GET','/v1/fonts/absent',status=404)
                def node(tid,source,origin,size,spacing=0,pin=None,transform=(1,0,0,1,0,0)):
                    n={'id':tid,'source':source,'font':'demo-sans','size':size,'origin':{'x':origin[0],'y':origin[1]},'letter_spacing':spacing,'brush':{'kind':'solid','color':{'r':0.8,'g':0.2,'b':0.15,'a':1}},'transform':list(transform)}
                    if pin:n['font_digest']=pin
                    return n
                def doc(texts):
                    return {'width':192,'height':144,'background':{'r':0.04,'g':0.055,'b':0.09,'a':1},'layers':[{'id':'sheet','opacity':1,'blend':'normal','shapes':[],'texts':texts}]}
                kern_doc=doc([node('kern-a','AV',(10,140),100,pin=FONT_SHA256),node('kern-b','AV',(10,60),50,spacing=5)])
                call('POST','/v1/plates',{'id':'text-plate','title':'文字校样','document':kern_doc},status=201)
                a=call('POST','/v1/plates/text-plate/render?revision=1&format=svg',status=201)
                raw,_=call('GET','/v1/artifacts/'+a['id'],raw=True)
                assert b'<text' not in raw and b'font' not in raw.lower()
                paths=re.findall(rb'<path d="([^"]+)"',raw);assert len(paths)==2
                def last_min_x(d):
                    subs=d.split(b'M')[1:]
                    nums=[float(v) for v in re.findall(rb'-?\d+(?:\.\d+)?',subs[-1])]
                    return min(nums[0::2])
                assert abs(last_min_x(paths[0])-75)<1e-6
                assert abs(last_min_x(paths[1])-47.5)<1e-6
                holes_doc=doc([node('holes','O8',(10,110),100)])
                call('POST','/v1/plates',{'id':'holes-plate','title':'内孔校样','document':holes_doc},status=201)
                h1=call('POST','/v1/plates/holes-plate/render?revision=1&format=png',status=201)
                raw,_=call('GET','/v1/artifacts/'+h1['id'],raw=True)
                pw,ph,px=png_pixels(raw);assert (pw,ph)==(192,144)
                def pixel(x,y):
                    o=(y*pw+x)*4;return tuple(px[o:o+4])
                bg=(10,14,23,255);fg=(204,51,38,255)
                assert pixel(48,75)==bg and pixel(22,75)==fg
                assert pixel(121,52)==bg and pixel(121,90)==bg
                assert pixel(138,90)==fg
                assert call('POST','/v1/plates/holes-plate/render?revision=1&format=png',status=201)['digest']==h1['digest']
                bar_doc=doc([node('bar','棱',(10,110),100)])
                call('POST','/v1/plates',{'id':'bar-plate','title':'相交轮廓','document':bar_doc},status=201)
                b1=call('POST','/v1/plates/bar-plate/render?revision=1&format=png',status=201)
                raw,_=call('GET','/v1/artifacts/'+b1['id'],raw=True)
                pw,ph,px=png_pixels(raw)
                def pixel2(x,y):
                    o=(y*pw+x)*4;return tuple(px[o:o+4])
                assert pixel2(60,62)==fg and pixel2(60,50)==bg and pixel2(60,100)==fg
                cjk_doc=doc([node('cjk','棱版Ä',(0,0),50,transform=(1.1276,0.4104,-0.4104,1.1276,15,90))])
                call('POST','/v1/plates',{'id':'cjk-plate','title':'旋转缩放','document':cjk_doc},status=201)
                c1=call('POST','/v1/plates/cjk-plate/render?revision=1&format=png',status=201)
                assert call('POST','/v1/plates/cjk-plate/render?revision=1&format=png',status=201)['digest']==c1['digest']
                bad=doc([node('bad-text','AVΩ',(10,100),20)])
                err=call('POST','/v1/plates',{'id':'missing-glyph','title':'x','document':bad},status=400)
                assert 'U+03A9' in err['error'] and 'rune index 2' in err['error'] and 'bad-text' in err['error']
                bad=doc([node('ghost','AV',(10,100),20)]);bad['layers'][0]['texts'][0]['font']='ghost'
                assert 'unknown font' in call('POST','/v1/plates',{'id':'no-font','title':'x','document':bad},status=400)['error']
                bad=doc([node('bad-pin','AV',(10,100),20,pin='0'*64)])
                assert 'digest' in call('POST','/v1/plates',{'id':'bad-pin','title':'x','document':bad},status=400)['error']
                rev2=copy.deepcopy(kern_doc);rev2['layers'][0]['texts'][1]['source']='VA'
                call('PUT','/v1/plates/text-plate',{'expected_revision':1,'document':rev2})
                a2=call('POST','/v1/plates/text-plate/render?revision=2&format=svg',status=201)
                assert a2['digest']!=a['digest']
                assert call('POST','/v1/plates/text-plate/render?revision=1&format=svg',status=201)['digest']==a['digest']
            elif w=='textbundle':
                fb=make_font(base)
                call('POST','/v1/fonts',{'id':'demo-sans','data':base64.b64encode(fb).decode()},status=201)
                def bdoc(source):
                    return {'width':160,'height':96,'background':{'r':0.5,'g':0.25,'b':0.75,'a':1},'layers':[{'id':'sheet','opacity':1,'blend':'normal','shapes':[],'texts':[{'id':'note','source':source,'font':'demo-sans','font_digest':FONT_SHA256,'size':40,'origin':{'x':8,'y':64},'letter_spacing':0.5,'brush':{'kind':'solid','color':{'r':1,'g':1,'b':1,'a':1}},'transform':[1,0,0,1,0,0]}]}]}
                call('POST','/v1/plates',{'id':'bundle-a','title':'封包甲','document':bdoc('O8')},status=201)
                call('POST','/v1/plates',{'id':'bundle-b','title':'封包乙','document':bdoc('VA 棱')},status=201)
                pa=call('POST','/v1/plates/bundle-a/render?revision=1&format=png',status=201)
                sa=call('POST','/v1/plates/bundle-a/render?revision=1&format=svg',status=201)
                sealed,_=call('GET','/v1/plates/bundle-a/bundle',raw=True)
                sealed_b,_=call('GET','/v1/plates/bundle-b/bundle',raw=True)
                payload=json.loads(sealed)
                assert payload['schema']=='prism-plate-bundle-v2'
                assert len(payload['fonts'])==1 and payload['fonts'][0]['id']=='demo-sans' and payload['fonts'][0]['digest']==FONT_SHA256
                def digest_of(inner):
                    return hashlib.sha256(json.dumps(inner,separators=(',',':'),ensure_ascii=False).encode()).hexdigest()
                assert digest_of({'plate':payload['plate'],'fonts':payload['fonts']})==payload['digest']
                call('POST','/v1/bundles',sealed,status=409)
                tampered=json.loads(sealed)
                rawf=bytearray(base64.b64decode(tampered['fonts'][0]['data']));rawf[100]^=1
                tampered['fonts'][0]['data']=base64.b64encode(bytes(rawf)).decode()
                tampered['digest']=digest_of({'plate':tampered['plate'],'fonts':tampered['fonts']})
                call('POST','/v1/bundles',tampered,status=400)
                nofont=json.loads(sealed);del nofont['fonts']
                nofont['digest']=digest_of({'plate':nofont['plate']})
                assert 'unknown font' in call('POST','/v1/bundles',nofont,status=400)['error']
                bad=json.loads(sealed);bad['digest']='0'*64
                call('POST','/v1/bundles',bad,status=400)
                stop();os.rename(base/'state',base/'origin');start()
                imported=call('POST','/v1/bundles',sealed,status=201)
                assert imported['revisions'][0]['document']['layers'][0]['texts'][0]['source']=='O8'
                call('POST','/v1/bundles',sealed_b,status=201)
                got=call('GET','/v1/fonts/demo-sans');assert got['digest']==FONT_SHA256 and got['family']=='Prism Demo Sans'
                p2=call('POST','/v1/plates/bundle-a/render?revision=1&format=png',status=201)
                s2=call('POST','/v1/plates/bundle-a/render?revision=1&format=svg',status=201)
                assert p2['digest']==pa['digest'] and p2['id']==pa['id']
                assert s2['digest']==sa['digest']
                note=call('GET','/v1/plates/bundle-a')['revisions'][0]['document']['layers'][0]['texts'][0]
                assert note['source']=='O8' and note['font_digest']==FONT_SHA256
            else:raise RuntimeError('unknown workflow '+w)
            print(json.dumps({'workflow':w,'result':'passed','entry':'HTTP','port':port},ensure_ascii=False))
        finally:stop();log.close()
if __name__=='__main__':main()
