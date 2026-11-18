#!/usr/bin/env python3
"""Prepare Linux/x64 runtime locally, including native npm dependencies."""
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tarfile

ROOT = Path(__file__).resolve().parents[1]
CACHE = ROOT / '.cache'
PACKAGE = CACHE / 'linux-runtime'
VERSION = json.loads((ROOT / 'runtime/node/package.json').read_text())['dependencies']['node']


def node_archive(system, architecture):
    downloads = CACHE / 'downloads'
    downloads.mkdir(parents=True, exist_ok=True)
    filename = f'node-v{VERSION}-{system}-{architecture}.tar.gz'
    base = f'https://nodejs.org/dist/v{VERSION}/'
    manifest = subprocess.check_output(['curl', '-fsSL', '--retry', '3', '--connect-timeout', '20',
                                        '--max-time', '300', base + 'SHASUMS256.txt'], text=True)
    checksums = dict((line.split()[1], line.split()[0]) for line in manifest.splitlines())
    expected = checksums[filename]
    archive = downloads / filename
    if not archive.exists() or hashlib.sha256(archive.read_bytes()).hexdigest() != expected:
        temporary = archive.with_suffix('.download')
        subprocess.run(['curl', '-fsSL', '--retry', '3', '--connect-timeout', '20', '--max-time', '300',
                        '-o', str(temporary), base + filename], check=True)
        if hashlib.sha256(temporary.read_bytes()).hexdigest() != expected:
            temporary.unlink()
            raise RuntimeError('Node archive checksum mismatch')
        temporary.replace(archive)
    return archive


def main():
    system = {'Darwin': 'darwin', 'Linux': 'linux'}[platform.system()]
    architecture = {'arm64': 'arm64', 'aarch64': 'arm64', 'x86_64': 'x64', 'AMD64': 'x64'}[platform.machine()]
    host = CACHE / f'node-host-v{VERSION}-{system}-{architecture}'
    if not (host / 'bin/node').exists():
        host.mkdir(parents=True, exist_ok=True)
        subprocess.run(['tar', '-xzf', str(node_archive(system, architecture)),
                        '-C', str(host), '--strip-components=1'], check=True)
    node = PACKAGE / 'runtime/node/node_modules/node'
    node_version = PACKAGE / '.node-version'
    if not (node / 'bin/node').exists() or not node_version.exists() or node_version.read_text() != VERSION:
        with tarfile.open(node_archive('linux', 'x64')) as archive:
            prefix = f'node-v{VERSION}-linux-x64/'
            for relative in ('bin/node', 'LICENSE'):
                destination = node / relative
                destination.parent.mkdir(parents=True, exist_ok=True)
                with archive.extractfile(prefix + relative) as source, destination.open('wb') as output:
                    shutil.copyfileobj(source, output)
                destination.chmod(0o755 if relative == 'bin/node' else 0o644)
        node_version.write_text(VERSION)
    runtime = PACKAGE / 'runtime'
    lock = ROOT / 'runtime/package-lock.json'
    fingerprint = hashlib.sha256(lock.read_bytes() + VERSION.encode()).hexdigest()
    marker = PACKAGE / '.dependencies.sha256'
    if not marker.exists() or marker.read_text() != fingerprint or not (runtime / 'node_modules/openclaw/openclaw.mjs').exists():
        for name in ('package.json', 'package-lock.json'):
            shutil.copy2(ROOT / 'runtime' / name, runtime / name)
        for directory in ('npm', 'tmp', 'node'):
            (CACHE / directory).mkdir(parents=True, exist_ok=True)
        env = dict(os.environ, PATH=str(host / 'bin') + os.pathsep + os.environ.get('PATH', ''),
                   NPM_CONFIG_CACHE=str(CACHE / 'npm'), TMPDIR=str(CACHE / 'tmp'),
                   NODE_COMPILE_CACHE=str(CACHE / 'node'))
        subprocess.run([str(host / 'bin/node'), str(host / 'lib/node_modules/npm/bin/npm-cli.js'),
                        'ci', '--prefix', str(runtime), '--os=linux', '--cpu=x64', '--libc=glibc',
                        '--ignore-scripts', '--no-audit', '--no-fund'], env=env, cwd=ROOT, check=True)
        marker.write_text(fingerprint)
    # Copy only the skill sources: generated config and session state stay on the server.
    skills = PACKAGE / 'openclaw/workspace/skills'
    if skills.exists():
        shutil.rmtree(skills)
    shutil.copytree(ROOT / 'openclaw/workspace/skills', skills)
    print('Linux/x64 AI-Assistant runtime prepared:', PACKAGE)


if __name__ == '__main__':
    main()
