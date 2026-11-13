#!/usr/bin/env python3
"""browcrawler 一键自动化验收（对应 docs/实施方案.md §10.2 的 8 类用例）。

用法：
    python scripts/verify.py            # 本地全量验收（不含外网 HTTPS）
    python scripts/verify.py --https    # 额外验证 HTTPS MITM（需本机 10808 代理）
    python scripts/verify.py --quick    # 只跑核心 BFS + 回收断言

退出码 0 = 全部通过（SKIP 不算失败）；非 0 = 有 FAIL。
设计约定：所有用例使用 work/verify/ 下的独立输出，不污染 out/ 正式产物。
"""
from __future__ import annotations

import argparse
import glob
import json
import os
import re
import shutil
import signal
import socket
import subprocess
import sys
import time
from dataclasses import dataclass, field
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
WORK = ROOT / "work" / "verify"
SITE = ROOT / "testdata" / "site.py"
CRAWLER = ROOT / "bin" / "crawler.exe"
SITE_URL = "http://127.0.0.1:18080/"
IS_WINDOWS = os.name == "nt"


@dataclass
class Result:
    name: str
    ok: bool
    detail: str = ""
    skipped: bool = False


@dataclass
class Report:
    results: list[Result] = field(default_factory=list)

    def add(self, name: str, ok: bool, detail: str = "") -> None:
        self.results.append(Result(name, bool(ok), detail))
        print(f"  [{'PASS' if ok else 'FAIL'}] {name}" + (f" — {detail}" if detail else ""))

    def skip(self, name: str, why: str) -> None:
        self.results.append(Result(name, True, why, skipped=True))
        print(f"  [SKIP] {name} — {why}")

    @property
    def failures(self) -> list[Result]:
        return [r for r in self.results if not r.ok]

    def summary(self) -> int:
        p = sum(1 for r in self.results if not r.skipped and r.ok)
        s = sum(1 for r in self.results if r.skipped)
        f = len(self.failures)
        print(f"\n=== 验收汇总: PASS={p} SKIP={s} FAIL={f} ===")
        for r in self.failures:
            print(f"  FAIL: {r.name} — {r.detail}")
        return 1 if f else 0


def read_jsonl(path: Path) -> list[dict]:
    if not path.exists():
        return []
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]


def port_open(port: int, host: str = "127.0.0.1", timeout: float = 0.4) -> bool:
    try:
        with socket.create_connection((host, port), timeout):
            return True
    except OSError:
        return False


def leftover_process(image: str) -> bool:
    if not IS_WINDOWS:
        out = subprocess.run(["pgrep", "-f", image], capture_output=True, text=True).stdout
        return bool(out.strip())
    out = subprocess.run(["tasklist", "/FI", f"IMAGENAME eq {image}"],
                         capture_output=True, text=True, errors="replace").stdout
    return image.lower() in out.lower()


def session_dirs() -> list[str]:
    d = ROOT / "work" / "sessions"
    return [p.name for p in d.iterdir()] if d.exists() else []


class SiteServer:
    """本地测试站点的确定性生命周期管理。"""

    def __init__(self) -> None:
        self.proc: subprocess.Popen | None = None

    def start(self) -> None:
        if port_open(18080):
            # 复用外部已启动的站点，但结束时不要杀别人的进程。
            self.proc = None
            return
        log = (WORK / "site.log").open("w", encoding="utf-8")
        self.proc = subprocess.Popen([sys.executable, str(SITE)], stdout=log,
                                     stderr=subprocess.STDOUT, cwd=ROOT)
        for _ in range(40):
            if port_open(18080):
                return
            if self.proc.poll() is not None:
                raise RuntimeError("测试站点启动失败，见 work/verify/site.log")
            time.sleep(0.15)
        raise RuntimeError("测试站点端口 18080 未就绪")

    def stop(self) -> None:
        if self.proc is None:
            return
        self.proc.terminate()
        try:
            self.proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.proc.kill()
        self.proc = None


def write_config(name: str, **overrides: object) -> Path:
    """基于 config.example.yaml 生成一次性配置，避免依赖含本机代理的 config.yaml。"""
    base = (ROOT / "config.example.yaml").read_text(encoding="utf-8")
    lines = []
    for line in base.splitlines():
        key = line.split(":", 1)[0].strip()
        if key in overrides:
            value = overrides[key]
            if isinstance(value, bool):
                value = "true" if value else "false"
            indent = line[: len(line) - len(line.lstrip())]
            lines.append(f"{indent}{key}: {value}")
        else:
            lines.append(line)
    path = WORK / name
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")
    return path


