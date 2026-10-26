"""compat/core/workbuddy_atrest_crypto 测试（离线自洽，不读取或输出任何真实凭据）。"""

import base64
import hashlib
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))

from compat.core import workbuddy_atrest_crypto as crypto  # noqa: E402

# 44 字符 canonical base64 == 32 字节，仅用于测试（与真实密钥无关）
TEST_SECRET = base64.b64encode(hashlib.sha256(b"test-seed").digest()).decode()
OTHER_SECRET = base64.b64encode(hashlib.sha256(b"other-seed").digest()).decode()


class AtRestCryptoTests(unittest.TestCase):
    def setUp(self):
        self._orig_fetch = crypto._fetch_key_payload
        self.secret = TEST_SECRET
        self.fetch = mock.patch.object(
            crypto, "_fetch_key_payload",
            side_effect=lambda: {"atRestSecretKey": self.secret},
        )
        self.fetch.start()
        self.addCleanup(self.fetch.stop)
        # 隔离真实环境变量，避免读到本机密钥
        clean_env = {k: v for k, v in os.environ.items()
                     if k != "WORKBUDDY_AT_REST_SECRET"}
        env = mock.patch.dict(os.environ, clean_env, clear=True)
        env.start()
        self.addCleanup(env.stop)
        crypto._cached_key = None
        self.addCleanup(setattr, crypto, "_cached_key", None)

    def expected_key_id(self, secret):
        key = hashlib.sha256(secret.encode("utf-8")).digest()
        return hashlib.sha256(key).hexdigest()[:16]

    def test_encrypt_decrypt_roundtrip(self):
        token = "eyJhbGciOiJSUzI1NiJ9.payload.signature"
        sealed = crypto.encrypt_auth_field(token)
        self.assertTrue(crypto.is_encrypted_field(sealed))
        self.assertEqual(sealed["$wbEncrypted"], 1)
        envelope = __import__("json").loads(base64.b64decode(sealed["envelope"]))
        self.assertEqual(envelope["suite"], 1)
        self.assertEqual(envelope["keyId"], self.expected_key_id(TEST_SECRET))
        self.assertEqual(crypto.decrypt_auth_field(sealed), token)

    def test_plaintext_passthrough(self):
        self.assertEqual(crypto.decrypt_auth_field("abc.jwt.token"), "abc.jwt.token")
        self.assertEqual(crypto.decrypt_auth_field(""), "")
        self.assertFalse(crypto.is_encrypted_field("abc"))

    def test_key_id_mismatch_raises(self):
        sealed = crypto.encrypt_auth_field("some-token")
        self.secret = OTHER_SECRET
        crypto._cached_key = None
        with self.assertRaises(RuntimeError) as ctx:
            crypto.decrypt_auth_field(sealed)
        self.assertIn("keyId", str(ctx.exception))
        self.assertNotIn(TEST_SECRET, str(ctx.exception))

    def test_missing_exe_error_is_actionable(self):
        with mock.patch.object(crypto, "_electron_candidates", return_value=[]):
            with self.assertRaises(RuntimeError) as ctx:
                self._orig_fetch()
        message = str(ctx.exception)
        self.assertIn("WORKBUDDY_ELECTRON_PATH", message)
        self.assertIn("WORKBUDDY_AT_REST_SECRET", message)
        self.assertIn("安装 WorkBuddy", message)
        self.assertNotIn(TEST_SECRET, message)

    def test_env_path_takes_priority_and_candidates_exist(self):
        with tempfile.TemporaryDirectory() as tmp:
            exe = Path(tmp) / "WorkBuddy.exe"
            exe.write_bytes(b"stub")
            with mock.patch.dict(os.environ, {"WORKBUDDY_ELECTRON_PATH": str(exe)}):
                candidates = crypto._electron_candidates()
            self.assertTrue(candidates)
            self.assertEqual(candidates[0].resolve(), exe.resolve())
            for candidate in candidates:
                self.assertTrue(candidate.is_file(), candidate)

    def test_discovers_real_installed_path(self):
        if sys.platform != "win32":
            self.skipTest("仅 Windows 安装路径发现")
        local = Path(os.environ.get("LOCALAPPDATA", Path.home() / "AppData" / "Local"))
        known = [
            Path(r"E:\应用\WorkBuddyAI\WorkBuddy.exe"),
            local / "Programs" / "WorkBuddyAI" / "WorkBuddy.exe",
            local / "Programs" / "WorkBuddy" / "WorkBuddy.exe",
        ]
        installed = [p for p in known if p.is_file()]
        if not installed:
            self.skipTest("本机未安装 WorkBuddy")
        found = {p.resolve() for p in crypto._electron_candidates()}
        self.assertTrue(any(p.resolve() in found for p in installed))

    def test_parse_display_icon(self):
        parse = crypto._parse_display_icon
        self.assertEqual(
            parse(r'"C:\Apps\WorkBuddy\WorkBuddy.exe",0'),
            Path(r"C:\Apps\WorkBuddy\WorkBuddy.exe"),
        )
        self.assertEqual(
            parse(r"C:\Apps\WorkBuddy\WorkBuddy.exe,0"),
            Path(r"C:\Apps\WorkBuddy\WorkBuddy.exe"),
        )
        self.assertIsNone(parse(None))
        self.assertIsNone(parse(""))


if __name__ == "__main__":
    unittest.main()
