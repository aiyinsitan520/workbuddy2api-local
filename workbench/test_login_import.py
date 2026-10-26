import json, sys, tempfile, unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent
sys.path.insert(0, str(ROOT / 'compat'))
sys.path.insert(0, str(HERE))  # workbench first: compat 里也有 server.py，不能遮蔽被测模块

import server
import core.workbuddy_atrest_crypto as crypto


def completed(stdout=''):
    return SimpleNamespace(returncode=0, stdout=stdout, stderr='')


class LoginLinkTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        (self.root / 'data').mkdir()
        self.addCleanup(self.temp.cleanup)
        server.FLOW = None
        self.patches = [patch.object(server, 'ROOT', self.root)]
        for item in self.patches: item.start()
        self.addCleanup(lambda: [item.stop() for item in self.patches])

    def state_file(self, state='upstream-state', realm='cn'):
        (self.root / 'data' / 'wb2api-login-state.json').write_text(
            json.dumps({'state': state, 'realm': realm}), encoding='utf-8')

    def run_url(self, url):
        return patch.object(server.subprocess, 'run', return_value=completed(url + '\n'))

    def test_cn_copilot_link_accepted(self):
        url = 'https://copilot.tencent.com/login?platform=CLI&state=upstream-state'
        self.state_file()
        with self.run_url(url):
            result = server.start_login('cn')
        self.assertEqual(result['state'], 'pending')
        self.assertEqual(server.FLOW['realm'], 'cn')
        self.assertIn('copilot.tencent.com/login', result['url'])

    def test_cn_rejects_foreign_host(self):
        self.state_file()
        with self.run_url('https://evil.example/login?platform=CLI&state=upstream-state'):
            with self.assertRaises(ValueError) as caught:
                server.start_login('cn')
        self.assertIn('授权链接域名或 state 校验失败', str(caught.exception))

    def test_cn_rejects_codebuddy_origin_host(self):
        # codebuddy.cn 只是 Origin/Referer，不能作为授权链接 host。
        self.state_file()
        with self.run_url('https://www.codebuddy.cn/login?platform=CLI&state=upstream-state'):
            with self.assertRaises(ValueError):
                server.start_login('cn')

    def test_rejects_state_mismatch(self):
        self.state_file(state='another-state')
        with self.run_url('https://copilot.tencent.com/login?platform=CLI&state=upstream-state'):
            with self.assertRaises(ValueError) as caught:
                server.start_login('cn')
        self.assertIn('state 校验失败', str(caught.exception))

    def test_rejects_wrong_path_or_port(self):
        for url in ('https://copilot.tencent.com/authorize?state=upstream-state',
                    'https://copilot.tencent.com:8443/login?state=upstream-state'):
            server.FLOW = None
            self.state_file()
            with self.run_url(url):
                with self.assertRaises(ValueError):
                    server.start_login('cn')

    def test_global_workbuddy_link_accepted(self):
        url = 'https://www.workbuddy.ai/login?platform=CLI&state=upstream-state'
        self.state_file(realm='global')
        with self.run_url(url):
            result = server.start_login('global')
        self.assertEqual(result['state'], 'pending')
        self.assertEqual(server.FLOW['realm'], 'global')


class ImportCredentialTests(unittest.TestCase):
    KEY = bytes(range(32))
    KEY_ID = '0123456789abcdef'
    PLAIN_ACCESS = 'access-token-plain-for-test'
    PLAIN_REFRESH = 'refresh-token-plain-for-test'
    PLAIN_NICK = '测试昵称'

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        (self.root / 'auths').mkdir()
        self.addCleanup(self.temp.cleanup)
        self.patches = [
            patch.object(server, 'ROOT', self.root),
            patch.object(server, 'gateway', lambda *a, **k: {'ok': True}),
            patch.object(server, '_CRYPTO', None),
            patch.object(crypto, 'protector_key', lambda: (self.KEY, self.KEY_ID)),
        ]
        for item in self.patches: item.start()
        self.addCleanup(lambda: [item.stop() for item in self.patches])

    def document(self, realm_domain='copilot.tencent.com', expires_at=1893456000000):
        return {
            'account': {'uid': 'cn-user-1', 'nickname': crypto.encrypt_auth_field(self.PLAIN_NICK), 'enterpriseId': ''},
            'auth': {
                'accessToken': crypto.encrypt_auth_field(self.PLAIN_ACCESS),
                'refreshToken': crypto.encrypt_auth_field(self.PLAIN_REFRESH),
                'expiresAt': expires_at,
                'domain': realm_domain,
            },
        }

    def stored(self, uid='cn-user-1'):
        return json.loads((self.root / 'auths' / ('workbuddy-' + uid + '.json')).read_text(encoding='utf-8'))

    def test_encrypted_info_is_decrypted_and_saved_plaintext(self):
        doc = self.document()
        result = server.save_credential(doc, 'cn')
        self.assertTrue(result['ok'])
        saved = self.stored()
        self.assertEqual(saved['auth']['accessToken'], self.PLAIN_ACCESS)
        self.assertEqual(saved['auth']['refreshToken'], self.PLAIN_REFRESH)
        self.assertEqual(saved['account']['nickname'], self.PLAIN_NICK)
        self.assertEqual(saved['auth']['expiresAt'], 1893456000)
        self.assertEqual(saved['auth']['realm'], 'cn')
        self.assertNotIn('$wbEncrypted', json.dumps(saved))

    def test_plaintext_legacy_info_still_imports(self):
        doc = self.document()
        doc['auth']['accessToken'] = 'legacy-access'
        doc['auth']['refreshToken'] = 'legacy-refresh'
        doc['account']['nickname'] = 'legacy'
        server.save_credential(doc, 'cn')
        saved = self.stored()
        self.assertEqual((saved['auth']['accessToken'], saved['account']['nickname']), ('legacy-access', 'legacy'))

    def test_decrypt_failure_is_actionable(self):
        def broken(_value):
            raise RuntimeError('无法获取 WorkBuddy at-rest 密钥（loggerGet）。')

        with patch.object(crypto, 'decrypt_auth_field', broken):
            with self.assertRaises(ValueError) as caught:
                server.save_credential(self.document(), 'cn')
        message = str(caught.exception)
        self.assertIn('WORKBUDDY_ELECTRON_PATH', message)
        self.assertNotIn(self.PLAIN_ACCESS, message)

    def test_realm_domain_mismatch_still_rejected(self):
        with self.assertRaises(ValueError):
            server.save_credential(self.document(realm_domain='www.workbuddy.ai'), 'cn')


if __name__ == '__main__':
    unittest.main()