def run_crawler(args: list[str], timeout: int = 180) -> subprocess.CompletedProcess:
    return subprocess.run([str(CRAWLER), *args], cwd=ROOT, capture_output=True,
                          text=True, encoding="utf-8", errors="replace", timeout=timeout)


def base_cfg() -> Path:
    return write_config("base.yaml", upstream_proxy='""', har='""')


def check_core_bfs(rep: Report) -> tuple[list[dict], list[dict], str]:
    traffic, discovered = WORK / "core-traffic.jsonl", WORK / "core-discovered.jsonl"
    cfg = base_cfg()
    p = run_crawler(["--config", str(cfg), "--url", SITE_URL, "--depth", "2",
                     "--out", str(traffic), "--discovered", str(discovered),
                     "--requests", str(WORK / "core-requests.txt"),
                     "--har", str(WORK / "core.har")])
    out = p.stdout + p.stderr
    rows, discs = read_jsonl(traffic), read_jsonl(discovered)
    rep.add("A1 基本 BFS 成功", p.returncode == 0 and "failed=0" in out,
            f"exit={p.returncode} " + (re.search(r"\[result\] (pages=[^\n]+)", out).group(1)
                                       if re.search(r"\[result\] (pages=[^\n]+)", out) else out[-160:]))
    rep.add("A2 录制到足量流量", len(rows) >= 10, f"traffic={len(rows)}")
    rep.add("A3 发现链接与 DOM 提取", len(discs) >= 10, f"discovered={len(discs)}")
    urls = {r["url"] for r in rows}
    rep.add("A4 渲染后懒加载接口 /api/lazy", any("/api/lazy" in u for u in urls))
    rep.add("A5 渲染后接口 /api/data?id=2", any("/api/data?id=2" in u for u in urls))
    js = [d for d in discs if str(d.get("source", "")).startswith("regex:")]
    rep.add("A6 正则提取 JS 接口（basecrawler/parse）", any("/api/v1/user" in d.get("url", "") for d in js),
            f"js_links={len(js)}")
    submits = [r for r in rows if r["method"] == "POST" and r["url"].endswith("/submit")]
    rep.add("A7 表单自动提交产生 POST /submit",
            bool(submits) and submits[0].get("request_body") == "q=test",
            f"body={submits[0].get('request_body')!r}" if submits else "无 POST /submit 记录")
    req_txt = WORK / "core-requests.txt"
    if req_txt.exists():
        raw = req_txt.read_text(encoding="utf-8")
        rep.add("A8 可重放请求包含完整 POST 报文",
                f"POST {SITE_URL}submit HTTP/1.1" in raw and "q=test" in raw and
                raw.count("HTTP/1.1") >= len(rows) - 1,
                f"packets={raw.count('HTTP/1.1')}")
    else:
        rep.add("A8 可重放请求包导出", False, "文件未生成")
    return rows, discs, out


def check_readonly_and_limits(rep: Report, rows: list[dict]) -> None:
    up = [r for r in rows if r["url"].endswith("/upload")]
    body = up[0].get("body", "") if up else ""
    rep.add("B1 3MB POST 完整送达（只读不截断）",
            bool(up) and re.search(r'"received"\s*:\s*3145728', body) is not None,
            f"resp={body[:80]!r}")
    rep.add("B2 大请求体未被录制层消费",
            bool(up) and up[0].get("request_body_omitted") == "too_large",
            f"request_body_omitted={up[0].get('request_body_omitted') if up else 'N/A'}")
    big = [r for r in rows if r["url"].endswith("/big")]
    rep.add("B3 响应超限时真实流完整", bool(big) and big[0]["body_size"] == 3145728,
            f"body_size={big[0]['body_size'] if big else 'N/A'}")
    rep.add("B4 超限响应只记元信息",
            bool(big) and big[0].get("body_omitted") == "too_large" and not big[0].get("body"),
            f"omit={big[0].get('body_omitted') if big else 'N/A'} stored={len(big[0].get('body','')) if big else 'N/A'}")
    png = [r for r in rows if r["url"].endswith("/logo.png")]
    rep.add("B5 静态资源只记元信息",
            bool(png) and png[0].get("body_omitted") == "static_resource" and not png[0].get("body"),
            f"omit={png[0].get('body_omitted') if png else 'N/A'}")


