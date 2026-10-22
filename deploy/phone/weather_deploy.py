#!/usr/bin/env python3
"""Phone releases: fetch main, test, build, gracefully promote, and roll back.

Installed root-owned outside release directories. Application binaries always run
as ayu through a fixed systemd template; downloaded code is never executed as root.
"""
import argparse
import contextlib
import fcntl
import hashlib
import json
import os
import pwd
from pathlib import Path
import re
import shutil
import socket
import subprocess
import sys
import tempfile
import time
from urllib.request import Request, urlopen

REPOSITORY = 'didslm/weather-api'
REVISION = re.compile(r'^[0-9a-f]{40}$')
MAX_BINARY = 32 * 1024 * 1024
PROXY = 'http://127.0.0.1:8083'


class BuildRejected(RuntimeError):
    pass


class MainChanged(RuntimeError):
    pass


def log(message):
    print(time.strftime('%Y-%m-%dT%H:%M:%S%z'), message, flush=True)


def atomic_text(path, value):
    temporary = path.with_name(path.name + '.new')
    temporary.write_text(value)
    temporary.chmod(0o644)
    with temporary.open('rb') as handle:
        os.fsync(handle.fileno())
    os.replace(temporary, path)
    descriptor = os.open(path.parent, os.O_RDONLY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def revision(value):
    if not REVISION.fullmatch(value):
        raise ValueError('Expected a full lowercase 40-character Git commit SHA')
    return value


def verify_artifact(sha, binary, manifest):
    if manifest.get('revision') != revision(sha):
        raise ValueError('Manifest revision mismatch')
    if not binary or len(binary) > MAX_BINARY or len(binary) != manifest.get('size'):
        raise ValueError('Binary size mismatch')
    if hashlib.sha256(binary).hexdigest() != manifest.get('sha256'):
        raise ValueError('Binary checksum mismatch')
    if binary[:4] != b'\x7fELF' or len(binary) < 20 or binary[18:20] != b'\xb7\x00':
        raise ValueError('Expected a Linux ARM64 executable')


class Manager:
    def __init__(self, base=Path('/opt/weather-risk-api'), config=Path('/etc/weather-deploy.json')):
        self.base = base
        self.releases = base / 'releases'
        self.releases.mkdir(parents=True, exist_ok=True)
        self.public_url = ''
        self.soak_seconds = 20
        if config.exists():
            settings = json.loads(config.read_text())
            self.public_url = settings.get('public_url', '').rstrip('/')
            self.soak_seconds = int(settings.get('soak_seconds', 20))

    def selected(self, name):
        path = self.base / name
        if not path.is_symlink():
            return None
        target = path.resolve()
        if target.parent != self.releases.resolve():
            raise ValueError('Release symlink leaves the release directory')
        return revision(target.name)

    def select(self, name, sha):
        link = self.base / name
        if sha is None:
            link.unlink(missing_ok=True)
            return
        revision(sha)
        temporary = self.base / (name + '.new')
        temporary.unlink(missing_ok=True)
        temporary.symlink_to('releases/' + sha)
        os.replace(temporary, link)
        descriptor = os.open(self.base, os.O_RDONLY)
        try:
            os.fsync(descriptor)
        finally:
            os.close(descriptor)

    def unit(self, action, sha):
        subprocess.run(['systemctl', action, 'weather-release@' + revision(sha) + '.service'], check=True)

    def reload_proxy(self):
        active = subprocess.run(['systemctl', 'is-active', '--quiet', 'weather-proxy.service']).returncode == 0
        # The unit validates as its own unprivileged user before HUP/start. A
        # failed validation preserves the running workers and their old config.
        subprocess.run(['systemctl', 'reload' if active else 'start', 'weather-proxy.service'], check=True)

    def port(self, sha):
        return int((self.releases / revision(sha) / 'port').read_text())

    def allocate_port(self):
        reserved = {int(p.read_text()) for p in self.releases.glob('*/port')}
        for port in range(18082, 18146):
            if port in reserved:
                continue
            with socket.socket() as sock:
                try:
                    sock.bind(('127.0.0.1', port))
                    return port
                except OSError:
                    pass
        raise RuntimeError('No free candidate port')

    def stage(self, sha, binary, manifest):
        verify_artifact(sha, binary, manifest)
        release = self.releases / sha
        if release.exists():
            if (release / 'manifest.json').read_text() != json.dumps(manifest, sort_keys=True):
                raise ValueError('An immutable release already exists with different contents')
            if hashlib.sha256((release / 'server').read_bytes()).hexdigest() != manifest['sha256']:
                raise ValueError('Existing release binary checksum mismatch')
            return
        port = self.allocate_port()
        temporary = self.releases / ('.incoming-' + sha)
        if temporary.exists():
            shutil.rmtree(temporary)
        temporary.mkdir(mode=0o755)
        (temporary / 'server').write_bytes(binary)
        (temporary / 'server').chmod(0o755)
        (temporary / 'manifest.json').write_text(json.dumps(manifest, sort_keys=True))
        (temporary / 'port').write_text(str(port))
        (temporary / 'upstream.conf').write_text(f'proxy_pass http://127.0.0.1:{port};\n')
        (temporary / 'service.env').write_text(
            f'PORT={port}\nLISTEN_HOST=127.0.0.1\n'
            f'VERIFICATION_LOG_PATH=/var/lib/weather-risk-api/{sha}.jsonl\n')
        for path in temporary.iterdir():
            if path.name != 'server':
                path.chmod(0o644)
            with path.open('rb') as handle:
                os.fsync(handle.fileno())
        descriptor = os.open(temporary, os.O_RDONLY)
        try:
            os.fsync(descriptor)
        finally:
            os.close(descriptor)
        os.replace(temporary, release)
        log(f'Staged {sha} on localhost:{port}')

    def build(self, sha):
        """Fetch, test, and compile main on the phone as the unprivileged user."""
        sha = revision(sha)
        existing = self.releases / sha
        if existing.exists() and (existing / 'success').exists():
            log(f'Reusing successful local build {sha}')
            return
        account = pwd.getpwnam('ayu')
        cache = Path('/var/cache/weather-risk-api')
        cache.mkdir(parents=True, exist_ok=True)
        os.chown(cache, account.pw_uid, account.pw_gid)
        workspace = Path(tempfile.mkdtemp(prefix='build-', dir=cache))
        os.chown(workspace, account.pw_uid, account.pw_gid)
        source = workspace / 'source'
        prefix = ['runuser', '-u', 'ayu', '--', 'env', f'HOME={cache}',
                  f'GOCACHE={cache}/go-build', f'GOMODCACHE={cache}/go-mod',
                  'GOTOOLCHAIN=local', 'GOMAXPROCS=2', 'CGO_ENABLED=0']
        try:
            log(f'Fetching main into {source}')
            subprocess.run(prefix + ['git', 'clone', '--depth=1', '--branch=main',
                                    f'https://github.com/{REPOSITORY}.git', str(source)],
                           check=True, timeout=120)
            actual = subprocess.check_output(prefix + ['git', 'rev-parse', 'HEAD'],
                                             cwd=source, text=True, timeout=15).strip()
            if actual != sha:
                raise MainChanged('Main changed while cloning; checking again next minute')
            try:
                for command in (['go', 'test', './...'], ['go', 'vet', './...'],
                                ['python3', '-m', 'unittest', 'discover', '-s', 'deploy/phone', '-p', 'test_*.py', '-v'],
                                ['go', 'build', '-trimpath', '-ldflags=-s -w -X main.buildRevision=' + sha,
                                 '-o', str(workspace / 'server'), '.']):
                    log('Running on phone: ' + ' '.join(command))
                    subprocess.run(prefix + command, cwd=source, check=True, timeout=900)
            except (subprocess.CalledProcessError, subprocess.TimeoutExpired) as exc:
                raise BuildRejected(f'Phone tests/build rejected {sha}: {exc}') from exc
            binary = (workspace / 'server').read_bytes()
            manifest = {'revision': sha, 'sha256': hashlib.sha256(binary).hexdigest(), 'size': len(binary)}
            self.stage(sha, binary, manifest)
            archive = self.releases / sha / 'source.tar.gz'
            with archive.open('wb') as output:
                subprocess.run(prefix + ['git', 'archive', '--format=tar.gz', 'HEAD'],
                               cwd=source, stdout=output, check=True, timeout=30)
        finally:
            shutil.rmtree(workspace)

    def probe(self, url, sha, forecast=False):
        headers = {'Cache-Control': 'no-cache', 'Connection': 'close'}
        for endpoint in ('/health', '/version'):
            request = Request(url + endpoint, headers=headers)
            with urlopen(request, timeout=10) as response:
                body = json.load(response)
                if response.headers.get('X-Release') != sha:
                    raise ValueError(f'{url}: wrong X-Release header')
            if endpoint == '/health' and body.get('status') != 'ok':
                raise ValueError(f'{url}: health check failed')
            if endpoint == '/version' and body.get('revision') != sha:
                raise ValueError(f'{url}: wrong revision')
        if forecast:
            request = Request(url + '/forecast/simple?lat=42.6&lon=20.1&elevation=2500', headers=headers)
            with urlopen(request, timeout=30) as response:
                body = json.load(response)
                if response.headers.get('X-Release') != sha:
                    raise ValueError('Forecast served by the wrong release')
            days = body.get('days', [])
            if body.get('mode') != 'live_forecast' or not body.get('sources') or not days:
                raise ValueError('Forecast smoke test returned no live forecast')
            for day in days:
                if day.get('risk') not in ('low', 'moderate', 'high') or not day.get('date'):
                    raise ValueError('Forecast smoke test returned invalid daily data')
                for field in ('temp_min_c', 'temp_max_c', 'wind_max_kmh'):
                    if not isinstance(day.get(field), (int, float)):
                        raise ValueError('Forecast smoke test returned invalid measurements')

    def ready(self, sha):
        url = f'http://127.0.0.1:{self.port(sha)}'
        for attempt in range(30):
            try:
                self.probe(url, sha)
                break
            except Exception:
                if attempt == 29:
                    raise
                time.sleep(0.5)
        self.probe(url, sha, forecast=True)

    def check_promoted(self, sha):
        # HUP is asynchronous; allow the new workers to take over before smoke tests.
        for attempt in range(20):
            try:
                self.probe(PROXY, sha)
                if self.public_url:
                    self.probe(self.public_url, sha)
                break
            except Exception:
                if attempt == 19:
                    raise
                time.sleep(0.5)
        self.probe(PROXY, sha, forecast=True)
        if self.public_url:
            self.probe(self.public_url, sha, forecast=True)
        deadline = time.monotonic() + self.soak_seconds
        while time.monotonic() < deadline:
            time.sleep(min(2, max(0, deadline - time.monotonic())))
            self.probe(PROXY, sha)
            if self.public_url:
                self.probe(self.public_url, sha)

    def promote(self, sha):
        sha = revision(sha)
        old = self.selected('current')
        old_previous = self.selected('previous')
        if sha == old:
            if not (self.releases / sha / 'success').exists():
                # An initial bootstrap may have been interrupted before its
                # first successful proxy check. Finish it without inventing a
                # previous release or skipping the verification gates.
                self.unit('start', sha)
                self.ready(sha)
                self.reload_proxy()
                self.check_promoted(sha)
                atomic_text(self.releases / sha / 'success', str(time.time()))
                (self.base / 'pending.json').unlink(missing_ok=True)
            log(f'Already serving {sha}')
            return
        self.unit('start', sha)
        changed = False
        pending = self.base / 'pending.json'
        try:
            self.ready(sha)
            atomic_text(pending, json.dumps({'current': old, 'previous': old_previous, 'candidate': sha}))
            self.select('current', sha)
            changed = True
            self.reload_proxy()
            self.check_promoted(sha)
            self.select('previous', old)
            atomic_text(self.releases / sha / 'success', str(time.time()))
            pending.unlink(missing_ok=True)
        except Exception:
            if changed and old:
                log(f'Promotion failed; restoring {old}')
                self.select('current', old)
                self.select('previous', old_previous)
                # Keep both processes and journal intact if rollback itself fails.
                self.reload_proxy()
                for attempt in range(20):
                    try:
                        self.probe(PROXY, old)
                        break
                    except Exception:
                        if attempt == 19:
                            raise
                        time.sleep(0.5)
            elif changed:
                log('Initial activation failed; keeping candidate for inspection')
                raise
            pending.unlink(missing_ok=True)
            self.unit('stop', sha)
            raise
        log(f'Promoted {sha}; previous={old}')
        try:
            self.cleanup()
        except Exception as exc:
            # Cleanup must never reclassify a verified promotion as failed.
            log(f'Release is healthy; retired-release cleanup needs attention: {exc}')

    def cleanup(self):
        protected = {self.selected('current'), self.selected('previous')}
        successful = sorted((p for p in self.releases.iterdir()
                             if REVISION.fullmatch(p.name) and (p / 'success').exists()),
                            key=lambda p: float((p / 'success').read_text()), reverse=True)
        keep = protected - {None}
        for path in successful:
            if len(keep) < 5:
                keep.add(path.name)
        for path in self.releases.iterdir():
            if not REVISION.fullmatch(path.name):
                continue
            if path.name not in protected:
                # SIGTERM drains any requests still held by old Nginx workers.
                self.unit('stop', path.name)
            if path.name not in keep:
                shutil.rmtree(path)
                log(f'Removed retired release {path.name}')

    def recover(self):
        pending = self.base / 'pending.json'
        if pending.exists():
            state = json.loads(pending.read_text())
            if state['current']:
                self.select('current', state['current'])
                self.select('previous', state['previous'])
                log('Recovered interrupted deployment to the last working release')
            else:
                log('Recovering initial activation')
            pending.unlink()
        for sha in {self.selected('current'), self.selected('previous')} - {None}:
            self.unit('start', sha)
            self.probe_ready_only(sha)

    def probe_ready_only(self, sha):
        for attempt in range(30):
            try:
                self.probe(f'http://127.0.0.1:{self.port(sha)}', sha)
                return
            except Exception:
                if attempt == 29:
                    raise
                time.sleep(0.5)

    def update(self):
        if (self.base / 'paused').exists():
            log('Automatic promotion is paused (weather-deploy resume to continue)')
            return
        output = subprocess.check_output(
            ['git', 'ls-remote', 'https://github.com/' + REPOSITORY + '.git', 'refs/heads/main'],
            text=True, timeout=30)
        sha = revision(output.split()[0])
        failed = self.base / 'failed-revision'
        if sha == self.selected('current') or (failed.exists() and failed.read_text().strip() == sha):
            return
        try:
            self.build(sha)
        except MainChanged as exc:
            log(str(exc))
            return
        except BuildRejected:
            atomic_text(failed, sha + '\n')
            raise
        # Re-check after building so an obsolete build cannot overtake a newer push.
        latest = subprocess.check_output(
            ['git', 'ls-remote', 'https://github.com/' + REPOSITORY + '.git', 'refs/heads/main'],
            text=True, timeout=30).split()[0]
        if latest != sha:
            log('Main changed during build; deferring to the next check')
            return
        try:
            self.promote(sha)
        except Exception:
            atomic_text(failed, sha + '\n')
            raise
        failed.unlink(missing_ok=True)

    def rollback(self, sha=None):
        sha = revision(sha or self.selected('previous') or '')
        if not (self.releases / sha / 'success').exists():
            raise ValueError('Rollback requires a retained successful release')
        # Otherwise the next poll would immediately deploy main again.
        atomic_text(self.base / 'paused', 'Manual rollback; resume explicitly\n')
        self.promote(sha)

    def status(self):
        successful = sorted(p.name for p in self.releases.iterdir()
                            if REVISION.fullmatch(p.name) and (p / 'success').exists())
        return {'current': self.selected('current'), 'previous': self.selected('previous'),
                'paused': (self.base / 'paused').exists(), 'releases': successful,
                'pending': (self.base / 'pending.json').exists(), 'public_url': self.public_url}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['update', 'deploy', 'install', 'rollback', 'recover', 'status', 'pause', 'resume'])
    parser.add_argument('revision', nargs='?')
    parser.add_argument('binary', nargs='?')
    parser.add_argument('manifest', nargs='?')
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error('Run with sudo')
    os.umask(0o022)
    with contextlib.ExitStack() as stack:
        # recover is also run by the proxy ExecStartPre during bootstrap while
        # its parent deployment already holds the lock. It only restores state.
        if args.command not in ('recover', 'status'):
            lock = stack.enter_context(open('/run/lock/weather-deploy.lock', 'a'))
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        manager = Manager()
        if args.command == 'install':
            manager.stage(revision(args.revision or ''), Path(args.binary).read_bytes(),
                          json.loads(Path(args.manifest).read_text()))
            manager.promote(args.revision)
        elif args.command == 'deploy':
            manager.build(revision(args.revision or ''))
            manager.promote(args.revision)
        elif args.command == 'rollback':
            manager.rollback(args.revision)
        elif args.command == 'status':
            print(json.dumps(manager.status(), indent=2))
        elif args.command == 'pause':
            atomic_text(manager.base / 'paused', 'Paused by operator\n')
        elif args.command == 'resume':
            (manager.base / 'paused').unlink(missing_ok=True)
            (manager.base / 'failed-revision').unlink(missing_ok=True)
            log('Automatic promotion resumed')
        else:
            getattr(manager, args.command)()


if __name__ == '__main__':
    try:
        main()
    except Exception as exc:
        log(f'ERROR {exc}')
        sys.exit(1)
