#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""Talks to a Syntaxis admin app from inside a test VM, as an admin would from the browser.

  adminapi.py setup  <token> <password>        use the single-use setup link's token
  adminapi.py login  <email> <password>        sign in; sets up 2FA the first time
  adminapi.py get    </path>                   GET /api/path (after login), prints the JSON
  adminapi.py post   </path> [json]            POST, with the CSRF token
  adminapi.py put    </path> <json>
  adminapi.py delete </path>

State (cookies, the CSRF token, the 2FA secret) lives in ~/.syx-admin. The admin address is
SYNTAXIS_ADMIN_URL or http://127.0.0.1:8081. Exits non-zero with the HTTP status for an error.
"""
import base64
import hashlib
import hmac
import http.cookiejar
import json
import os
import struct
import sys
import time
import urllib.error
import urllib.request

BASE = os.environ.get("SYNTAXIS_ADMIN_URL", "http://127.0.0.1:8081").rstrip("/")
STATE = os.path.expanduser("~/.syx-admin")
os.makedirs(STATE, exist_ok=True)
JAR = http.cookiejar.MozillaCookieJar(os.path.join(STATE, "cookies.txt"))
try:
    JAR.load(ignore_discard=True, ignore_expires=True)
except FileNotFoundError:
    pass
OPENER = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(JAR))


def csrf() -> str:
    try:
        return open(os.path.join(STATE, "csrf")).read().strip()
    except FileNotFoundError:
        return ""


def save_csrf(v: str) -> None:
    if v:
        open(os.path.join(STATE, "csrf"), "w").write(v)


def call(method: str, path: str, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(BASE + "/api" + path, data=data, method=method)
    req.add_header("Origin", BASE)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    if method != "GET" and csrf():
        req.add_header("X-CSRF-Token", csrf())
    try:
        with OPENER.open(req, timeout=60) as r:
            raw = r.read().decode()
            code = r.status
    except urllib.error.HTTPError as e:
        raw, code = e.read().decode(), e.code
    JAR.save(ignore_discard=True, ignore_expires=True)
    try:
        parsed = json.loads(raw) if raw else {}
    except ValueError:
        parsed = {"raw": raw}
    if isinstance(parsed, dict) and parsed.get("csrf"):
        save_csrf(parsed["csrf"])
    return code, parsed


def totp(secret: str, at=None) -> str:
    key = base64.b32decode(secret.upper() + "=" * (-len(secret) % 8))
    counter = int((at or time.time()) // 30)
    mac = hmac.new(key, struct.pack(">Q", counter), hashlib.sha1).digest()
    off = mac[-1] & 0x0F
    return "%06d" % ((struct.unpack(">I", mac[off:off + 4])[0] & 0x7FFFFFFF) % 1000000)


def fresh_code(secret: str) -> str:
    # a code is accepted once: wait for the next 30 second window if this one was already used
    marker = os.path.join(STATE, "last_counter")
    counter = int(time.time() // 30)
    try:
        if int(open(marker).read()) >= counter:
            time.sleep(31 - (time.time() % 30))
            counter = int(time.time() // 30)
    except (FileNotFoundError, ValueError):
        pass
    open(marker, "w").write(str(counter))
    return totp(secret)


def fail(code, body):
    print(json.dumps(body))
    print("HTTP %s" % code, file=sys.stderr)
    sys.exit(1)


def login(email: str, password: str) -> None:
    code, m = call("POST", "/login", {"email": email, "password": password})
    if code != 200:
        fail(code, m)
    secret_file = os.path.join(STATE, "totp-secret")
    if m.get("needs_mfa"):
        secret = open(secret_file).read().strip()
        code, m = call("POST", "/login/mfa", {"code": fresh_code(secret)})
        if code != 200:
            fail(code, m)
    elif m.get("must_setup_2fa") or not os.path.exists(secret_file):
        # first sign-in: 2FA is mandatory for an admin
        code, b = call("POST", "/2fa/begin")
        if code != 200:
            fail(code, b)
        open(secret_file, "w").write(b["secret"])
        os.chmod(secret_file, 0o600)
        code, c = call("POST", "/2fa/confirm", {"code": fresh_code(b["secret"])})
        if code != 200:
            fail(code, c)
        print(json.dumps({"recovery_codes": c.get("recovery_codes", [])}))
    code, me = call("GET", "/me")
    if code != 200:
        fail(code, me)
    save_csrf(me.get("csrf", ""))
    print(json.dumps({"signed_in": me.get("email", email)}))


def main(argv):
    if len(argv) < 2:
        print(__doc__)
        return 2
    cmd = argv[1]
    if cmd == "setup":
        code, m = call("POST", "/setup", {"token": argv[2], "password": argv[3]})
        if code != 200:
            fail(code, m)
        print(json.dumps(m))
    elif cmd == "login":
        login(argv[2], argv[3])
    elif cmd in ("get", "post", "put", "delete"):
        body = json.loads(argv[3]) if len(argv) > 3 else None
        if cmd in ("post", "put") and body is None:
            body = {}
        code, m = call(cmd.upper(), argv[2], body)
        print(json.dumps(m))
        if code >= 400:
            print("HTTP %s" % code, file=sys.stderr)
            return 1
    else:
        print(__doc__)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