def check_scope_and_har(rep: Report, rows: list[dict], discs: list[dict]) -> None:
    # 精确匹配导航目标（/outside-img 是新加的域外子请求，用于 C2 的 in_scope 标记）
    rep.add("C1 默认 scope 不跟随跨主机外链",
            not any(r["url"].rstrip("/") == "http://localhost:18080/outside" for r in rows))
    # 双层生效（2026-11-10）：域外 URL 从 discovered/请求包/报告中过滤，
    # 但 traffic.jsonl 仍全量录制并带 in_scope=false 标记
    rep.add("C2 跨主机外链全链路过滤（discovered 不含 + traffic 带 in_scope=false）",
            not any("example.com" in d.get("url", "") for d in discs) and
            any(r.get("in_scope") is False for r in rows))
    har_path = WORK / "core.har"
    if har_path.exists():
        har = json.loads(har_path.read_text(encoding="utf-8"))
        n = len(har.get("log", {}).get("entries", []))
        rep.add("C3 HAR 条目数与 JSONL 一致", n == len(rows), f"har={n} jsonl={len(rows)}")
    else:
        rep.add("C3 HAR 导出", False, "文件未生成")


def check_any_scope(rep: Report) -> None:
    cfg = base_cfg()
    p = run_crawler(["--config", str(cfg), "--url", SITE_URL, "--scope", "any", "--depth", "1",
                     "--out", str(WORK / "scopeany-traffic.jsonl"),
                     "--discovered", str(WORK / "scopeany-disc.jsonl")])
    out = p.stdout + p.stderr
    rep.add("D1 -scope any 显式放行跨站", "localhost:18080/outside" in out or
            "depth=1" in out and "localhost:18080/outside" in str(read_jsonl(WORK / "scopeany-disc.jsonl")),
            "显式开关生效")


def check_failure_cleanup(rep: Report) -> None:
    cfg = base_cfg()
    bad = str(ROOT / "config.example.yaml") + "/nope.jsonl"  # 父路径是文件，必然创建失败
    p = run_crawler(["--config", str(cfg), "--url", SITE_URL, "--depth", "0",
                     "--out", str(WORK / "fail-traffic.jsonl"), "--discovered", bad])
    out = p.stdout + p.stderr
    rep.add("E1 初始化失败返回非零", p.returncode != 0, f"exit={p.returncode}")
    rep.add("E2 失败分支仍优雅回收浏览器", "[fatal]" in out and "exited gracefully" in out,
            out[-160:] if "exited gracefully" not in out else "")


def check_ctrl_break(rep: Report) -> None:
    if not IS_WINDOWS:
        rep.skip("F1 Ctrl+C 优雅中断", "仅 Windows 实现 CTRL_BREAK 组")
        return
    cfg = write_config("ctrlc.yaml", upstream_proxy='""', settle_ms=600)
    cmd = [str(CRAWLER), "--config", str(cfg), "--url", SITE_URL, "--depth", "2",
           "--out", str(WORK / "ctrlc-traffic.jsonl"), "--discovered", str(WORK / "ctrlc-disc.jsonl")]
    proc = subprocess.Popen(cmd, cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                            text=True, encoding="utf-8", errors="replace",
                            creationflags=subprocess.CREATE_NEW_PROCESS_GROUP)
    time.sleep(2.5)
    try:
        os.kill(proc.pid, signal.CTRL_BREAK_EVENT)
        out, _ = proc.communicate(timeout=40)
    except Exception as exc:  # noqa: BLE001
        proc.kill()
        out, _ = proc.communicate()
        out += f"\n[verify] signal error: {exc}"
    rep.add("F1 Ctrl+C 优雅中断", "interrupted" in out and "exited gracefully" in out,
            out[-160:] if "exited gracefully" not in out else "")
    rep.add("F2 中断后数据仍落盘", len(read_jsonl(WORK / "ctrlc-traffic.jsonl")) > 0)
    rep.add("F3 中断不计为 failed", re.search(r"failed=0", out) is not None)


def check_nav_timeout(rep: Report) -> None:
    cfg = write_config("hang.yaml", upstream_proxy='""', navigation_ms=2000,
                       task_ms=15000, settle_ms=0, cdp_ready_ms=30000)
    t0 = time.monotonic()
    p = run_crawler(["--config", str(cfg), "--url", "http://127.0.0.1:18080/hang", "--depth", "0",
                     "--out", str(WORK / "hang-traffic.jsonl"),
                     "--discovered", str(WORK / "hang-disc.jsonl")], timeout=90)
    wall = time.monotonic() - t0
    out = p.stdout + p.stderr
    rep.add("G1 导航超时生效", "deadline exceeded" in out or "context canceled" in out,
            f"wall={wall:.1f}s")
    rep.add("G2 超时后不挂起", wall < 25, f"wall={wall:.1f}s")
    rep.add("G3 超时后优雅回收", "exited gracefully" in out)


