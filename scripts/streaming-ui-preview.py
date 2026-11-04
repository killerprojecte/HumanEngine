"""Synthetic-only visual QA proxy; never forwards model requests or credentials.
Start the local UI on port 3140 with an empty HUMANENGINE_DB, then run this script.
Open http://127.0.0.1:3141/models or /model-calls. Mutations stay in process memory.
"""
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler
import json
import urllib.request
from urllib.parse import urlparse

CONFIG = {
    'providers': [dict(id='demo-native', name='Demo native provider', protocol='openai-chat', baseUrl='https://fixture.invalid/v1', credentialEnv='', retryCount=0, timeoutMs=30000), dict(id='demo-custom', name='Demo custom adapter', protocol='fixture-custom', baseUrl='https://fixture.invalid', credentialEnv='', retryCount=0, timeoutMs=30000)],
    'models': [dict(id='demo-stream', displayName='Streaming demo', providerId='demo-native', apiModelName='demo-text', capabilities=['text']), dict(id='demo-sync', displayName='Synchronous demo', providerId='demo-native', apiModelName='demo-text', capabilities=['text'], streaming=False), dict(id='demo-custom-model', displayName='Custom adapter demo', providerId='demo-custom', apiModelName='demo-custom', capabilities=['text'])],
    'routes': [],
}
CALLS = []
for i, (mode, first, kind, status) in enumerate([('stream', 482.3, 'text', 'completed'), ('stream', 1160, 'reasoning', 'completed'), ('stream', 724, 'tool', 'completed'), ('non_stream', None, None, 'completed'), ('custom', None, None, 'completed'), ('unknown', None, None, 'completed'), ('stream', None, None, 'failed')]):
    CALLS.append(dict(id=108-i, at=f'2026-11-04T12:{9-i:02d}:00.000Z', modelId='demo-stream' if mode=='stream' else 'demo-'+mode, providerId='demo-native' if mode!='custom' else 'demo-custom', apiModelName='demo-text', role='planner' if i%2==0 else 'replyer', status=status, attempt=1, inputTokens=28640 if status=='completed' else None, cacheReadInputTokens=20480 if status=='completed' else None, cacheWriteInputTokens=0 if status=='completed' else None, outputTokens=462 if status=='completed' else None, durationMs=1840 if status=='completed' else 30000, tokensPerSecond=251.09 if status=='completed' else None, transportMode=mode, firstTokenMs=first, firstTokenKind=kind, failureReason='first_content_timeout' if status=='failed' else None))

class Handler(BaseHTTPRequestHandler):
    def do_GET(self): self.respond()
    def do_PUT(self): self.respond()
    def do_POST(self): self.respond()
    def do_DELETE(self): self.respond()
    def respond(self):
        global CONFIG
        path = urlparse(self.path).path
        if path.startswith('/api/') and not path.startswith('/api/_nuxt_icon/'):
            body = self.rfile.read(int(self.headers.get('Content-Length', '0')))
            if path == '/api/auth/session': data = {'authenticated': True}
            elif path == '/api/models':
                if self.command == 'PUT': CONFIG = json.loads(body)
                data = CONFIG
            elif path == '/api/model-protocols': data = [dict(id='openai-chat', label='OpenAI Chat'), dict(id='openai-responses', label='OpenAI Responses'), dict(id='fixture-custom', label='Custom fixture')]
            elif path == '/api/model-calls': data = dict(items=CALLS, nextCursor=None, retentionDays=30, maxRecords=100000)
            elif path == '/api/embedding-index': data = dict(state='unconfigured', coverage=dict(eligible=0, indexed=0, pending=0), retained=dict(generations=0, vectors=0))
            elif path == '/api/usage': data = []
            else:
                self.send_error(404, 'No synthetic fixture; real API access is blocked')
                return
            raw = json.dumps(data).encode()
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(raw)
            return
        try:
            response = urllib.request.urlopen(urllib.request.Request('http://127.0.0.1:3140'+self.path, headers={'Cookie': self.headers.get('Cookie', 'i18n_redirected=zh-TW'), 'Accept-Language': 'zh-TW'}))
            self.send_response(response.status)
            for key, value in response.headers.items():
                if key.lower() not in ['connection', 'transfer-encoding', 'content-length']: self.send_header(key, value)
            self.end_headers()
            self.wfile.write(response.read())
        except Exception:
            self.send_error(502, 'Local UI preview unavailable')

print('Synthetic-only preview on http://127.0.0.1:3141', flush=True)
ThreadingHTTPServer(('127.0.0.1', 3141), Handler).serve_forever()
