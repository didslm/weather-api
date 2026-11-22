import http.client
import json
from pathlib import Path
import subprocess
import threading
import unittest
from unittest.mock import patch

import weather_dashboard as dashboard

SHA = 'a' * 40
PREVIOUS = 'b' * 40


class Controls(unittest.TestCase):
    def test_unlisted_service_or_shell_action_is_rejected(self):
        for target, action in [('sshd', 'stop'), ('route', 'restart; reboot'), ('ai', 'rollback')]:
            with self.assertRaises(dashboard.ControlError), patch.object(dashboard.EXECUTOR, 'submit') as submit:
                dashboard.start_job(target, action)
            submit.assert_not_called()

    def test_revisions_cannot_inject_unit_names(self):
        with patch.object(dashboard, 'checked', return_value=json.dumps({'current': '../../sshd', 'previous': None})):
            with self.assertRaises(dashboard.ControlError):
                dashboard.deployment(dashboard.APPS['route'])

    def test_only_one_control_can_run_at_a_time(self):
        with patch.object(dashboard, 'JOB', {'state': 'running'}), patch.object(dashboard.EXECUTOR, 'submit') as submit:
            with self.assertRaises(dashboard.ControlError):
                dashboard.start_job('route', 'restart')
            submit.assert_not_called()

    def test_failed_control_is_reported_as_failed(self):
        job = {'id': 'test', 'state': 'running'}
        with patch.object(dashboard, 'JOB', job), patch.object(dashboard, 'control', side_effect=RuntimeError('health failed')):
            dashboard.execute_job('test', 'route', 'restart')
            self.assertEqual(dashboard.JOB['state'], 'failed')
            self.assertEqual(dashboard.JOB['message'], 'health failed')

    def test_stop_stops_timer_proxy_and_both_retained_workers(self):
        data = {'current': SHA, 'previous': PREVIOUS}
        with patch.object(dashboard, 'deployment', return_value=data), patch.object(dashboard, 'states', return_value={'trail-update.timer': {'ActiveState': 'active'}}), patch.object(dashboard, 'require_idle'), patch.object(dashboard, 'checked') as checked:
            dashboard.control('route', 'stop')
            calls = [c.args for c in checked.call_args_list]
            self.assertEqual(calls[0], ('systemctl', 'stop', 'trail-update.timer'))
            self.assertEqual(calls[1][:3], ('systemctl', 'stop', 'trail-proxy.service'))
            self.assertEqual(set(calls[1][3:]), {f'trail-release@{SHA}.service', f'trail-release@{PREVIOUS}.service'})

    def test_stop_during_deploy_restores_timer_and_leaves_api_running(self):
        with patch.object(dashboard, 'deployment', return_value={'current': SHA}), patch.object(dashboard, 'states', return_value={'trail-update.timer': {'ActiveState': 'active'}}), patch.object(dashboard, 'require_idle', side_effect=dashboard.ControlError('deployment running')), patch.object(dashboard, 'checked') as checked:
            with self.assertRaisesRegex(dashboard.ControlError, 'deployment running'):
                dashboard.control('route', 'stop')
            self.assertEqual([c.args for c in checked.call_args_list], [('systemctl', 'stop', 'trail-update.timer'), ('systemctl', 'start', 'trail-update.timer')])

    def test_activating_deployment_counts_as_busy(self):
        with patch.object(dashboard, 'states', return_value={'trail-update.service': {'ActiveState': 'activating'}}):
            with self.assertRaises(dashboard.ControlError):
                dashboard.require_idle(dashboard.APPS['route'])

    def test_rollback_requires_a_successful_previous_release(self):
        with patch.object(dashboard, 'deployment', return_value={'previous_successful': False}), patch.object(dashboard, 'checked') as checked:
            with self.assertRaises(dashboard.ControlError):
                dashboard.control('route', 'rollback')
            checked.assert_not_called()

    def test_restart_requires_health_before_success(self):
        with patch.object(dashboard, 'deployment', return_value={'current': SHA}), patch.object(dashboard, 'require_idle'), patch.object(dashboard, 'checked'), patch.object(dashboard, 'wait_healthy', side_effect=dashboard.ControlError('not ready')):
            with self.assertRaisesRegex(dashboard.ControlError, 'not ready'):
                dashboard.control('weather', 'restart')

    def test_log_target_is_allowlisted(self):
        with patch.object(dashboard, 'checked') as checked:
            with self.assertRaises(dashboard.ControlError):
                dashboard.logs('../../ssh')
            checked.assert_not_called()

    def test_one_broken_manager_does_not_hide_other_services(self):
        def manager(app):
            if app['prefix'] == 'trail':
                raise dashboard.ControlError('route manager failed')
            return {'current': SHA, 'previous': None, 'paused': False, 'releases': [SHA]}
        def state(units):
            return {unit: {'ActiveState': 'inactive' if unit.endswith('-update.service') else 'active', 'SubState': 'running', 'MemoryCurrent': '100'} for unit in units}
        with patch.object(dashboard, 'CACHE', {'at': 0, 'value': None}), patch.object(dashboard, 'deployment', side_effect=manager), patch.object(dashboard, 'states', side_effect=state), patch.object(dashboard, 'probe', return_value=True), patch.object(dashboard, 'device', return_value={}):
            result = dashboard.snapshot()
        self.assertEqual(len(result['apps']), 6)
        self.assertEqual(result['apps'][0]['error'], 'route manager failed')
        self.assertTrue(next(a for a in result['apps'] if a['id'] == 'weather')['healthy'])