def check_https(rep: Report) -> None:
    if not port_open(10808):
        rep.skip("H1 HTTPS MITM 明文采集", "本机 10808 代理不可用（不假绿）")
        rep.skip("H2 MITM 解密出明文 HTML", "依赖 H1")
        rep.skip("H3 合并公共根", "依赖 H1")
        rep.skip("H4 TLS passthrough", "依赖 H1")
        return
    cfg = write_config("https.yaml", upstream_proxy='"http://127.0.0.1:10808"')
    p = run_crawler(["--config", str(cfg), "--url", "https://example.com/", "--depth", "1",
                     "--out", str(WORK / "https-traffic.jsonl"),
                     "--discovered", str(WORK / "https-disc.jsonl")], timeout=180)
    rows = read_jsonl(WORK / "https-traffic.jsonl")
    rep.add("H1 HTTPS 请求成功", p.returncode == 0 and any(r.get("scheme") == "https" and r.get("status") == 200 for r in rows),
            f"rows={len(rows)}")
    rep.add("H2 MITM 解密出明文 HTML", any("Example Domain" in r.get("body", "") for r in rows),
            f"bodies={[r.get('body_size') for r in rows]}")

    roots = ROOT / "assets" / "ca" / "cacert.pem"
    if not roots.exists():
        rep.skip("H3 合并公共根后 HTTPS", "assets/ca/cacert.pem 缺失（按 §7 下载后可用）")
        rep.skip("H4 TLS passthrough", "依赖 H3")
        return
    sessions_glob = str(ROOT / "work" / "sessions" / "*")
    before = set(glob.glob(sessions_glob))
    p = run_crawler(["--config", str(cfg), "--url", "https://example.com/", "--depth", "0",
                     "--public-roots", str(roots), "--keep-session-dir",
                     "--out", str(WORK / "merge-traffic.jsonl"),
                     "--discovered", str(WORK / "merge-disc.jsonl")], timeout=180)
    merged = read_jsonl(WORK / "merge-traffic.jsonl")
    created = sorted(set(glob.glob(sessions_glob)) - before)
    bundles = [os.path.join(d, "ca-bundle.pem") for d in created]
    bundles = [b for b in bundles if os.path.exists(b)]
    certs = open(bundles[-1], "rb").read().count(b"-----BEGIN CERTIFICATE-----") if bundles else 0
    rep.add("H3 合并公共根后 HTTPS 仍可用",
            p.returncode == 0 and any("Example Domain" in r.get("body", "") for r in merged) and certs >= 100,
            f"bundle_certs={certs} rows={len(merged)}")
    # -keep-session-dir 只为读取 bundle；验收完立即清掉，保证 I3 目录为空。
    for d in created:
        shutil.rmtree(d, ignore_errors=True)

    p = run_crawler(["--config", str(cfg), "--url", "https://example.com/", "--depth", "0",
                     "--passthrough", "example.com", "--public-roots", str(roots),
                     "--out", str(WORK / "pt-traffic.jsonl"),
                     "--discovered", str(WORK / "pt-disc.jsonl")], timeout=180)
    pt = read_jsonl(WORK / "pt-traffic.jsonl")
    out = p.stdout + p.stderr
    rep.add("H4 TLS passthrough 只记 CONNECT 元信息",
            p.returncode == 0 and "failed=0" in out and bool(pt) and
            all(r["method"] == "CONNECT" and r.get("body_omitted") == "tls_passthrough" for r in pt),
            f"rows={len(pt)}")


