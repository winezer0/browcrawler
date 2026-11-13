#!/usr/bin/env python3
"""连续稳定性观察：反复运行 crawler，检查失败率、进程残留与会话目录累积。

用法：
    python scripts/stability.py --minutes 11.5
    python scripts/stability.py --minutes 3 --url http://127.0.0.1:18080/

自动管理测试站点生命周期（端口空闲时启动，结束时停止）。
结果写入 work/stability-result.json，逐轮日志写入 work/stability.log。
退出码 0 = 全部通过；1 = 出现失败/残留。
"""
from __future__ import annotations

import argparse
import glob
import json
import os
import socket
import subprocess
import sys
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
WORK = ROOT / "work"
LOG = WORK / "stability.log"
RESULT = WORK / "stability-result.json"


def log(msg: str) -> None:
    with open(LOG, "a", encoding="utf-8") as fh:
        fh.write(f"{time.strftime('%H:%M:%S')} {msg}\n")


def port_open(port: int, host: str = "127.0.0.1", timeout: float = 0.4) -> bool:
    try:
        with socket.create_connection((host, port), timeout):
            return True
    except OSError:
        return False


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--minutes", type=float, default=11.5, help="观察时长（分钟），默认 11.5 覆盖 '>10 分钟' 口径")
    ap.add_argument("--url", default="http://127.0.0.1:18080/", help="被测 seed URL")
    ap.add_argument("--depth", type=int, default=1)
    ap.add_argument("--settle-ms", type=int, default=0)
    args = ap.parse_args()

    WORK.mkdir(parents=True, exist_ok=True)
    crawler = ROOT / "bin" / "crawler.exe"
    site_py = ROOT / "testdata" / "site.py"
    if not crawler.exists():
        print("[fatal] bin/crawler.exe 不存在，请先构建")
        return 2

    site = None
    if "127.0.0.1:18080" in args.url and not port_open(18080):
        site = subprocess.Popen([sys.executable, str(site_py)], cwd=ROOT,
                                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        for _ in range(40):
            if port_open(18080):
                break
            time.sleep(0.15)

    start = time.time()
    rounds, failures = 0, []
    # 固定输出文件名循环复用（crawler 每轮 os.Create 截断重写）。
    # 不逐轮删除：宿主安全删除钩子有 50 次累计阈值，逐轮删会在 ~25 轮时触发
    # SAFE_DELETE_BULK_CONFIRM_REQUIRED 中断观察。
    out = WORK / "stab-current.jsonl"
    disc = WORK / "stab-current-d.jsonl"
    try:
        while time.time() - start < args.minutes * 60:
            n = rounds + 1
            ok, detail = False, ""
            try:
                p = subprocess.run(
                    [str(crawler), "--url", args.url, "--depth", str(args.depth),
                     "--settle-ms", str(args.settle_ms), "--out", str(out), "--discovered", str(disc)],
                    cwd=ROOT, capture_output=True, text=True, encoding="utf-8",
                    errors="replace", timeout=90)
                detail = (p.stdout + p.stderr)[-200:]
                ok = p.returncode == 0 and "failed=0" in p.stdout and "exited gracefully" in p.stdout
            except Exception as exc:  # noqa: BLE001
                detail = f"exception: {type(exc).__name__}: {exc}"
            leftovers = glob.glob(str(ROOT / "work" / "sessions" / "*"))
            if not ok:
                failures.append(f"round {n}: {detail}")
                log(f"round {n} FAIL {detail}")
            elif leftovers:
                failures.append(f"round {n} leftover session dirs: {leftovers[:3]}")
                log(f"round {n} LEFTOVER {leftovers[:3]}")
            else:
                log(f"round {n} ok")
                if n % 50 == 0:
                    print(f"progress: {n} rounds ok", flush=True)
            rounds += 1
            if failures:
                break
            time.sleep(0.5)
    finally:
        procs = subprocess.run(["tasklist", "/FI", "IMAGENAME eq obscura.exe"],
                               capture_output=True, text=True, errors="replace").stdout
        result = {
            "elapsed_seconds": round(time.time() - start, 1),
            "rounds": rounds,
            "failures": failures[:5],
            "session_dirs_left": glob.glob(str(ROOT / "work" / "sessions" / "*")),
            "obscura_process_left": "obscura.exe" in procs.lower(),
        }
        with open(RESULT, "w", encoding="utf-8") as fh:
            json.dump(result, fh, ensure_ascii=False, indent=2)
        print(json.dumps(result, ensure_ascii=False), flush=True)
        if site is not None:
            site.terminate()
            try:
                site.wait(timeout=5)
            except subprocess.TimeoutExpired:
                site.kill()
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