class HttpGuards(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = dashboard.ThreadingHTTPServer(('127.0.0.1', 0), dashboard.Handler)
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()
        cls.port = cls.server.server_port

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()

    def request(self, method, path, body=None, headers=None):
        connection = http.client.HTTPConnection('127.0.0.1', self.port, timeout=5)
        connection.request(method, path, body, headers or {})
        response = connection.getresponse()
        status, body = response.status, response.read()
        connection.close()
        return status, body

    def test_dns_rebinding_host_is_rejected_for_reads(self):
        status, _ = self.request('GET', '/api/status', headers={'Host': 'evil.example'})
        self.assertEqual(status, 403)

    def test_cross_site_and_missing_token_cannot_change_services(self):
        for headers in ({'Origin': 'https://evil.example', 'X-Phone-Control': dashboard.TOKEN}, {'Origin': f'http://127.0.0.1:{self.port}'}):
            with patch.object(dashboard, 'start_job') as job:
                status, _ = self.request('POST', '/api/action', '{}', {'Content-Type': 'application/json', **headers})
                self.assertEqual(status, 403)
                job.assert_not_called()

    def test_valid_local_json_reaches_only_validated_action(self):
        headers = {'Origin': f'http://127.0.0.1:{self.port}', 'X-Phone-Control': dashboard.TOKEN, 'Content-Type': 'application/json'}
        with patch.object(dashboard, 'start_job', return_value={'id': 'example'}) as job:
            status, _ = self.request('POST', '/api/action', json.dumps({'service': 'route', 'action': 'pause'}), headers)
            self.assertEqual(status, 202)
            job.assert_called_once_with('route', 'pause')

    def test_oversized_and_wrongly_typed_bodies_do_not_schedule_actions(self):
        headers = {'Origin': f'http://127.0.0.1:{self.port}', 'X-Phone-Control': dashboard.TOKEN, 'Content-Type': 'application/json'}
        for body in ('x' * 2049, '[1,2,3]', '{"service":[],"action":"stop"}'):
            with patch.object(dashboard, 'start_job') as job:
                status, _ = self.request('POST', '/api/action', body, headers)
                self.assertEqual(status, 409)
                job.assert_not_called()

    def test_page_contains_server_token_and_no_external_dependencies(self):
        status, body = self.request('GET', '/')
        self.assertEqual(status, 200)
        self.assertIn(dashboard.TOKEN.encode(), body)
        self.assertNotIn(b'__CONTROL_TOKEN__', body)
        self.assertNotIn(b'<script src=', body)


if __name__ == '__main__':
    unittest.main()