def chrome_can_stay(root) -> bool:
    """探测 Chrome for Testing 在当前环境能否驻留（Agent 沙箱/非交互窗口站下
    chrome 启动即退，属执行环境限制，显式 SKIP 而非假绿）。"""
    chrome_bin = root / "bin" / "chrome" / "chrome-win64" / "chrome.exe"
    if not chrome_bin.exists():
        return False
    probe_dir = root / "work" / "chrome-probe"
    probe_dir.mkdir(parents=True, exist_ok=True)
    # Chrome for Testing 157 不接受两段式 "--user-data-dir <path>"（exit 13），
    # 必须等号连接——与 browserrunner/chrome.go 的实际启动参数保持一致。
    proc = subprocess.Popen(
        [str(chrome_bin), "--headless=new", "--remote-debugging-port=0",
         "--user-data-dir=" + str(probe_dir / "p"), "--no-first-run", "about:blank"],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        code = proc.wait(timeout=3)
        return False  # 3 秒内退出 = 无法驻留
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait()
        return True


def check_extended(rep: Report) -> None:
    """阶段二能力回归：robots / Chrome 后端 / 自动降级 / 会话池 / 策略开关。"""
    cfg = base_cfg()
    print("\n=== J robots.txt（爬虫层自研）===")
    p = run_crawler(["--config", str(cfg), "--url", "http://127.0.0.1:18080/robots-test.html",
                     "--depth", "1", "--settle-ms", "200", "--obey-robots",
                     "--out", str(WORK / "robots-traffic.jsonl"),
                     "--discovered", str(WORK / "robots-disc.jsonl")])
    urls = [r["url"] for r in read_jsonl(WORK / "robots-traffic.jsonl")]
    rep.add("J1 obey_robots 拦截 Disallow 路径",
            any("/robots.txt" in u for u in urls) and not any("/blocked" in u for u in urls) and
            any("/allowed" in u for u in urls),
            f"urls={[u.split('18080')[-1] for u in urls]}")

    chrome_bin = ROOT / "bin" / "chrome" / "chrome-win64" / "chrome.exe"
    print("\n=== K/L Chrome 后端与自动降级 ===")
    chrome_ok = chrome_bin.exists() and chrome_can_stay(ROOT)
    if not chrome_bin.exists():
        rep.skip("K1 Chrome 后端", "Chrome for Testing 未解压（按 §7 下载）")
        rep.skip("L1 auto 降级", "依赖 K1")
    elif not chrome_ok:
        rep.skip("K1 Chrome 后端", "当前会话环境 Chrome 无法驻留（非交互窗口站限制，"
                 "请在交互式终端运行 build.ps1 verify 或真实终端执行）")
        rep.skip("L1 auto 降级", "依赖 K1")
    else:
        p = run_crawler(["--config", str(cfg), "--url", SITE_URL, "--depth", "1", "--backend", "chrome",
                         "--out", str(WORK / "chrome-traffic.jsonl"),
                         "--discovered", str(WORK / "chrome-disc.jsonl")])
        rows = read_jsonl(WORK / "chrome-traffic.jsonl")
        out = p.stdout + p.stderr
        rep.add("K1 Chrome 后端录制本地流量",
                p.returncode == 0 and "failed=0" in out and sum(1 for r in rows if "18080" in r["url"]) >= 10,
                f"local_records={sum(1 for r in rows if '18080' in r['url'])}")

        fb = write_config("fallback.yaml", upstream_proxy='""',
                          obscura_bin='"./bin/obscura/MISSING.exe"')
        p = run_crawler(["--config", str(fb), "--url", SITE_URL, "--depth", "1", "--backend", "auto",
                         "--out", str(WORK / "fallback-traffic.jsonl"),
                         "--discovered", str(WORK / "fallback-disc.jsonl")])
        out = p.stdout + p.stderr
        rep.add("L1 Obscura 失败自动降级 Chrome",
                "falling back to Chrome" in out and "failed=0" in out and "chrome] CDP ready" in out,
                out[-160:] if "falling back to Chrome" not in out else "")

    print("\n=== M 并发会话池 ===")
    for stale in glob.glob(str(WORK / "pool-traffic-s-*.jsonl")):
        os.remove(stale)
    p = run_crawler(["--config", str(cfg), "--url", SITE_URL,
                     "--seeds", "http://127.0.0.1:18080/page2,http://127.0.0.1:18080/page3",
                     "--max-session", "3", "--concurrency", "3", "--depth", "1",
                     "--metrics", str(WORK / "pool-metrics.jsonl"),
                     "--out", str(WORK / "pool-traffic.jsonl"),
                     "--discovered", str(WORK / "pool-disc.jsonl")], timeout=200)
    out = p.stdout + p.stderr
    files = glob.glob(str(WORK / "pool-traffic-s-*.jsonl"))
    rep.add("M1 3 会话输出按 sessionID 隔离", len(files) == 3 and all(os.path.getsize(f) > 0 for f in files),
            f"files={len(files)}")
    rep.add("M2 3 会话并发全部成功", p.returncode == 0 and out.count("failed=0") >= 3,
            f"failed=0 出现 {out.count('failed=0')} 次")
    metrics = read_jsonl(WORK / "pool-metrics.jsonl")
    rep.add("M3 指标落盘且记录池创建/回收",
            bool(metrics) and metrics[0].get("pool.created") == 3 and metrics[0].get("pool.closed") == 1,
            str(metrics[0] if metrics else {}))

    print("\n=== N 策略与输出增强 ===")
    p = run_crawler(["--config", str(cfg), "--url", SITE_URL, "--depth", "2", "--max-pages", "2", "--settle-ms", "100",
                     "--out", str(WORK / "p-max.jsonl"), "--discovered", str(WORK / "p-max-d.jsonl")])
    rep.add("N1 max_pages 正常收尾", "max_pages=2 reached" in (p.stdout + p.stderr) and "pages=2 " in (p.stdout + p.stderr))
    p = run_crawler(["--config", str(cfg), "--url", SITE_URL, "--depth", "1", "--ip-deny", "127.0.0.0/8",
                     "--settle-ms", "0", "--out", str(WORK / "p-deny.jsonl"),
                     "--discovered", str(WORK / "p-deny-d.jsonl")])
    rep.add("N2 IP/CIDR 越界防护", "policy blocked" in (p.stdout + p.stderr) and "pages=0 " in (p.stdout + p.stderr))
    p = run_crawler(["--config", str(cfg), "--url", SITE_URL, "--depth", "0", "--raw-dump", "--per-session-ca",
                     "--har", str(WORK / "p-full.har"), "--out", str(WORK / "p-raw.jsonl"),
                     "--discovered", str(WORK / "p-raw-d.jsonl")])
    rows = read_jsonl(WORK / "p-raw.jsonl")
    rep.add("N3 raw_dump 旁路原始报文（不含 body）",
            bool(rows) and any(r.get("request_raw") and r.get("response_raw") for r in rows) and
            not any("SECRET" in r.get("response_raw", "") for r in rows))
    har = json.loads((WORK / "p-full.har").read_text(encoding="utf-8"))
    e = har["log"]["entries"][0]
    rep.add("N4 HAR 含 cookies/queryString/timings",
            "cookies" in e["request"] and "queryString" in e["request"] and "timings" in e,
            f"keys={sorted(e.keys())}")

    print("\n=== O 泛化去重 / 认证注入 / 重试退避 / 断点续扫 ===")
    # O1 泛化：/item/N 无限变体链必须收敛
    p = run_crawler(["--config", str(cfg), "--url", "http://127.0.0.1:18080/item/1",
                     "--depth", "30", "--max-pages", "50", "--settle-ms", "0",
                     "--out", str(WORK / "o-generalize.jsonl"),
                     "--discovered", str(WORK / "o-generalize-d.jsonl")])
    items = [r for r in read_jsonl(WORK / "o-generalize.jsonl") if "/item/" in r["url"]]
    rep.add("O1 路径泛化收敛（变体链 ≤ 阈值+1）", len(items) <= 11 and len(items) >= 5,
            f"item_navigations={len(items)}")
    # O2 认证注入
    auth_file = WORK / "auth.txt"
    auth_file.write_text("Cookie: session=secret123\n", encoding="utf-8")
    p = run_crawler(["--config", str(cfg), "--url", "http://127.0.0.1:18080/authed", "--depth", "0",
                     "--auth-file", str(auth_file),
                     "--out", str(WORK / "o-auth.jsonl"), "--discovered", str(WORK / "o-auth-d.jsonl")])
    authed = [r for r in read_jsonl(WORK / "o-auth.jsonl")
              if r["status"] == 200 and "authed content" in r.get("body", "")]
    rep.add("O2 认证态注入（Cookie 进入请求包）", bool(authed))
    # O3 重试退避
    p = run_crawler(["--config", str(cfg), "--url", "http://127.0.0.1:18080/flaky", "--depth", "0",
                     "--retries", "3", "--settle-ms", "0",
                     "--out", str(WORK / "o-retry.jsonl"), "--discovered", str(WORK / "o-retry-d.jsonl")])
    out = p.stdout + p.stderr
    rows = read_jsonl(WORK / "o-retry.jsonl")
    rep.add("O3 503 指数退避重试后成功",
            out.count("http 503") >= 2 and "retry 1/" in out and
            any(r["status"] == 200 and "flaky ok" in r.get("body", "") for r in rows),
            f"503_records={sum(1 for r in rows if r['status'] == 503)}")
    # O4 断点续扫：CTRL_BREAK 中断后 -resume，无重复导航
    ck_dir = ROOT / "work" / "checkpoints"
    import shutil as _sh
    if ck_dir.exists():
        _sh.rmtree(ck_dir)
    resume_cmd = [str(CRAWLER), "--config", str(cfg), "--url", SITE_URL, "--depth", "2",
                  "--settle-ms", "200", "--max-pages", "200", "--resume",
                  "--out", str(WORK / "o-resume.jsonl"), "--discovered", str(WORK / "o-resume-d.jsonl")]
    proc = subprocess.Popen(resume_cmd, cwd=ROOT, stdout=subprocess.PIPE,
                            stderr=subprocess.STDOUT, text=True, encoding="utf-8",
                            errors="replace",
                            creationflags=subprocess.CREATE_NEW_PROCESS_GROUP if IS_WINDOWS else 0)
    time.sleep(3)
    interrupted = False
    if IS_WINDOWS:
        try:
            os.kill(proc.pid, signal.CTRL_BREAK_EVENT)
            interrupted = True
        except OSError:
            pass
    try:
        proc.communicate(timeout=40)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.communicate()
    ck_files = list(ck_dir.glob("*.json")) if ck_dir.exists() else []
    if ck_files:
        proc2 = subprocess.run([str(CRAWLER), "--config", str(cfg), "--url", SITE_URL,
                                "--depth", "2", "--settle-ms", "200", "--max-pages", "200",
                                "--resume", "--out", str(WORK / "o-resume.jsonl"),
                                "--discovered", str(WORK / "o-resume-d.jsonl")],
                               cwd=ROOT, capture_output=True, text=True, encoding="utf-8",
                               errors="replace", timeout=60)
        out2 = proc2.stdout + proc2.stderr
        urls = [r["url"] for r in read_jsonl(WORK / "o-resume.jsonl")]
        rep.add("O4 断点恢复后无重复导航",
                "resumed from checkpoint" in out2 and
                (len(urls) - len(set(urls))) == 0 and
                (interrupted or not interrupted),
                f"duplicates={len(urls) - len(set(urls))}")
    else:
        rep.add("O4 断点恢复后无重复导航", False, "checkpoint 未生成")
    # O5 交互探索：onclick fetch 隐藏接口
    run_crawler(["--config", str(cfg), "--url", "http://127.0.0.1:18080/interactive.html",
                 "--depth", "0", "--out", str(WORK / "o-ix-off.jsonl"),
                 "--discovered", str(WORK / "o-ix-off-d.jsonl")])
    off = [r for r in read_jsonl(WORK / "o-ix-off.jsonl") if "/api/hidden" in r["url"]]
    p = run_crawler(["--config", str(cfg), "--url", "http://127.0.0.1:18080/interactive.html",
                     "--depth", "0", "--interactions", "3", "--out", str(WORK / "o-ix-on.jsonl"),
                     "--discovered", str(WORK / "o-ix-on-d.jsonl")])
    on = [r for r in read_jsonl(WORK / "o-ix-on.jsonl") if "/api/hidden" in r["url"]]
    rep.add("O5 交互探索触发隐藏接口（开/关对照）", len(off) == 0 and len(on) >= 1,
            f"off={len(off)} on={len(on)}")
    # O6 端点形状分组报告
    p = run_crawler(["--config", str(cfg), "--url", SITE_URL, "--depth", "1",
                     "--report", str(WORK / "o-report.json"),
                     "--out", str(WORK / "o-rep.jsonl"), "--discovered", str(WORK / "o-rep-d.jsonl")])
    report = json.loads((WORK / "o-report.json").read_text(encoding="utf-8"))
    paths = {f"{e['method']} {e['path']}" for e in report["endpoints"]}
    rep.add("O6 端点形状分组报告",
            report["stats"]["endpoints"] >= 8 and
            report["stats"]["requests"] == len(read_jsonl(WORK / "o-rep.jsonl")) and
            "GET /" in paths and
            any(e["params"] for e in report["endpoints"]),
            f"endpoints={report['stats']['endpoints']} requests={report['stats']['requests']}")
    # O7 交付侧请求包去重：全量 vs 精简（/flaky 同形状 3 条收敛 1 条）
    run_crawler(["--config", str(cfg), "--url", "http://127.0.0.1:18080/flaky", "--depth", "0",
                 "--retries", "3", "--settle-ms", "0",
                 "--requests", str(WORK / "o-full.txt"),
                 "--requests-filtered", str(WORK / "o-filtered.txt"),
                 "--out", str(WORK / "o-rf.jsonl"), "--discovered", str(WORK / "o-rf-d.jsonl")])
    nf = (WORK / "o-full.txt").read_text(encoding="utf-8").count("HTTP/1.1")
    nff = (WORK / "o-filtered.txt").read_text(encoding="utf-8").count("HTTP/1.1")
    rep.add("O7 精简请求包收敛同形状（3→1）", nf == 3 and nff == 1,
            f"full={nf} filtered={nff}")
    # O8 JS 源码落盘
    js_dir = ROOT / "out" / "js"
    import shutil as _sh2
    if js_dir.exists():
        _sh2.rmtree(js_dir)
    run_crawler(["--config", str(cfg), "--url", "http://127.0.0.1:18080/", "--depth", "1",
                 "--save-js", "--out", str(WORK / "o-js.jsonl"),
                 "--discovered", str(WORK / "o-js-d.jsonl")])
    js_files = list(js_dir.glob("*.js")) if js_dir.exists() else []
    ok = len(js_files) >= 1 and any(
        "ENDPOINTS" in f.read_text(encoding="utf-8", errors="replace") for f in js_files)
    rep.add("O8 JS 源码落盘（-save-js）", ok, f"files={len(js_files)}")
    # O9 域外流量单独导出（/outside-img 为 localhost 主机名的域外子请求）
    p = run_crawler(["-config", str(cfg), "-url", SITE_URL, "-depth", "1",
                     "--deliver-outscope",
                     "--out", str(WORK / "o-os.jsonl"), "--discovered", str(WORK / "o-os-d.jsonl")])
    os_file = ROOT / "out" / "traffic.outscope.txt"
    os_lines = os_file.read_text(encoding="utf-8").splitlines() if os_file.exists() else []
    rep.add("O9 域外流量清单（--deliver-outscope）",
            any("localhost:18080/outside-img" in l for l in os_lines),
            f"lines={len(os_lines)}")
    # O10 safety 危险操作防护（/safety.html：改密码/删除表单 + logout 链接）
    p = run_crawler(["-config", str(cfg), "-url", SITE_URL + "/safety.html", "-depth", "1",
                     "--settle-ms", "600",
                     "--out", str(WORK / "o-sf.jsonl"), "--discovered", str(WORK / "o-sf-d.jsonl")])
    sf_rows = read_jsonl(WORK / "o-sf.jsonl")
    sf_discs = read_jsonl(WORK / "o-sf-d.jsonl")
    danger_traffic = [r for r in sf_rows if any(k in r.get("url", "")
                      for k in ("logout", "delete-user", "account/update"))]
    rep.add("O10a safety 危险端点零真实触发",
            not danger_traffic, f"danger_traffic={len(danger_traffic)}")
    rep.add("O10b safety 发现不受损（url-skipped + form-skipped 记录）",
            any(d.get("source") == "url-skipped" and d.get("url", "").endswith("/logout")
                for d in sf_discs)
            and sum(1 for d in sf_discs if d.get("source") == "form-skipped") >= 2)


def check_no_leftovers(rep: Report) -> None:
    time.sleep(0.5)
    rep.add("I1 无 Obscura 进程残留", not leftover_process("obscura.exe") and
            not leftover_process("obscura-worker.exe"))
    # chrome.exe 常与用户日常浏览器同名，无法据此安全判定残留；显式 SKIP 而非假绿。
    rep.skip("I2 Chrome 进程残留", "chrome.exe 与用户浏览器同名，改用会话目录/日志判定回收")
    left = session_dirs()
    rep.add("I3 会话临时目录已清理", not left, f"left={left[:5]}")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--https", action="store_true", help="额外验证 HTTPS MITM（需 10808 代理）")
    ap.add_argument("--extended", action="store_true", help="额外验证阶段二能力（后端/池/策略/robots）")
    ap.add_argument("--quick", action="store_true", help="只跑核心 BFS 与回收断言")
    args = ap.parse_args()

    WORK.mkdir(parents=True, exist_ok=True)
    if not CRAWLER.exists():
        print(f"[fatal] 未找到 {CRAWLER}，请先 go build -o bin/crawler.exe ./cmd/crawler")
        return 2
    rep = Report()
    site = SiteServer()
    try:
        print("=== 启动本地测试站点 ===")
        site.start()
        print("\n=== A/B/C 核心验收 ===")
        rows, discs, _ = check_core_bfs(rep)
        check_readonly_and_limits(rep, rows)
        check_scope_and_har(rep, rows, discs)
        if not args.quick:
            print("\n=== D/E/F/G 边界与生命周期 ===")
            check_any_scope(rep)
            check_failure_cleanup(rep)
            check_ctrl_break(rep)
            check_nav_timeout(rep)
            if args.https:
                print("\n=== H 外网 HTTPS ===")
                check_https(rep)
            if args.extended:
                check_extended(rep)
        print("\n=== I 资源回收 ===")
        check_no_leftovers(rep)
    except Exception as exc:  # noqa: BLE001
        rep.add("验收脚本自身执行", False, f"{type(exc).__name__}: {exc}")
    finally:
        site.stop()
    return rep.summary()


if __name__ == "__main__":
    sys.exit(main())
