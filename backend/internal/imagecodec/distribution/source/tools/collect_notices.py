#!/usr/bin/env python3
"""Copy legal/copyright files from hash-verified source archives without execution.
Does not claim that filename discovery alone completes license compliance.
"""
import argparse,hashlib,json,pathlib,re,tarfile,zipfile
LEGAL=re.compile(r'^(copying|copyrights?|licen[sc]es?|(?:third[ _-]party[ _-])?notices?|authors|thanks|patents|legal)([._ -].*)?$',re.I)
def legal_path(name):
 parts=pathlib.PurePosixPath(name).parts
 return bool(LEGAL.match(parts[-1])) or any(p.lower() in {'licenses','licences','legal','notices'} for p in parts[:-1])
def hash_file(p):
 h=hashlib.sha256()
 with p.open('rb') as f:
  for b in iter(lambda:f.read(1024*1024),b''):h.update(b)
 return h.hexdigest()
def safe_name(value):return re.sub('[^A-Za-z0-9._-]','_',value)
def main():
 ap=argparse.ArgumentParser(description=__doc__);ap.add_argument('--lock',type=pathlib.Path,required=True);ap.add_argument('--archives',type=pathlib.Path,required=True);ap.add_argument('--output',type=pathlib.Path,required=True);args=ap.parse_args()
 lock=json.loads(args.lock.read_text());args.output.mkdir(parents=True,exist_ok=True);records=[];coverage=[]
 for row in lock['files']:
  if row['kind']!='source':continue
  source=args.archives/row['file'];actual=hash_file(source)
  if actual!=row['sha256']:raise RuntimeError('Archive hash mismatch: '+str(source))
  component=row['component'];found=[]
  def save(name,content):
   # Never extract original paths or symlinks. Preserve the original path only
   # as provenance; deterministic flattened filenames avoid traversal attacks.
   sub=args.output/safe_name(component);sub.mkdir(exist_ok=True)
   filename=f'{len(found):04d}-'+safe_name(pathlib.PurePosixPath(name).name)
   dest=sub/filename;dest.write_bytes(content);record={'component':component,'sourceArchive':row['file'],'sourceSHA256':actual,'originalPath':name,'file':str(dest.relative_to(args.output)),'bytes':len(content),'sha256':hashlib.sha256(content).hexdigest()};records.append(record);found.append(record)
  if tarfile.is_tarfile(source):
   with tarfile.open(source,'r:*') as archive:
    for member in archive:
     if member.isfile() and legal_path(member.name):
      stream=archive.extractfile(member)
      if stream is not None:save(member.name,stream.read())
  elif zipfile.is_zipfile(source):
   with zipfile.ZipFile(source) as archive:
    for entry in archive.infolist():
     if not entry.is_dir() and legal_path(entry.filename):save(entry.filename,archive.read(entry))
  else:raise RuntimeError('Unknown source archive format: '+str(source))
  coverage.append({'component':component,'legalFiles':len(found),'reviewRequired':True})
 result={'status':'copyright material collected; manual dependency/SPDX/embedded-crate reconciliation still required','files':records,'coverage':coverage}
 (args.output/'notices-manifest.json').write_text(json.dumps(result,indent=2)+'\n')
 missing=[r['component'] for r in coverage if r['legalFiles']==0]
 if missing:raise SystemExit('No legal files found for: '+', '.join(missing))
 print(f'Collected {len(records)} legal files from {len(coverage)} hash-verified source archives')
if __name__=='__main__':main()
