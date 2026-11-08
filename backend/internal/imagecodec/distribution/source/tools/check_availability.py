#!/usr/bin/env python3
"""HEAD-only source availability/size audit. Never fetch archive bodies."""
import argparse,concurrent.futures,datetime,json,pathlib,re,subprocess
p=argparse.ArgumentParser(description=__doc__);p.add_argument('manifest',type=pathlib.Path);p.add_argument('--output',type=pathlib.Path,required=True);p.add_argument('--jobs',type=int,default=3);p.add_argument('--timeout',type=int,default=15);a=p.parse_args()
rows=json.loads(a.manifest.read_text())['sources']
def check(item):
 r=subprocess.run(['curl','-sS','-I','-L','--max-time',str(a.timeout),'--max-redirs','4','-w','\nHE_HTTP:%{http_code}\nHE_URL:%{url_effective}\n',item['url']],capture_output=True,text=True)
 final={}
 for block in re.split(r'\r?\n\r?\n',r.stdout):
  if block.startswith('HTTP/'):
   final={}
   for line in block.splitlines()[1:]:
    if ':' in line:
     k,v=line.split(':',1);final[k.lower()]=v.strip()
 status=re.search(r'HE_HTTP:(\d+)',r.stdout);url=re.search(r'HE_URL:(.*)',r.stdout)
 return dict(item,headStatus=int(status[1]) if status else None,headExit=r.returncode,contentLength=int(final['content-length']) if final.get('content-length','').isdigit() else None,effectiveURL=url[1] if url else None,diagnostic=r.stderr.strip())
with concurrent.futures.ThreadPoolExecutor(max_workers=a.jobs) as pool:results=list(pool.map(check,rows))
report={'checkedUTC':datetime.datetime.now(datetime.timezone.utc).isoformat(),'source':str(a.manifest),'scope':'HEAD responses only; missing length is unknown, not zero','sources':results}
a.output.write_text(json.dumps(report,indent=2)+'\n');print(f"{sum(r['headStatus']==200 for r in results)}/{len(results)} HTTP 200; {sum(r['contentLength'] or 0 for r in results if r['headStatus']==200)} known bytes")
