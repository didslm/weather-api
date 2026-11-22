#!/usr/bin/env python3
"""Local phone service panel with fixed service targets and serialized controls."""
from concurrent.futures import ThreadPoolExecutor
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import subprocess
import threading
import time
from urllib.parse import parse_qs, urlsplit
from urllib.request import urlopen

HOST, PORT = '127.0.0.1', int(os.environ.get('PHONE_SERVICES_PORT', '8084'))
TOKEN = secrets.token_urlsafe(32)
PAGE_PATH = Path(os.environ.get('PHONE_SERVICES_PAGE', str(Path(__file__).with_name('phone_services.html'))))
SHA = re.compile(r'^[0-9a-f]{40}$')
APPS = {
    'route': {'name': 'Route planner', 'description': 'Trail network & hiking routes', 'kind': 'deployment', 'prefix': 'trail', 'manager': '/usr/local/sbin/trail-deploy', 'proxy': 'trail-proxy.service', 'timer': 'trail-update.timer', 'updater': 'trail-update.service', 'health': 'http://127.0.0.1:2998/health', 'url': 'https://trail-router.shtegtimi.com', 'units': ['trail-proxy.service', 'trail-update.timer', 'trail-update.service']},
    'weather': {'name': 'Weather API', 'description': 'Mountain forecasts & risk reports', 'kind': 'deployment', 'prefix': 'weather', 'manager': '/usr/local/sbin/weather-deploy', 'proxy': 'weather-proxy.service', 'timer': 'weather-update.timer', 'updater': 'weather-update.service', 'health': 'http://127.0.0.1:8083/health', 'url': 'https://weather-api.shtegtimi.com', 'units': ['weather-proxy.service', 'weather-update.timer', 'weather-update.service']},
    'ai': {'name': 'AI server', 'description': 'Qwen 2.5 · 7B local model', 'kind': 'service', 'health': 'http://127.0.0.1:8080/health', 'url': 'http://127.0.0.1:8080', 'units': ['llama-server.service']},
    'test-site': {'name': 'Shtegtimi test', 'description': 'Demo website · separate from production', 'kind': 'service', 'health': 'http://127.0.0.1:8081/health', 'url': 'http://127.0.0.1:8081', 'units': ['shtegtimi-test-php.service', 'shtegtimi-test-web.service', 'shtegtimi-test-queue.service', 'shtegtimi-test-scheduler.timer', 'shtegtimi-test-tunnel.service']},
    'public-tunnel': {'name': 'Public API tunnel', 'description': 'Shared by route planner & weather', 'kind': 'connection', 'units': ['weather-api-tunnel.service']},
    'ai-tunnel': {'name': 'AI tunnel', 'description': 'Public connection to the AI server', 'kind': 'connection', 'units': ['cloudflared-quick.service']},
}
ACTIONS = {'start', 'stop', 'restart', 'check', 'pause', 'resume', 'rollback'}
EXECUTOR = ThreadPoolExecutor(max_workers=1)
JOB_LOCK, CACHE_LOCK = threading.Lock(), threading.Lock()
JOB = None
CACHE = {'at': 0, 'value': None}


class ControlError(RuntimeError):
    pass


def run(*args, timeout=8):
    return subprocess.run(args, capture_output=True, text=True, timeout=timeout, check=False)


def checked(*args, timeout=20):
    result = run(*args, timeout=timeout)
    if result.returncode:
        raise ControlError((result.stderr or result.stdout).strip()[-1200:] or 'Service command failed')
    return result.stdout


def states(units):
    result = checked('systemctl', 'show', '--no-pager', '--property=Id,ActiveState,SubState,MemoryCurrent,NRestarts', *sorted(set(units)))
    output = {}
    for block in result.strip().split('\n\n'):
        values = dict(line.split('=', 1) for line in block.splitlines() if '=' in line)
        if values.get('Id'):
            output[values['Id']] = values
    return output


def is_running(state):
    return state.get('ActiveState') in ('active', 'reloading')


def deployment(app):
    raw = checked(app['manager'], 'status')
    data = json.loads(raw)
    for key in ('current', 'previous'):
        if data.get(key) is not None and not SHA.fullmatch(data[key]):
            raise ControlError('Invalid deployment revision')
    data['previous_successful'] = bool(data.get('previous')) and data['previous'] in data.get('releases', [])
    return data


def release_units(app, data):
    return [app['prefix'] + '-release@' + sha + '.service' for sha in {data.get('current'), data.get('previous')} if sha]


def probe(url):
    try:
        with urlopen(url, timeout=2) as response:
            payload = json.load(response)
            return response.status == 200 and payload.get('status') in ('ok', 'healthy')
    except Exception:
        return False


def device():
    info = {}
    try:
        mem = {line.split(':', 1)[0]: int(line.split()[1]) * 1024 for line in Path('/proc/meminfo').read_text().splitlines()}
        info.update(memory_total=mem['MemTotal'], memory_used=mem['MemTotal'] - mem['MemAvailable'])
        info['uptime_seconds'] = int(float(Path('/proc/uptime').read_text().split()[0]))
        info['load'] = round(os.getloadavg()[0], 2)
        info['cpu_count'] = os.cpu_count()
        disk = shutil.disk_usage('/')
        info.update(disk_total=disk.total, disk_free=disk.free)
        battery = Path('/sys/class/power_supply/qcom-battery')
        if battery.exists():
            info['battery'] = int((battery / 'capacity').read_text())
            info['charging'] = (battery / 'status').read_text().strip()
    except (OSError, ValueError, KeyError):
        pass
    return info


