#!/usr/bin/env python3
"""阶段一/二验收用本地站点。

覆盖：HTML 链接 / JS 懒加载接口 / 3MB POST / 大响应 / 静态资源 /
真实 SSE(EventSource) 与 WebSocket 长连接 / robots.txt / 导航超时。
"""
import base64
import hashlib
import json
import struct
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = 18080
WS_GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
COUNTERS = {"sse": 0, "ws": 0, "robots": 0, "blocked": 0, "allowed": 0}
DIAG = []

HTML = """<!doctype html><html><head><title>browcrawler test</title>
<script src="/app.js"></script></head><body>
<h1>index</h1>
<a href="/page2">go page2</a>
<a href="/api/list">api list</a>
<a href="http://127.0.0.1:18080/page3">absolute same host</a>
<a href="http://localhost:18080/outside">cross-host link (scope regression)</a>
<iframe src="/frame.html"></iframe>
<form action="/submit" method="post"><input name="q"></form>
<img src="/logo.png">
<script>setTimeout(function(){fetch('/api/lazy').then(r=>r.text())}, 300);
fetch('/big').then(function(r){return r.text()}).then(function(t){document.title='big:'+t.length});
// 3MB POST：验证录制层不预读/不截断请求体（服务端回报实际收到的字节数）
setTimeout(function(){
  fetch('/upload', {method:'POST', body:'z'.repeat(3*1024*1024)}).then(function(r){return r.text()});
}, 300);</script>
</body></html>"""

PAGE2 = """<!doctype html><html><body>
<img src="http://localhost:18080/outside-img" width="1" height="1"><h1>page2</h1>
<a href="/page3">go page3</a>
<script>fetch('/api/data?id=2');</script></body></html>"""

PAGE3 = """<!doctype html><html><body><h1>page3</h1>
<a href="/">back</a></body></html>"""

FRAME = """<!doctype html><html><body>frame <a href="/frame-link">frame link</a></body></html>"""

APP_JS = """var ENDPOINTS = {
  user: '/api/v1/user',
  search: '/api/v1/search?q=',
  external: 'https://api.example.com/v2/token'
};
function load() { return fetch('/api/v1/user?id=1', {method:'GET'}); }
"""

# 长连接专项页：真实 EventSource + WebSocket，验证录制层只记元信息且不阻塞退出。
LONG_LIVED = """<!doctype html><html><body><h1>long lived</h1>
<script>
var diag = 'EventSource=' + (typeof EventSource) + ',WebSocket=' + (typeof WebSocket) + ',fetch=' + (typeof fetch);
fetch('/api/diag?d=' + encodeURIComponent(diag));
try { var es = new EventSource('/sse'); es.onmessage = function(e) { document.title = e.data; }; }
catch (e) { fetch('/api/diag?err=EventSource:' + encodeURIComponent(e.message)); }
try { var ws = new WebSocket('ws://127.0.0.1:18080/ws'); ws.onmessage = function(e) { document.title = e.data; }; }
catch (e) { fetch('/api/diag?err=WebSocket:' + encodeURIComponent(e.message)); }
</script></body></html>"""

# robots 专项页：obey_robots=true 时 /blocked 不应被导航。
ROBOTS_TEST = """<!doctype html><html><body><h1>robots</h1>
<a href="/allowed">allowed</a>
<a href="/blocked">blocked</a>
</body></html>"""

ROBOTS_TXT = "User-agent: *\nDisallow: /blocked\n"

PNG = bytes.fromhex(
    "89504e470d0a1a0a0000000d4948445200000001000000010806000000"
    "1f15c4890000000a49444154789c6360000002000100ffff03000006000557bf"
    "abd40000000049454e44ae426082"
)


