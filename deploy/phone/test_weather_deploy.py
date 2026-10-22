import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from weather_deploy import Manager, atomic_text, verify_artifact


def artifact(sha):
    data = b'\x7fELF' + b'\x00' * 14 + b'\xb7\x00' + sha.encode()
    return data, {'revision': sha, 'sha256': hashlib.sha256(data).hexdigest(), 'size': len(data)}


class FakeManager(Manager):
    def __init__(self, base):
        super().__init__(base, base / 'missing-config')
        self.started = set()
        self.events = []
        self.fail_candidate = False
        self.fail_public = False
        self.fail_reload = False

    def unit(self, action, sha):
        self.events.append((action, sha))
        if action == 'start':
            self.started.add(sha)
        elif action == 'stop':
            self.started.discard(sha)

    def allocate_port(self):
        return 18082 + len(list(self.releases.glob('*/port')))

    def reload_proxy(self):
        self.events.append(('reload', self.selected('current')))
        if self.fail_reload:
            self.fail_reload = False
            raise RuntimeError('bad nginx config')

    def ready(self, sha):
        if self.fail_candidate:
            raise RuntimeError('candidate forecast failed')

    def check_promoted(self, sha):
        if self.fail_public:
            raise RuntimeError('public route served wrong revision')

    def probe(self, *args, **kwargs):
        pass


class DeploymentTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.manager = FakeManager(Path(self.temp.name))

    def stage(self, char):
        sha = char * 40
        self.manager.stage(sha, *artifact(sha))
        return sha

    def test_corrupt_artifact_is_rejected_before_installation(self):
        sha = 'a' * 40
        data, manifest = artifact(sha)
        with self.assertRaisesRegex(ValueError, 'checksum'):
            verify_artifact(sha, data[:-1] + b'x', manifest)
        self.assertEqual(list(self.manager.releases.iterdir()), [])

    def test_candidate_failure_preserves_current_and_previous(self):
        first = self.stage('a')
        self.manager.promote(first)
        second = self.stage('b')
        self.manager.fail_candidate = True
        with self.assertRaises(RuntimeError):
            self.manager.promote(second)
        self.assertEqual(self.manager.selected('current'), first)
        self.assertIsNone(self.manager.selected('previous'))
        self.assertNotIn(second, self.manager.started)
        self.assertFalse((self.manager.releases / second / 'success').exists())

    def test_public_failure_rolls_back_without_stopping_old_process(self):
        first = self.stage('a')
        self.manager.promote(first)
        second = self.stage('b')
        self.manager.fail_public = True
        with self.assertRaises(RuntimeError):
            self.manager.promote(second)
        self.assertEqual(self.manager.selected('current'), first)
        self.assertIn(first, self.manager.started)
        self.assertNotIn(second, self.manager.started)
        self.assertFalse((self.manager.base / 'pending.json').exists())
        self.assertEqual([e for e in self.manager.events if e[0] == 'reload'][-2:],
                         [('reload', second), ('reload', first)])

    def test_nginx_reload_failure_restores_previous_route(self):
        first = self.stage('a')
        self.manager.promote(first)
        second = self.stage('b')
        self.manager.fail_reload = True
        with self.assertRaises(RuntimeError):
            self.manager.promote(second)
        self.assertEqual(self.manager.selected('current'), first)
        self.assertIn(first, self.manager.started)

    def test_retains_five_releases_and_two_running_processes(self):
        shas = []
        for char in 'abcdef7':
            sha = self.stage(char)
            self.manager.promote(sha)
            shas.append(sha)
        self.assertEqual(set(self.manager.status()['releases']), set(shas[-5:]))
        self.assertEqual(self.manager.started, set(shas[-2:]))
        self.assertEqual(self.manager.selected('previous'), shas[-2])

    def test_manual_rollback_pauses_auto_promotion(self):
        first = self.stage('a')
        self.manager.promote(first)
        second = self.stage('b')
        self.manager.promote(second)
        self.manager.rollback()
        self.assertEqual(self.manager.selected('current'), first)
        self.assertEqual(self.manager.selected('previous'), second)
        self.assertTrue(self.manager.status()['paused'])
        with patch('subprocess.check_output', side_effect=AssertionError('must not fetch')):
            self.manager.update()

    def test_interrupted_promotion_recovers_last_working_release(self):
        first = self.stage('a')
        self.manager.promote(first)
        second = self.stage('b')
        atomic_text(self.manager.base / 'pending.json', json.dumps({
            'current': first, 'previous': None, 'candidate': second}))
        self.manager.select('current', second)
        self.manager.recover()
        self.assertEqual(self.manager.selected('current'), first)
        self.assertIn(first, self.manager.started)
        self.assertFalse(self.manager.status()['pending'])

    def test_release_cannot_be_overwritten(self):
        sha = self.stage('a')
        binary, manifest = artifact(sha)
        binary += b'extra'
        manifest.update(size=len(binary), sha256=hashlib.sha256(binary).hexdigest())
        with self.assertRaisesRegex(ValueError, 'immutable'):
            self.manager.stage(sha, binary, manifest)


if __name__ == '__main__':
    unittest.main()