def snapshot():
    with CACHE_LOCK:
        if CACHE['value'] is not None and time.monotonic() - CACHE['at'] < 3:
            result = dict(CACHE['value'])
        else:
            deployments, errors, units = {}, {}, []
            for key, app in APPS.items():
                units.extend(app['units'])
                if app['kind'] == 'deployment':
                    try:
                        deployments[key] = deployment(app)
                        units.extend(release_units(app, deployments[key]))
                    except (OSError, ControlError, ValueError, subprocess.TimeoutExpired) as exc:
                        errors[key] = str(exc)
            all_states = states(units)
            apps = []
            for key, app in APPS.items():
                data = deployments.get(key, {})
                display_units = app['units'] + release_units(app, data) if app['kind'] == 'deployment' else app['units']
                component_states = [{'unit': unit, 'state': all_states.get(unit, {}).get('ActiveState', 'unknown'), 'detail': all_states.get(unit, {}).get('SubState', 'unknown')} for unit in display_units]
                if app['kind'] == 'deployment':
                    live = app['prefix'] + '-release@' + data['current'] + '.service' if data.get('current') else ''
                    running = is_running(all_states.get(app['proxy'], {})) and is_running(all_states.get(live, {}))
                    busy = all_states.get(app['updater'], {}).get('ActiveState') not in ('inactive', 'failed', None)
                    timer_active = is_running(all_states.get(app['timer'], {}))
                else:
                    running = all(is_running(all_states.get(unit, {})) for unit in app['units'])
                    busy, timer_active = False, False
                runtime_units = [app['proxy'], live] if app['kind'] == 'deployment' else app['units']
                partly_running = any(is_running(all_states.get(unit, {})) for unit in runtime_units)
                can_stop = any(is_running(all_states.get(unit, {})) for unit in display_units)
                healthy = probe(app['health']) if running and app.get('health') else running
                memory = sum(int(all_states.get(unit, {}).get('MemoryCurrent', 0)) for unit in display_units if str(all_states.get(unit, {}).get('MemoryCurrent', '')).isdigit())
                apps.append({'id': key, 'name': app['name'], 'description': app['description'], 'kind': app['kind'], 'url': app.get('url'), 'running': running, 'can_stop': can_stop, 'healthy': healthy, 'status': 'deploying' if busy else ('healthy' if healthy else ('attention' if running or partly_running or key in errors else 'stopped')), 'deploying': busy, 'memory': memory, 'components': component_states, 'deployment': data if app['kind'] == 'deployment' else None, 'auto_active': timer_active and not data.get('paused', False), 'error': errors.get(key)})
            result = {'device': device(), 'apps': apps, 'updated_at': time.time()}
            CACHE.update(at=time.monotonic(), value=result)
    with JOB_LOCK:
        result['job'] = dict(JOB) if JOB else None
    return result


def require_idle(app):
    state = states([app['updater']]).get(app['updater'], {})
    if state.get('ActiveState') not in ('inactive', 'failed', None):
        raise ControlError('A deployment is running. Wait until it finishes, then try again.')


def wait_healthy(app, seconds=60):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if probe(app['health']):
            return
        time.sleep(1)
    raise ControlError('The process started but its health check is still failing. Check its logs.')


def control(target, action):
    app = APPS[target]
    if app['kind'] == 'deployment':
        data = deployment(app)
        if action in ('pause', 'resume', 'rollback'):
            if action == 'rollback' and not data.get('previous_successful'):
                raise ControlError('No previous successful release is available.')
            if action == 'resume' and not is_running(states([app['proxy']]).get(app['proxy'], {})):
                raise ControlError('Start the API before resuming automatic deployments.')
            checked(app['manager'], action, timeout=240 if action == 'rollback' else 10)
            if action == 'resume':
                checked('systemctl', 'start', app['timer'])
            return 'Previous release restored; automatic deployments are paused.' if action == 'rollback' else ('Automatic deployments paused.' if action == 'pause' else 'Automatic deployments resumed.')
        if action == 'check':
            require_idle(app)
            checked('systemctl', 'start', '--no-block', app['updater'])
            return 'Deployment check started. Follow its progress in the service details.'
        if action == 'stop':
            timer_was_active = is_running(states([app['timer']]).get(app['timer'], {}))
            checked('systemctl', 'stop', app['timer'])
            try:
                require_idle(app)
            except ControlError:
                if timer_was_active:
                    checked('systemctl', 'start', app['timer'])
                raise
            checked('systemctl', 'stop', app['proxy'], *release_units(app, data))
            return 'API stopped. Automatic checks are stopped with it.'
        require_idle(app)
        if action == 'start':
            if not data.get('current'):
                raise ControlError('No release is installed. Run a deployment check first.')
            checked('systemctl', 'start', app['proxy'])
            checked('systemctl', 'start', app['timer'])
        else:
            if not data.get('current'):
                raise ControlError('No release is installed.')
            checked('systemctl', 'restart', app['prefix'] + '-release@' + data['current'] + '.service')
            checked('systemctl', 'start', app['proxy'])
        wait_healthy(app)
    else:
        units = list(reversed(app['units'])) if action == 'stop' else app['units']
        checked('systemctl', action, *units, timeout=90)
        if action != 'stop' and app.get('health'):
            wait_healthy(app, seconds=90)
    return app['name'] + {'start': ' started.', 'stop': ' stopped.', 'restart': ' restarted.'}[action]