def ws_text_frame(text):
    """服务端→客户端文本帧（FIN+text，服务端不掩码）。"""
    payload = text.encode()
    n = len(payload)
    head = b"\x81"
    if n < 126:
        head += bytes([n])
    else:
        head += b"\x7e" + struct.pack(">H", n)
    return head + payload


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def send(self, body, ctype="text/html; charset=utf-8", status=200, extra=None):
        if isinstance(body, str):
            body = body.encode()
        self.send_response(status)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        for k, v in (extra or {}).items():
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(body)

    def handle_ws(self):
        COUNTERS["ws"] += 1
        key = self.headers.get("Sec-WebSocket-Key", "")
        if not key:
            self.send_error(400, "missing websocket key")
            return
        accept = base64.b64encode(hashlib.sha1((key + WS_GUID).encode()).digest()).decode()
        self.send_response(101, "Switching Protocols")
        self.send_header("Upgrade", "websocket")
        self.send_header("Connection", "Upgrade")
        self.send_header("Sec-WebSocket-Accept", accept)
        self.end_headers()
        try:
            for i in range(4):
                self.wfile.write(ws_text_frame(f"tick-{i}"))
                self.wfile.flush()
                time.sleep(0.3)
            time.sleep(6)  # 保持长连接，检验录制层与 Shutdown 时序
        except (BrokenPipeError, ConnectionResetError, OSError):
            pass
        self.close_connection = True

    def do_GET(self):
        path = self.path.split("?")[0]
        if self.headers.get("Upgrade", "").lower() == "websocket":
            self.handle_ws()
            return
        if path == "/":
            self.send(HTML)
        elif path == "/page2":
            self.send(PAGE2)
        elif path == "/page3":
            self.send(PAGE3)
        elif path == "/frame.html":
            self.send(FRAME)
        elif path == "/longlived.html":
            self.send(LONG_LIVED)
        elif path == "/robots-test.html":
            self.send(ROBOTS_TEST)
        elif path == "/robots.txt":
            COUNTERS["robots"] += 1
            self.send(ROBOTS_TXT, "text/plain")
        elif path == "/debug-counters":
            self.send(json.dumps({"counters": COUNTERS, "diag": DIAG}), "application/json")
        elif path == "/api/diag":
            DIAG.append(self.path)
            self.send("ok", "text/plain")
        elif path.startswith("/item/"):
            # 动态路径泛化验收页：/item/N 内含指向 /item/N+1 的链接，形成无限变体链
            try:
                n = int(path[len("/item/"):])
            except ValueError:
                self.send("bad item", "text/plain", 404)
                return
            self.send(f"<!doctype html><html><body>item {n}"
                      f"<a href='/item/{n + 1}'>next</a></body></html>")
        elif path == "/interactive.html":
            self.send("""<!doctype html><html><body><h1>interactive</h1>
<button id="load-hidden" onclick="fetch('/api/hidden').then(r=>r.json()).then(j=>{document.body.setAttribute('data-loaded', j.secret)})">load hidden</button>
<a href="/page2">normal link</a>
</body></html>""")
        elif path == "/api/hidden":
            self.send(json.dumps({"secret": "hidden-endpoint"}), "application/json")
        elif path == "/spa-next.html":
            # Next.js 运行时探针验证页：mock window.next.router.components
            self.send("""<!doctype html><html><body><h1>fake next app</h1>
<script>window.next={router:{components:{
  "/dashboard":{}, "/settings/profile":{}, "/admin/panel":{}
}}};</script>
</body></html>""")
        elif path == "/spa-push.html":
            # generic pushState hook 验证页：点击按钮做编程式导航
            self.send("""<!doctype html><html><body><h1>spa push</h1>
<button id="go" onclick="history.pushState({},'','/spa/user/9')">go</button>
<script>history.pushState({},'','/spa/initial');</script>
</body></html>""")
        elif path == "/safety.html":
            # F1 safety 验收页：两个危险表单 + logout 链接 + 一个正常表单。
            # 危险表单被拦截时不产生 POST 流量；正常表单照常提交。
            self.send("""<!doctype html><html><body><h1>safety</h1>
<a href="/logout">sign out</a>
<a href="/catalogue">catalogue</a>
<form method="POST" action="/account/update">
  <input name="password_new"><input name="password_confirm">
  <input type="hidden" name="csrf" value="x"><button>change</button>
</form>
<form method="POST" action="/admin/delete-user">
  <input name="confirm" value="yes"><button>del</button>
</form>
<form method="GET" action="/search">
  <input name="q" value="safe-form"><button>search</button>
</form>
</body></html>""")
        elif path == "/logout":
            self.send("logged out", "text/plain", 200)
        elif path == "/account/update" or path == "/admin/delete-user":
            self.send("ok", "text/plain", 200)
        elif path == "/authed":
            # 认证注入验收页：需要 Cookie session=secret123
            cookie = self.headers.get("Cookie", "")
            if "session=secret123" in cookie:
                self.send("<html><body>authed content</body></html>")
            else:
                self.send("unauthorized", "text/plain", 401)
        elif path == "/flaky":
            # 重试退避验收页：周期模式（每 3 次请求：前 2 次 503、第 3 次 200），
            # 幂等可重复——任意时刻连续请求 3 次内必得到 503,503,200 序列
            COUNTERS["flaky"] = COUNTERS.get("flaky", 0) + 1
            if (COUNTERS["flaky"] - 1) % 3 < 2:
                self.send("service unavailable", "text/plain", 503)
            else:
                self.send("<html><body>flaky ok</body></html>")
        elif path in ("/allowed", "/blocked"):
            COUNTERS[path.lstrip("/")] += 1
            self.send(path.lstrip("/"), "text/plain")
        elif path == "/app.js":
            self.send(APP_JS, "application/javascript; charset=utf-8")
        elif path == "/logo.png":
            self.send(PNG, "image/png")
        elif path == "/api/list":
            self.send(json.dumps({"items": [1, 2, 3]}), "application/json")
        elif path == "/big":
            # 超过 max_body_size 的响应：录制层只记元信息，但不能截断真实流
            self.send(b"x" * (3 * 1024 * 1024), "text/plain")
        elif path == "/api/lazy":
            self.send(json.dumps({"lazy": True}), "application/json")
        elif path == "/api/data":
            self.send(json.dumps({"id": self.path}), "application/json")
        elif path == "/hang":
            # 导航超时回归：故意 5s 不响应（大于测试用的 navigation_ms）
            time.sleep(5)
            self.send("late", "text/plain")
        elif path == "/sse":
            COUNTERS["sse"] += 1
            # 真实长连接（EventSource 会重连），验证只记元信息且不阻塞回收。
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache")
            self.send_header("Connection", "keep-alive")
            self.end_headers()
            try:
                for i in range(12):
                    self.wfile.write(f"data: tick-{i}\n\n".encode())
                    self.wfile.flush()
                    time.sleep(0.5)
            except (BrokenPipeError, ConnectionResetError, OSError):
                pass
            self.close_connection = True
        else:
            self.send("not found", "text/plain", 404)

    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        got = len(self.rfile.read(n))
        self.send(json.dumps({"ok": True, "received": got}), "application/json")


if __name__ == "__main__":
    print(f"test site on http://127.0.0.1:{PORT}", flush=True)
    ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
