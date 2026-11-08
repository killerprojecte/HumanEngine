#!/usr/bin/env python3
"""Package a source-only checkpoint; exclude shared libraries/ELF executables.
The resulting checkpoint is not a claim that full native distribution is ready.
"""
import argparse,hashlib,io,json,pathlib,tarfile
p=argparse.ArgumentParser(description=__doc__);p.add_argument('overlay',type=pathlib.Path);p.add_argument('output',type=pathlib.Path);a=p.parse_args();root=a.overlay.resolve();out=a.output.resolve();out.parent.mkdir(parents=True,exist_ok=True)
entries=[]
for f in sorted(root.rglob('*')):
 if not f.is_file() or f.is_symlink():continue
 rel=f.relative_to(root)
 if f.resolve()==out or any(part in {'checkpoints','__pycache__'} for part in rel.parts):continue
 if '.so' in f.name or f.suffix in {'.pyc','.test','.exe'} or f.name=='FILES-SHA256.json':continue
 with f.open('rb') as stream:
  if stream.read(4)==b'\x7fELF':continue
 entries.append((f,rel))
manifest={'status':'source-only checkpoint; full native distribution source/notices and runtime validation gates remain open','files':[{'file':str(r),'bytes':f.stat().st_size,'sha256':hashlib.sha256(f.read_bytes()).hexdigest()} for f,r in entries]}
with tarfile.open(out,'w:gz') as archive:
 for f,r in entries:archive.add(f,arcname=str(pathlib.Path('humanengine-image-source')/r),recursive=False)
 b=(json.dumps(manifest,indent=2)+'\n').encode();info=tarfile.TarInfo('humanengine-image-source/CHECKPOINT-MANIFEST.json');info.size=len(b);info.mode=0o644;archive.addfile(info,io.BytesIO(b))
print(json.dumps({'file':str(out),'files':len(entries),'bytes':out.stat().st_size,'sha256':hashlib.sha256(out.read_bytes()).hexdigest()},indent=2))
