"""Landing page and download entry points, rendered by the actual Flask app."""
from pathlib import Path
import unittest
from unittest.mock import patch

import app as website


class Website(unittest.TestCase):
    def setUp(self):
        self.client = website.app.test_client()

    def test_mac_download_and_installer_are_discoverable(self):
        response = self.client.get('/')
        self.assertEqual(response.status_code, 200)
        html = response.get_data(as_text=True)
        self.assertIn('id="downloads"', html)
        self.assertIn('href="#downloads"', html)
        self.assertIn(f'{website.GITHUB_URL}/releases/latest/download/MCPRuntimeAdapter-unsigned-preview.zip', html)
        self.assertIn('Download for macOS', html)
        self.assertIn('macOS 13 or later', html)
        self.assertIn('Apple Silicon and Intel', html)
        self.assertIn('unsigned preview', html)
        self.assertIn('sh -s -- --with-desktop', html)
        self.assertIn('~/.local/bin', html)
        self.assertIn('~/Applications', html)
        self.assertIn(f'{website.GITHUB_URL}/releases/latest', html)

    def test_adapter_guide_anchor_exists_and_uses_configured_docs_origin(self):
        with patch.object(website, 'DOCS_URL', 'https://docs.example.test/manual/'):
            html = self.client.get('/').get_data(as_text=True)
        self.assertIn('https://docs.example.test/manual/connect-clients/#macos-background-adapter-and-companion', html)
        guide = Path(__file__).resolve().parents[1] / 'docs/connect-clients.md'
        self.assertIn('## macOS background adapter and companion', guide.read_text())

    def test_docs_redirect_preserves_path(self):
        response = self.client.get('/docs/connect-clients/')
        self.assertEqual(response.status_code, 302)
        self.assertEqual(response.headers['Location'], website.DOCS_URL + 'connect-clients/')

    def test_home_security_headers(self):
        response = self.client.get('/')
        self.assertEqual(response.headers['X-Content-Type-Options'], 'nosniff')
        self.assertEqual(response.headers['X-Frame-Options'], 'DENY')
        self.assertIn("frame-ancestors 'none'", response.headers['Content-Security-Policy'])

    def test_download_styles_are_served(self):
        response = self.client.get('/static/style.css')
        self.assertEqual(response.status_code, 200)
        self.assertIn('.download-grid', response.get_data(as_text=True))
        self.assertIn('max-age=', response.headers['Cache-Control'])


if __name__ == '__main__':
    unittest.main()
