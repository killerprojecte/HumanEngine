"""Local-only synthetic API proxy for visual QA of the production UI.
No adapter, model, or real API endpoint is reached; all /api routes are fixtures.
Usage: python scripts/chat-preview-server.py /tmp/chat-fixture-data.json
Requires the production UI Node server on localhost:3129 (empty test DB).
"""
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler
import json, sys, urllib.request, urllib.error
from urllib.parse import unquote
DATA=json.load(open(sys.argv[1]))
class Handler(BaseHTTPRequestHandler):
 def do_GET(self): self.respond()
 def do_POST(self): self.respond()
 def do_DELETE(self): self.respond()
 def do_PUT(self): self.respond()
 def respond(self):
  path=unquote(self.path)
  if path == '/preview-mobile':
   raw=b'<!doctype html><html><head><title>HumanEngine mobile QA</title><meta name="viewport" content="width=device-width,initial-scale=1"></head><body style="margin:0;background:#e7e9ef"><iframe title="HumanEngine mobile preview" src="/chat" style="display:block;border:0;width:390px;height:844px;background:white"></iframe></body></html>'
   self.send_response(200); self.send_header('Content-Type','text/html; charset=utf-8');self.end_headers();self.wfile.write(raw);return
  if path.startswith('/api/') and not path.startswith('/api/_nuxt_icon/'):
   if self.command in ['POST','PUT']: self.rfile.read(int(self.headers.get('Content-Length','0')))
   key=path[5:]
   if key=='auth/session': value={'authenticated':True}
   elif key=='scenes': value=DATA['SCENES']
   elif key.endswith('/history'): value={'items':DATA['PRIVATE'] if 'private' in key else DATA['GROUP'],'coverage':'partial'}
   elif key.endswith('/state'): value={'activeTasks':0,'secondsSinceBotSpoke':None,'topics':[]}
   elif key=='simulations': value={'id':'synthetic-simulation'}
   elif key.startswith('simulations/'): value={'ok':True} if self.command=='DELETE' else DATA['SIM']
   else: self.send_error(404,'No synthetic fixture'); return
   raw=json.dumps(value,ensure_ascii=False).encode(); self.send_response(200); self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(raw);return
  if path == '/fixture-avatar/broken.svg':
   self.send_error(404,'Synthetic missing avatar'); return
  if path.startswith('/fixture-avatar/'):
   raw=b'<svg xmlns="http://www.w3.org/2000/svg" width="80" height="80"><rect width="80" height="80" rx="24" fill="#557d75"/><circle cx="40" cy="30" r="14" fill="#f7e3ca"/><path d="M12 80Q12 48 40 48Q68 48 68 80" fill="#d9e5ce"/><path d="M24 28Q22 10 40 10Q60 10 56 32Q45 14 24 28" fill="#373c4c"/></svg>'
   self.send_response(200); self.send_header('Content-Type','image/svg+xml'); self.end_headers();self.wfile.write(raw);return
  try:
   response=urllib.request.urlopen(urllib.request.Request('http://127.0.0.1:3129'+self.path,headers={'Cookie':self.headers.get('Cookie',''),'Accept-Language':'zh-CN'}))
   self.send_response(response.status)
   for k,v in response.headers.items():
    if k.lower() not in ['connection','transfer-encoding','content-length']: self.send_header(k,v)
   self.end_headers();self.wfile.write(response.read())
  except (urllib.error.URLError,OSError): self.send_error(502,'Production preview is not running')
for rows in DATA.values():
 for row in rows:
  for key in ['avatarUrl','senderAvatarUrl']:
   if key in row: row[key]=row[key].replace('https://fixture.invalid/','/fixture-avatar/')
print('Synthetic-only UI preview: http://127.0.0.1:3128',flush=True)
ThreadingHTTPServer(('127.0.0.1',3128),Handler).serve_forever()
