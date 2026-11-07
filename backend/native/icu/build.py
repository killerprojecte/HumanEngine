#!/usr/bin/env python3
"""Build pinned Node ICU, one compiler process at a time; no network or installs.
Linux little-endian ELF only. Pass the official Node source archive downloaded
from https://nodejs.org/dist/v24.22.1/node-v24.22.1.tar.xz.
"""
import argparse, bz2, hashlib, json, os, pathlib, shlex, subprocess, sys, tarfile, tempfile, time
HERE = pathlib.Path(__file__).resolve().parent
ARCHIVE = '4f5379796280cdaeb0c2f358fd04b6a16cc4fb7dfa83b5a8c91a9d963b435231'
DATA = 'f294f56707f4bbbc669d291ff537f632eb8d363a8ea60b70b091cdf273ea0499'
MANIFEST = 'c4769fb719612e04c279cae928bb875d6b962d5f99f01f180bb79ee70761ac25'
PREFIX = 'node-v24.22.1/deps/icu-small/'
def sha(path): return hashlib.sha256(path.read_bytes()).hexdigest()
def run(args): subprocess.run(args, check=True)
def object_key(source_digest, manifest_digest, compiler, command):
    # The verified manifest covers every header and translation unit, not just
    # this .cpp. Changing any pinned dependency invalidates every cached object.
    inputs={'schema':2, 'sourceSHA256':source_digest,
            'sourceManifestSHA256':manifest_digest, 'compiler':compiler,
            'command':command}
    return hashlib.sha256(json.dumps(inputs, sort_keys=True).encode()).hexdigest()

def cached_object(out, stamp, key):
    try:
        record=json.loads(stamp.read_text())
        return (record['key']==key and record['objectSHA256']==sha(out))
    except (OSError, ValueError, KeyError, TypeError):
        return False

def compile_object(command, out, key=None, runner=run):
    """Publish only a complete object, then its checksum-bound cache record.

    A failed/interrupted compiler never writes the final path. If interruption
    happens between object and stamp publication, the old stamp cannot validate
    different object bytes. Legacy plain-text stamps are intentionally misses.
    """
    stamp=out.with_suffix('.key')
    if key is not None and cached_object(out, stamp, key):
        return None
    with tempfile.TemporaryDirectory(prefix='.compile-', dir=out.parent) as temp:
        temporary=pathlib.Path(temp)/out.name
        actual=list(command)
        actual[actual.index('-o')+1]=str(temporary)
        runner(actual)
        digest=sha(temporary)
        temporary_stamp=pathlib.Path(temp)/stamp.name
        if key is not None:
            temporary_stamp.write_text(json.dumps({'key':key, 'objectSHA256':digest})+'\n')
        os.replace(temporary, out)
        if key is not None:
            os.replace(temporary_stamp, stamp)
    return actual

def build_archive(ar, objects, lib, runner=run):
    # ar rcsD updates existing archives and retains unmentioned members. Always
    # start at a fresh path so removed sources cannot survive a later rebuild.
    with tempfile.TemporaryDirectory(prefix='.archive-', dir=lib.parent) as temp:
        temporary=pathlib.Path(temp)/lib.name
        command=[ar, 'rcsD', str(temporary), *[str(x) for x in objects]]
        runner(command)
        os.replace(temporary, lib)
    return command

