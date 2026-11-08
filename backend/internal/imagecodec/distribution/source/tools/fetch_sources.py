#!/usr/bin/env python3
"""Retrieve the exact upstream recipe's source inputs; never compile/execute them.
Default prints a plan. --download requires an explicit byte budget and either a
previous lock or --bootstrap-lock. Bootstrap hashes are trust-on-first-download,
not a claim that tags/PR patches are immutable or historical build bytes proven.
"""
import argparse,hashlib,json,os,pathlib,re,sys,urllib.request,urllib.parse
ROOT=pathlib.Path(__file__).resolve().parent.parent
class HTTPSOnly(urllib.request.HTTPRedirectHandler):
 def redirect_request(self,req,fp,code,msg,headers,newurl):
  if urllib.parse.urlparse(newurl).scheme!='https':raise RuntimeError('Refusing non-HTTPS source redirect')
  return super().redirect_request(req,fp,code,msg,headers,newurl)
def digest(path):
 h=hashlib.sha256()
 with path.open('rb') as f:
  for block in iter(lambda:f.read(1024*1024),b''):h.update(block)
 return h.hexdigest()
def main():
 ap=argparse.ArgumentParser(description=__doc__);ap.add_argument('--manifest',type=pathlib.Path,default=ROOT/'acquisition-complete-plan.json');ap.add_argument('--dest',type=pathlib.Path,required=True);ap.add_argument('--download',action='store_true');ap.add_argument('--lock',type=pathlib.Path);ap.add_argument('--bootstrap-lock',type=pathlib.Path);ap.add_argument('--max-total-bytes',type=int);ap.add_argument('--max-file-bytes',type=int);ap.add_argument('--include-proxy-libintl',action='store_true');args=ap.parse_args()
 rows=json.loads(args.manifest.read_text())['sources'];rows=[r for r in rows if r['component']!='proxy-libintl' or args.include_proxy_libintl]
 for r in rows:
  base=pathlib.PurePosixPath(urllib.parse.urlparse(r['url']).path).name
  r['file']=re.sub('[^A-Za-z0-9._-]','_',str(r['recipeLine'])+'-'+(r['component'] or 'patch')+'-'+base)
 if not args.download:print(json.dumps({'download':False,'inputs':rows,'next':'Supply --download, byte budgets, and --lock or --bootstrap-lock.'},indent=2));return
 if bool(args.lock)==bool(args.bootstrap_lock):ap.error('Choose exactly one of --lock / --bootstrap-lock')
 if not args.max_total_bytes or not args.max_file_bytes:ap.error('Explicit positive total and per-file download budgets are required')
 if args.max_total_bytes<1 or args.max_file_bytes<1:ap.error('Byte budgets must be positive')
 locks={r['url']:r for r in json.loads(args.lock.read_text())['files']} if args.lock else {}
 if args.lock:
  for r in rows:
   expected=locks.get(r['url'],{}).get('sha256','')
   if not re.fullmatch('[0-9a-f]{64}',expected):raise RuntimeError('Missing trusted lock for '+r['url'])
 args.dest.mkdir(parents=True,exist_ok=True);opener=urllib.request.build_opener(HTTPSOnly());total=0;done=[];failures=[]
 for r in rows:
  target=args.dest/r['file'];temporary=target.with_name(target.name+'.part')
  size=0
  try:
   if target.exists():
    actual=digest(target);size=target.stat().st_size
    if args.lock and actual!=locks[r['url']]['sha256']:raise RuntimeError('Existing source hash mismatch: '+str(target))
    if total+size>args.max_total_bytes:raise RuntimeError('Total download/material budget exceeded')
    total+=size;done.append(dict(r,sha256=actual,bytes=size,finalURL=locks.get(r['url'],{}).get('finalURL'),cached=True));continue
   if temporary.exists():raise RuntimeError('Partial file exists; inspect/remove it before retry: '+str(temporary))
   if urllib.parse.urlparse(r['url']).scheme!='https':raise RuntimeError('Only HTTPS sources are accepted')
   request=urllib.request.Request(r['url'],headers={'User-Agent':'HumanEngine-source-materials/1','Accept-Encoding':'identity'})
   h=hashlib.sha256();size=0
   with opener.open(request,timeout=30) as response,temporary.open('xb') as out:
    if response.status!=200:raise RuntimeError('Unexpected HTTP status '+str(response.status))
    final=response.geturl();length=response.headers.get('Content-Length')
    if length and int(length)>min(args.max_file_bytes,args.max_total_bytes-total):raise RuntimeError('Advertised source exceeds budget')
    for block in iter(lambda:response.read(65536),b''):
     size+=len(block)
     if size>args.max_file_bytes or total+size>args.max_total_bytes:raise RuntimeError('Actual source exceeds explicit download budget')
     out.write(block);h.update(block)
   actual=h.hexdigest()
   if args.lock and actual!=locks[r['url']]['sha256']:raise RuntimeError('Downloaded source hash mismatch')
   os.replace(temporary,target);total+=size;done.append(dict(r,sha256=actual,bytes=size,finalURL=final,cached=False));print(f'{r["file"]}: {size} bytes {actual}',flush=True)
  except Exception as e:
   # Do not retry denials or continue an uncertain download automatically.
   # Count partial bytes against the material budget as well.
   total+=size
   failures.append({'url':r['url'],'error':str(e),'partial':str(temporary) if temporary.exists() else None});print(str(e),file=sys.stderr)
 record={'status':'complete' if not failures else 'incomplete','hashProvenance':'verified against supplied lock' if args.lock else 'bootstrap observation; not historical-source authenticity proof','bytes':total,'files':done,'failures':failures}
 output=args.bootstrap_lock or args.dest/'verified-downloads.json';output.write_text(json.dumps(record,indent=2)+'\n')
 if failures:raise SystemExit(1)
if __name__=='__main__':main()
