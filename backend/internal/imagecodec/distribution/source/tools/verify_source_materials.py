#!/usr/bin/env python3
"""Verify complete requested material coverage/hashes, not legal sufficiency."""
import argparse,hashlib,json,pathlib,sys
p=argparse.ArgumentParser(description=__doc__);p.add_argument('--manifest',type=pathlib.Path,required=True);p.add_argument('--lock',type=pathlib.Path,required=True);p.add_argument('--archives',type=pathlib.Path,required=True);p.add_argument('--notices',type=pathlib.Path,required=True);p.add_argument('--include-proxy-libintl',action='store_true');a=p.parse_args()
expected=[r for r in json.loads(a.manifest.read_text())['sources'] if r['component']!='proxy-libintl' or a.include_proxy_libintl]
locked={r['url']:r for r in json.loads(a.lock.read_text())['files']};notices=json.loads((a.notices/'notices-manifest.json').read_text());coverage={r['component']:r for r in notices['coverage']};fail=[]
def verify(path,want):
 h=hashlib.sha256()
 with path.open('rb') as f:
  for block in iter(lambda:f.read(1024*1024),b''):h.update(block)
 if h.hexdigest()!=want:raise RuntimeError('hash mismatch '+str(path))
for e in expected:
 try:
  r=locked[e['url']];verify(a.archives/r['file'],r['sha256'])
  if e['kind']=='source' and coverage.get(e['component'],{}).get('legalFiles',0)<1:raise RuntimeError('copyright coverage missing '+e['component'])
 except Exception as error:fail.append({'url':e['url'],'error':str(error)})
for r in notices['files']:
 try:verify(a.notices/r['file'],r['sha256'])
 except Exception as error:fail.append({'notice':r['file'],'error':str(error)})
print(json.dumps({'status':'integrity-and-file-coverage-complete; legal/source-correspondence review still required' if not fail else 'incomplete','requestedInputs':len(expected),'failures':fail},indent=2));sys.exit(bool(fail))