def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--archive', type=pathlib.Path, required=True)
    p.add_argument('--work-dir', type=pathlib.Path, required=True)
    p.add_argument('--prepare-only', action='store_true')
    a=p.parse_args(); work=a.work_dir.resolve()
    if sys.platform != 'linux' or sys.byteorder != 'little': p.error('currently verified only on Linux little-endian ELF')
    if any(c.isspace() for c in str(work)): p.error('cgo build path must not contain whitespace')
    if sha(a.archive) != ARCHIVE: raise ValueError('official Node archive checksum mismatch')
    if sha(HERE/'sources.sha256') != MANIFEST: raise ValueError('source manifest checksum mismatch')
    work.mkdir(parents=True, exist_ok=True)
    # Reject all links/devices and traversal before extracting selected members.
    with tarfile.open(a.archive, 'r:xz') as tf:
        members=[m for m in tf if m.name.startswith(PREFIX)]
        for m in members:
            path=pathlib.PurePosixPath(m.name)
            if path.is_absolute() or '..' in path.parts or not (m.isfile() or m.isdir()): raise ValueError('unsafe archive member: '+m.name)
        tf.extractall(work, members=members, filter='data')
    source=work/PREFIX
    verified=[]
    for line in (HERE/'sources.sha256').read_text().splitlines():
        digest, name=line.split('  ', 1)
        if sha(source/name) != digest: raise ValueError('source checksum mismatch: '+name)
        verified.append(source/name)
    actual={x.relative_to(source).as_posix() for x in source.rglob('*') if x.is_file()}
    expected={x.relative_to(source).as_posix() for x in verified}
    if actual - expected - {'source/data/in/icudt78l.dat'}: raise ValueError('unexpected files in extracted source tree')
    dat=source/'source/data/in/icudt78l.dat'
    data=bz2.decompress(dat.with_suffix('.dat.bz2').read_bytes())
    if hashlib.sha256(data).hexdigest()!=DATA: raise ValueError('vendored full ICU data checksum mismatch')
    dat.write_bytes(data)
    if a.prepare_only: print('Verified Node archive, all ICU sources, and full data'); return
    start=time.monotonic(); obj=work/'objects'; obj.mkdir(exist_ok=True)
    cxx=os.environ.get('CXX', 'g++'); cc=os.environ.get('CC','gcc'); ar=os.environ.get('AR','ar')
    flags=['-std=c++17','-O2','-fPIC','-fno-strict-aliasing','-ffunction-sections','-fdata-sections',
           '-DU_STATIC_IMPLEMENTATION=1','-DUCONFIG_NO_SERVICE=1','-DU_ENABLE_DYLOAD=0','-DU_HAVE_STD_STRING=1',
           '-DUCONFIG_NO_BREAK_ITERATION=0','-DU_ATTRIBUTE_DEPRECATED=',
           '-ffile-prefix-map='+str(work)+'=icu-node-v24.22.1',
           '-I'+str(source/'source/common'), '-I'+str(source/'source/i18n')]
    commands=[]; objects=[]
    compiler=subprocess.check_output([cxx,'--version'],text=True)
    manifest_digest=sha(HERE/'sources.sha256')
    for kind, define in [('common','U_COMMON_IMPLEMENTATION'),('i18n','U_I18N_IMPLEMENTATION')]:
        files=sorted(x for x in verified if x.parent==source/'source'/kind and x.suffix=='.cpp')
        for i, path in enumerate(files):
            out=obj/(kind+'_'+path.stem+'.o'); objects.append(out)
            cmd=[cxx,*flags,'-D'+define+'=1','-c',str(path),'-o',str(out)]
            key=object_key(sha(path), manifest_digest, compiler, cmd)
            executed=compile_object(cmd, out, key)
            commands.append(executed or cmd)
            if (i+1)%25==0 or i+1==len(files): print(f'{kind}: {i+1}/{len(files)} ({time.monotonic()-start:.0f}s)',flush=True)
    asm=work/'icudata.S'
    asm.write_text('.section .rodata\n.balign 16\n.globl icudt78_dat\n.type icudt78_dat, @object\nicudt78_dat:\n.incbin '+json.dumps(str(dat))+'\n.globl he_icu_data_end\nhe_icu_data_end:\n.size icudt78_dat, .-icudt78_dat\n.section .note.GNU-stack,"",@progbits\n')
    dataobj=obj/'icudata.o'; cmd=[cc,'-c',str(asm),'-o',str(dataobj)]
    commands.append(compile_object(cmd, dataobj)); objects.append(dataobj)
    lib=work/'libhumanengineicu.a'; commands.append(build_archive(ar, objects, lib))
    cflags=' '.join(flags[-2:]+['-DU_STATIC_IMPLEMENTATION=1'])
    ldflags=str(lib)+' -lstdc++ -lm -lpthread'
    (work/'env.sh').write_text('export CGO_ENABLED=1\nexport CGO_CFLAGS='+shlex.quote(cflags)+'\nexport CGO_LDFLAGS='+shlex.quote(ldflags)+'\n')
    report={'node':'v24.22.1','archiveSHA256':ARCHIVE,'sourceManifestSHA256':MANIFEST,'dataSHA256':DATA,
            'compiler':subprocess.check_output([cxx,'--version'],text=True),'archiveTool':subprocess.check_output([ar,'--version'],text=True),
            'commands':commands,'staticArchiveSHA256':sha(lib),'elapsedSeconds':round(time.monotonic()-start,2)}
    (work/'build.json').write_text(json.dumps(report,indent=2)+'\n')
    print(f'Built {lib} in {report["elapsedSeconds"]} seconds. Source {work}/env.sh, then go build/test -tags humanengine_icu.',flush=True)
if __name__=='__main__': main()