def execute_job(job_id, target, action):
    global JOB
    try:
        message = control(target, action)
        state = 'complete'
    except Exception as exc:
        message, state = str(exc), 'failed'
    with JOB_LOCK:
        if JOB and JOB['id'] == job_id:
            JOB.update(state=state, message=message, finished_at=time.time())
    with CACHE_LOCK:
        CACHE['at'] = 0


def start_job(target, action):
    global JOB
    if target not in APPS or action not in ACTIONS:
        raise ControlError('Unknown service or action.')
    if APPS[target]['kind'] != 'deployment' and action not in ('start', 'stop', 'restart'):
        raise ControlError('This service has no deployment controls.')
    with JOB_LOCK:
        if JOB and JOB['state'] == 'running':
            raise ControlError('Another action is still running. Wait for it to finish.')
        job_id = secrets.token_hex(8)
        JOB = {'id': job_id, 'target': target, 'action': action, 'state': 'running', 'message': 'Working…', 'started_at': time.time()}
        result = dict(JOB)
    EXECUTOR.submit(execute_job, job_id, target, action)
    return result


def logs(target):
    if target not in APPS:
        raise ControlError('Unknown service.')
    app = APPS[target]
    units = list(app['units'])
    if app['kind'] == 'deployment':
        units += release_units(app, deployment(app))
    args = ['journalctl', '--no-pager', '--output=short-iso', '-n', '80']
    for unit in units:
        args.extend(['-u', unit])
    return checked(*args)[-24000:]


class Handler(BaseHTTPRequestHandler):
    server_version = 'PhoneServices/2'
    timeout = 10

    def local_host(self):
        return self.headers.get('Host') in (f'127.0.0.1:{self.server.server_port}', f'localhost:{self.server.server_port}')

    def send(self, code, body, content_type='application/json; charset=utf-8'):
        payload = body.encode() if isinstance(body, str) else body
        self.send_response(code)
        self.send_header('Content-Type', content_type)
        self.send_header('Content-Length', str(len(payload)))
        self.send_header('Cache-Control', 'no-store')
        self.send_header('X-Content-Type-Options', 'nosniff')
        self.send_header('Referrer-Policy', 'no-referrer')
        self.send_header('Content-Security-Policy', "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
        self.end_headers()
        self.wfile.write(payload)

    def error(self, code, message):
        self.send(code, json.dumps({'error': message}))

    def do_GET(self):
        if not self.local_host():
            self.error(403, 'Invalid host')
            return
        path = urlsplit(self.path)
        try:
            if path.path == '/':
                self.send(200, PAGE_PATH.read_text().replace('__CONTROL_TOKEN__', TOKEN), 'text/html; charset=utf-8')
            elif path.path == '/api/status':
                self.send(200, json.dumps(snapshot()))
            elif path.path == '/api/logs':
                target = parse_qs(path.query).get('service', [''])[0]
                self.send(200, json.dumps({'logs': logs(target)}))
            else:
                self.error(404, 'Not found')
        except (OSError, ControlError, ValueError, subprocess.TimeoutExpired) as exc:
            self.error(503, str(exc))

    def do_POST(self):
        if urlsplit(self.path).path != '/api/action':
            self.error(404, 'Not found')
            return
        allowed = (f'http://127.0.0.1:{self.server.server_port}', f'http://localhost:{self.server.server_port}')
        if not self.local_host() or self.headers.get('Origin') not in allowed or not secrets.compare_digest(self.headers.get('X-Phone-Control', ''), TOKEN):
            self.error(403, 'Use this local dashboard to control services. Reload the page if needed.')
            return
        try:
            length = int(self.headers.get('Content-Length', '0'))
            if length < 1 or length > 2048 or self.headers.get_content_type() != 'application/json':
                raise ValueError('Expected a small JSON request.')
            body = json.loads(self.rfile.read(length))
            if not isinstance(body, dict) or not isinstance(body.get('service'), str) or not isinstance(body.get('action'), str):
                raise ValueError('Expected a service and action.')
            self.send(202, json.dumps(start_job(body['service'], body['action'])))
        except (ValueError, ControlError) as exc:
            self.error(409, str(exc))

    def log_message(self, format, *args):
        print('%s - %s' % (self.log_date_time_string(), format % args), flush=True)


if __name__ == '__main__':
    server = ThreadingHTTPServer((HOST, PORT), Handler)
    server.daemon_threads = True
    server.serve_forever()
