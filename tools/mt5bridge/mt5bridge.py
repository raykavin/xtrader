"""mt5bridge serves a MetaTrader 5 terminal to xtrader over a local HTTP JSON API.

It runs with the Windows Python of the terminal (under Wine on Linux) and uses the
official MetaTrader5 package, which talks to the terminal already logged in:

    wine python.exe mt5bridge.py --login 12345678 --server Exness-MT5Trial11

The terminal keeps the password (saved at its login), or the bridge logs it in with
the one of MT5_PASSWORD, read from the environment, never from the command line,
eg: a terminal installed afresh in a container. It listens on 127.0.0.1 unless
--host says otherwise, then only with a token (--token, or MT5_BRIDGE_TOKEN),
refuses a real account without --allow-real, and refuses every trading request
with --read-only. --portable keeps the data of the terminal in its folder (its
/portable mode). Times are Unix seconds (candles, deals) or milliseconds (ticks),
as the terminal reports them. Connections persist (HTTP/1.1): a body comes with
its Content-Length, never chunked. With orjson installed, it encodes the answers.

Endpoints (JSON):
    GET  /health                       terminal and account state, the instance of the terminal
    GET  /account                      account_info
    GET  /symbol?symbol=XAUUSDm        symbol_info (selects the symbol)
    GET  /tick?symbol=XAUUSDm          symbol_info_tick
    GET  /rates?symbol=&timeframe=M5&count=N[&start=0]      latest candles
    GET  /rates?symbol=&timeframe=M5&from=<s>&to=<s>         candles of a range
    GET  /positions[?symbol=]          open positions
    GET  /orders[?symbol=]             pending orders
    GET  /deals?from=<s>&to=<s>        deals of the history
    GET  /history_orders?from=<s>&to=<s>   orders of the history
    POST /order                        order_send, body: the MT5 trade request
    POST /check                        order_check, body: the MT5 trade request
    GET  /session                      the account the bridge serves, never its password
    POST /session                      the account to serve, from the controller (see below)
Errors: {"error": "...", "retcode": n} with an HTTP status. A list the package
fails to read (None) is an error (502), never an empty list. Candles are the
exception for a range the terminal holds none of, eg: before the start of its
history or beyond its "Max. bars in chart", which copy_rates_* refuse (None)
as the documentation of CopyRates says: when the latest candle of the symbol
reads, the range is outside the history, answered empty as a range without
candles, and only otherwise an error. The candles by position are read within
the "Max. bars in chart" of the terminal (terminal_info().maxbars).

The terminal serves one request at a time. Identical reads (a GET of the same
route and query, eg: every agent polling /positions of the account) waiting for
it together are served by one call (see Flights): an answer is never read
before its request arrived. Trading requests are never shared.

The terminal serves one request at a time. A request with a deadline waits for
it until then at most, and POST /order is never sent to the terminal after it,
eg: while the terminal reconnects: the client gave up by then. The deadline is
the earliest of the headers X-Bridge-Deadline (Unix milliseconds, by the clock
of the client) and X-Bridge-Timeout (milliseconds from the receipt of the
request, by the clock of the bridge, so that a clock of the bridge behind the
client's, eg: on another host, never sends a request later), a minute from
now at most, and is counted by the monotonic clock. POST /order also
checks the account before order_send: the terminal logged in another account
than --login, or a real one without --allow-real, eg: switched by hand, refuses
it (409). A POST /order refused before order_send answers "sent": false (504
for its deadline passed), so the client knows it was not executed; without it,
an error after order_send (500, 502, 503) leaves the outcome unknown.

The account comes from the controller (Configurações > Contas MT5): POST /session
{login, server, password, allow_real, read_only} logs the terminal in it
(mt5.login, or initialize when the terminal isn't open) and, once it is, keeps
it in --session, a JSON file without the password (the terminal keeps its own),
taken at the next start over --login/--server, which only serve until the
controller first gives one. Without any, the bridge serves all the same: /health
answers 200 telling it waits for the session (or for the password, when the
password the terminal kept no longer logs it in), and the other routes 503.
"""

import argparse
import datetime as dt
import hmac
import ipaddress
import json
import math
import os
import secrets
import subprocess
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, parse_qsl, urlparse

import MetaTrader5 as mt5

try:
    import orjson
except ImportError:
    orjson = None

TIMEFRAMES = {
    "M1": mt5.TIMEFRAME_M1, "M3": mt5.TIMEFRAME_M3, "M5": mt5.TIMEFRAME_M5,
    "M10": mt5.TIMEFRAME_M10, "M15": mt5.TIMEFRAME_M15, "M30": mt5.TIMEFRAME_M30,
    "H1": mt5.TIMEFRAME_H1, "H2": mt5.TIMEFRAME_H2, "H4": mt5.TIMEFRAME_H4,
    "D1": mt5.TIMEFRAME_D1, "W1": mt5.TIMEFRAME_W1, "MN1": mt5.TIMEFRAME_MN1,
}

# the fields of a trade request the bridge forwards to order_send
REQUEST_FIELDS = {
    "action", "magic", "order", "symbol", "volume", "price", "stoplimit", "sl", "tp",
    "deviation", "type", "type_filling", "type_time", "expiration", "comment",
    "position", "position_by",
}

# the types order_send takes, which JSON doesn't keep: a float 0.0 comes as the
# integer 0 (eg: from Go), which order_send refuses ("Invalid "sl" argument")
FLOAT_FIELDS = {"volume", "price", "stoplimit", "sl", "tp"}
INT_FIELDS = {"action", "magic", "order", "deviation", "type", "type_filling", "type_time", "expiration",
              "position", "position_by"}

MAX_RATES = 100000

# the largest body of a request read, in bytes
MAX_BODY = 1 << 20

# the headers of the deadline of a request: in Unix milliseconds, and in
# milliseconds from its receipt; and the furthest one taken, in seconds from now
DEADLINE_HEADER = "X-Bridge-Deadline"
TIMEOUT_HEADER = "X-Bridge-Timeout"
MAX_DEADLINE = 60

# the code of last_error() after a call without error (RES_S_OK of the package)
RES_S_OK = 1


class BridgeError(Exception):
    """An error answer: its HTTP status, and the last_error code of the terminal
    when it has one; sent is False for a trade request refused before
    order_send, None when unknown or not a trade request."""

    def __init__(self, status, message, retcode=None, sent=None):
        super().__init__(message)
        self.status = status
        self.retcode = retcode
        self.sent = sent


def check_deadline(deadline, what):
    """Refuses (504) to go on past deadline, a time.monotonic() or None for none."""
    if deadline is not None and time.monotonic() >= deadline:
        raise BridgeError(504, "the deadline of the request passed %s: not sent to the terminal" % what)


def load_session(path):
    """The account of the session file path, None without one."""
    if not path or not os.path.exists(path):
        return None
    with open(path, encoding="utf-8") as f:
        data = json.load(f)
    return session_of(data)


def save_session(path, session):
    """Keeps session in path, without the password, atomically, readable by its
    owner only."""
    if not path:
        return
    os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
    temporary = path + ".tmp"
    with open(os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600), "w", encoding="utf-8") as f:
        json.dump({k: session[k] for k in ("login", "server", "allow_real", "read_only")}, f)
    os.replace(temporary, path)


def instance_of(session_path):
    """The instance of the terminal: an identifier made once and kept next to
    the session file, the same across the restarts of its container, apart
    from any other terminal, on the same host too; one of the process without
    a session file."""
    if not session_path:
        return secrets.token_hex(8)
    path = os.path.join(os.path.dirname(session_path) or ".", "instance")
    try:
        with open(path, encoding="utf-8") as f:
            instance = f.read().strip()
        if instance:
            return instance
    except FileNotFoundError:
        pass
    instance = secrets.token_hex(8)
    os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
    temporary = path + ".tmp"
    with open(temporary, "w", encoding="utf-8") as f:
        f.write(instance)
    os.replace(temporary, path)
    return instance


def legacy_session(args):
    """The account of --login/--server, the legacy one, None without them."""
    login, server = getattr(args, "login", None), getattr(args, "server", "")
    if not login or not server:
        return None
    return {"login": int(login), "server": server, "allow_real": bool(getattr(args, "allow_real", False)),
            "read_only": bool(getattr(args, "read_only", False)), "legacy": True}


def session_of(data):
    """The account of a JSON object: login (an integer above 0), server, and the
    switches allow_real and read_only; a BridgeError (400) else."""
    if not isinstance(data, dict):
        raise BridgeError(400, "the session must be an object")
    login, server = data.get("login"), data.get("server")
    if isinstance(login, bool) or not isinstance(login, int) or login <= 0:
        raise BridgeError(400, "login must be an integer above 0")
    if not isinstance(server, str) or not server.strip():
        raise BridgeError(400, "server is required")
    for name in ("allow_real", "read_only"):
        if not isinstance(data.get(name, False), bool):
            raise BridgeError(400, "%s must be true or false" % name)
    return {"login": login, "server": server.strip(), "allow_real": bool(data.get("allow_real", False)),
            "read_only": bool(data.get("read_only", False))}


class Terminal:
    """The terminal, used by one thread at a time: the MetaTrader5 package keeps a
    single connection per process."""

    def __init__(self, args):
        self.args = args
        self.lock = threading.Lock()
        self.connected = False
        self.process = None  # the terminal the bridge opened, if any
        self.info = None  # terminal_info() of the request under the lock
        self.selected = set()  # the symbols selected in the Market Watch since connecting
        # the account served: the one of --session, else of --login/--server
        # (legacy), else none until POST /session; its password in memory only
        self.session = load_session(getattr(args, "session", "")) or legacy_session(args)
        self.password = os.environ.get("MT5_PASSWORD", "") if self.session and self.session.get("legacy") else ""
        self.waiting = "" if self.session else "session"  # why no account is served: session or password
        self.instance = instance_of(getattr(args, "session", ""))  # tells this terminal apart, see instance_of
        self.flights = Flights()  # the identical reads waiting for the terminal, see Flights

    def read_only(self):
        """Whether the account served refuses every trading request."""
        return bool(self.session and self.session.get("read_only"))

    def launch(self):
        """Opens the terminal of --path, unless the one the bridge opened still
        runs, and gives it --startup seconds to load: initialize opening it
        itself closes it again when it doesn't answer at once, eg: after a boot,
        and so forever. A terminal open already, not by the bridge, takes over:
        the second one exits at once."""
        if not self.args.path or (self.process is not None and self.process.poll() is None):
            return
        print("mt5bridge: opening the terminal, %ds to load" % self.args.startup, flush=True)
        self.process = subprocess.Popen([self.args.path] + (["/portable"] if self.args.portable else []))
        time.sleep(self.args.startup)

    def connect(self):
        if not self.session:
            self.waiting = "session"
            raise BridgeError(503, "no account: set it in Configurações > Contas MT5 (POST /session)")
        self.selected = set()
        self.launch()
        kwargs = {"login": self.session["login"], "server": self.session["server"],
                  "timeout": self.args.timeout * 1000}
        if self.args.path:
            kwargs["path"] = self.args.path
        if self.args.portable:
            kwargs["portable"] = True
        if self.password:
            kwargs["password"] = self.password
        if not mt5.initialize(**kwargs):
            # the password the terminal kept no longer logs it in: the
            # controller gives it again
            self.waiting = "" if self.password else "password"
            raise BridgeError(503, "initialize: %s" % (mt5.last_error(),))
        account = mt5.account_info()
        if account is None:
            mt5.shutdown()
            raise BridgeError(503, "no account: %s" % (mt5.last_error(),))
        problem = self.account_problem(account)
        if problem:
            mt5.shutdown()
            raise BridgeError(503, problem)
        self.connected, self.waiting = True, ""

    def account_problem(self, account, session=None):
        """Why the account the terminal is logged in is not the one to trade
        (of session, the one served by default), None when it is."""
        session = session or self.session
        if not session:
            return "no account: set it in Configurações > Contas MT5"
        if account.login != session["login"]:
            return "the terminal is logged in %d, not %d" % (account.login, session["login"])
        if account.trade_mode == mt5.ACCOUNT_TRADE_MODE_REAL and not session.get("allow_real"):
            return "the account %d is real: allow the real account in Configurações > Contas MT5" % account.login
        return None

    def login(self, session, password):
        """Logs the terminal in the account of session with password (the one
        it kept when empty), served and kept in --session once it is: on the
        terminal open, mt5.login; else initialize. Raises a BridgeError, the
        account served unchanged, when it can't. Under the lock."""
        if self.connected and mt5.terminal_info() is None:
            self.connected = False
            mt5.shutdown()
        if self.connected:
            kwargs = {"server": session["server"], "timeout": self.args.timeout * 1000}
            if password:
                kwargs["password"] = password
            if not mt5.login(session["login"], **kwargs):
                code, message = mt5.last_error()
                raise BridgeError(502, "login %d: %s" % (session["login"], message), code)
        else:
            previous, previous_password = self.session, self.password
            self.session, self.password = session, password
            try:
                self.connect()
            except BridgeError:
                self.session, self.password = previous, previous_password
                raise
        account = mt5.account_info()
        if account is None:
            raise terminal_error("account_info")
        problem = self.account_problem(account, session)
        if problem:
            raise BridgeError(409, problem)
        self.session, self.password, self.waiting, self.connected = session, password, "", True
        self.selected = set()
        save_session(getattr(self.args, "session", ""), session)


    def check_account(self):
        """Refuses a trade request (409) when the terminal is no longer logged in
        the account of --login, or is real without --allow-real, eg: the account
        switched by hand after the bridge connected. Under the lock."""
        account = mt5.account_info()
        if account is None:
            raise terminal_error("account_info")
        problem = self.account_problem(account)
        if problem:
            raise BridgeError(409, "%s: trade request refused" % problem)

    def select(self, symbol):
        """Selects symbol in the Market Watch, once per connection. Under the lock."""
        if symbol in self.selected:
            return True
        if not mt5.symbol_select(symbol, True):
            return False
        self.selected.add(symbol)
        return True

    def reselect(self, symbol):
        """Selects symbol again after a read of it failed, eg: removed from the
        Market Watch by hand, and reports whether it is selected, so worth
        reading again. Under the lock."""
        self.selected.discard(symbol)
        return self.select(symbol)

    def call(self, fn, deadline=None, started=None):
        """Runs fn with the terminal, connecting again when it was lost, eg: the
        terminal closed, which initialize starts again with --path; self.info is
        its terminal_info(). An exception of fn is the terminal lost (503) only
        when terminal_info() no longer answers, else an error of the bridge
        (500). With a deadline (a time.monotonic()), it waits for the terminal
        until then at most, a request behind a slow one or a reconnection, and
        refuses to start fn after it (504). started, if any, is called once the
        terminal is taken, before anything is read (see Flights)."""
        if deadline is None:
            self.lock.acquire()
        elif not self.lock.acquire(timeout=max(deadline - time.monotonic(), 0)):
            raise BridgeError(504, "the deadline of the request passed waiting for the terminal: not sent to it")
        try:
            if started is not None:
                started()
            check_deadline(deadline, "waiting for the terminal")
            self.info = mt5.terminal_info() if self.connected else None
            if self.connected and self.info is None:
                self.connected = False
                mt5.shutdown()
            if not self.connected:
                self.connect()
                check_deadline(deadline, "connecting to the terminal")
                self.info = mt5.terminal_info()
            try:
                return fn()
            except BridgeError:
                raise
            except Exception as exc:
                if mt5.terminal_info() is not None:
                    raise BridgeError(500, "%s: %s" % (type(exc).__name__, exc))
                self.connected = False
                mt5.shutdown()
                raise BridgeError(503, "terminal: %s" % exc)
        finally:
            self.lock.release()


class Flight:
    """A call of the terminal shared by identical reads, see Flights."""

    def __init__(self):
        self.done = threading.Event()
        self.started = False  # the terminal taken: no request joins it any more
        self.result = None
        self.error = None


class Flights:
    """The identical reads waiting for the terminal, served by one call of it.

    A read (a GET of the same route and query) joins the call of an identical
    one queued for the terminal and not started yet, and takes its answer:
    read once the terminal is taken, after every request joined arrived, never
    before. Once the call has the terminal, the next identical read queues a
    call of its own. The agents polling the same account every second then cost
    the terminal a call each round, not one each, and a trading request waits
    behind a few calls rather than behind every copy of them.

    Each request keeps its deadline: one joined gives up at its own, and when
    the call it joined gave up waiting for the terminal (its deadline sooner),
    it queues again while it still has time."""

    def __init__(self):
        self.lock = threading.Lock()
        self.queued = {}  # the call not started of each key

    def run(self, key, call, deadline):
        """The answer of call(started) for key, a call shared by the identical
        reads queued meanwhile; started is to be called once the terminal is
        taken (see Terminal.call). deadline is the one of the request, a
        time.monotonic() or None."""
        while True:
            with self.lock:
                flight = self.queued.get(key)
                leader = flight is None
                if leader:
                    flight = Flight()
                    self.queued[key] = flight
            if leader:
                try:
                    flight.result = call(lambda: self.start(key, flight))
                except BaseException as exc:  # handed to the ones joined too
                    flight.error = exc
                finally:
                    self.start(key, flight, taken=False)
                    flight.done.set()
            else:
                left = None if deadline is None else max(deadline - time.monotonic(), 0)
                if not flight.done.wait(left):
                    raise BridgeError(504, "the deadline of the request passed waiting for the terminal: "
                                           "not sent to it")
            if flight.error is None:
                return flight.result
            if not leader and not flight.started and (deadline is None or time.monotonic() < deadline):
                continue  # the call joined gave up before the terminal: this one still has time
            raise flight.error

    def start(self, key, flight, taken=True):
        """Closes flight to the requests arriving: the terminal taken (taken),
        or the call over without it."""
        with self.lock:
            if taken:
                flight.started = True
            if self.queued.get(key) is flight:
                del self.queued[key]


def dumps(payload):
    """The JSON of an answer, by orjson when installed."""
    if orjson is not None:
        return orjson.dumps(payload)
    return json.dumps(payload, separators=(",", ":")).encode()


def record(value):
    """Converts the named tuples and structures of the package into JSON values."""
    if value is None:
        return None
    if hasattr(value, "_asdict"):
        return {k: record(v) for k, v in value._asdict().items()}
    if isinstance(value, (list, tuple)):
        return [record(v) for v in value]
    if hasattr(value, "item"):  # numpy scalars
        return value.item()
    return value


def listing(what, value):
    """The records the package returned, converted by record out of the lock:
    None is an error of the terminal (502), unless last_error() reports none,
    as for nothing to list. Under the lock, right after the call."""
    if value is None:
        code, message = mt5.last_error()
        if code != RES_S_OK:
            raise BridgeError(502, "%s: %s" % (what, message), code)
        return ()
    return value


def rates_json(rates):
    """The JSON candles of copy_rates_*, converted out of the lock."""
    if len(rates) == 0:
        return []
    names = rates.dtype.names
    return [dict(zip(names, row)) for row in rates.tolist()]


def rates_listing(what, symbol, timeframe, rates):
    """The candles copy_rates_* returned, see listing: None with an error is a
    range outside the history of the terminal, answered empty, when the latest
    candle of the symbol reads, eg: before the start of its history or beyond
    its "Max. bars in chart" (-1 of CopyRates); else the error (502). Under the
    lock, right after the call."""
    if rates is not None:
        return rates
    code, message = mt5.last_error()
    if code == RES_S_OK:
        return ()
    latest = mt5.copy_rates_from_pos(symbol, timeframe, 0, 1)
    if latest is not None and len(latest) > 0:
        return ()
    raise BridgeError(502, "%s: %s" % (what, message), code)


def query_int(query, name, default=None):
    values = query.get(name)
    if not values:
        if default is None:
            raise BridgeError(400, "missing %s" % name)
        return default
    try:
        return int(values[0])
    except ValueError:
        raise BridgeError(400, "invalid %s: %r" % (name, values[0]))


def query_time(query, name):
    """The parameter name, in Unix seconds, as a UTC datetime, refused (400)
    beyond what the platform converts, eg: negative on Windows."""
    seconds = query_int(query, name)
    try:
        return dt.datetime.fromtimestamp(seconds, tz=dt.timezone.utc)
    except (OverflowError, OSError, ValueError):
        raise BridgeError(400, "invalid %s: %d" % (name, seconds))


def query_symbol(query, required=True):
    symbol = (query.get("symbol") or [""])[0]
    if required and not symbol:
        raise BridgeError(400, "missing symbol")
    return symbol


def terminal_error(what):
    code, message = mt5.last_error()
    return BridgeError(502, "%s: %s" % (what, message), code)


def trade_number(field, value):
    """The value of a numeric field of a trade request as order_send takes it,
    refused (400) when not a finite number, or not an integer for INT_FIELDS."""
    if isinstance(value, (int, float)) and not isinstance(value, bool):
        try:
            if field in FLOAT_FIELDS:
                number = float(value)
                if math.isfinite(number):
                    return number
            elif value == int(value):
                return int(value)
        except (OverflowError, ValueError):
            pass
    raise BridgeError(400, "%s must be %s" % (field, "a number" if field in FLOAT_FIELDS else "an integer"))


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    disable_nagle_algorithm = True
    terminal = None
    token = ""
    read_only = False
    deadline = None  # of the request handled, see request_deadline
    started = None  # of the read handled shared, see Flights

    def log_message(self, fmt, *args):
        if self.server.verbose:
            super().log_message(fmt, *args)

    def reply(self, status, payload):
        data = dumps(payload)
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        if self.close_connection:
            self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(data)

    def read_body(self):
        """The body of the request, read before any answer so that the next
        request of the connection starts after it."""
        if self.headers.get("Transfer-Encoding"):
            self.close_connection = True
            raise BridgeError(411, "a Content-Length is required")
        try:
            length = int(self.headers.get("Content-Length") or 0)
        except ValueError:
            length = -1
        if not 0 <= length <= MAX_BODY:
            self.close_connection = True
            raise BridgeError(400, "invalid Content-Length, at most %d" % MAX_BODY)
        return self.rfile.read(length)

    def authorized(self):
        """Whether the request carries the token, compared in constant time:
        as bytes, the header as received (http.server decodes it as latin-1),
        the token as UTF-8, as a Go client sends it."""
        if not self.token:
            return True
        given = self.headers.get("X-Bridge-Token") or ""
        return hmac.compare_digest(given.encode("latin-1", "replace"), self.token.encode("utf-8"))

    def request_deadline(self):
        """The deadline of the request as a time.monotonic(): the earliest of
        X-Bridge-Deadline, by the clock of the client, and X-Bridge-Timeout from
        now, its receipt, MAX_DEADLINE from now at most, or None without
        either."""
        now, wall = time.monotonic(), time.time()
        deadlines = []
        for header, deadline in ((DEADLINE_HEADER, lambda ms: now + ms / 1000 - wall),
                                 (TIMEOUT_HEADER, lambda ms: now + ms / 1000)):
            value = self.headers.get(header)
            if value is None:
                continue
            try:
                deadlines.append(deadline(int(value)))
            except ValueError:
                raise BridgeError(400, "invalid %s: %r" % (header, value))
        if not deadlines:
            return None
        return min(min(deadlines), now + MAX_DEADLINE)

    def run(self, fn):
        """Runs fn with the terminal, within the deadline of the request."""
        return self.terminal.call(fn, self.deadline, self.started)

    def respond(self, method):
        """The payload of the request, or its BridgeError."""
        url = urlparse(self.path)
        raw = self.read_body()
        if not self.authorized():
            raise BridgeError(401, "invalid X-Bridge-Token")
        self.deadline = self.request_deadline()
        route = ROUTES.get((method, url.path))
        if route is None:
            raise BridgeError(404, "no route %s %s" % (method, url.path))
        if method == "GET" and url.path in SHARED:
            key = (url.path, tuple(sorted(parse_qsl(url.query, keep_blank_values=True))))

            def call(started):
                self.started = started
                return route(self, parse_qs(url.query), None)
            return self.terminal.flights.run(key, call, self.deadline)
        body = json.loads(raw or b"{}") if method == "POST" else None
        return route(self, parse_qs(url.query), body)

    def handle_request(self, method):
        try:
            status, payload = 200, self.respond(method)
        except BridgeError as exc:
            status, payload = exc.status, {"error": str(exc)}
            if exc.retcode is not None:
                payload["retcode"] = exc.retcode
            if exc.sent is False:
                payload["sent"] = False
        except json.JSONDecodeError as exc:
            status, payload = 400, {"error": "invalid JSON: %s" % exc}
        except Exception as exc:
            status, payload = 500, {"error": "%s: %s" % (type(exc).__name__, exc)}
        self.reply(status, payload)

    def do_GET(self):
        self.handle_request("GET")

    def do_POST(self):
        self.handle_request("POST")

    # routes

    def health(self, query, body):
        def fn():
            info = self.terminal.info
            account = mt5.account_info()
            version = mt5.version()
            return {
                "connected": bool(info and info.connected),
                "trade_allowed": bool(info and info.trade_allowed),
                "build": version[1] if version else 0,
                "login": account.login if account else 0,
                "server": account.server if account else "",
                "read_only": self.is_read_only(),
                "instance": self.terminal.instance,
                "deadline": True,  # X-Bridge-Deadline is honored
                "timeout": True,  # and X-Bridge-Timeout
                "time": time.time(),
            }
        try:
            return self.run(fn)
        except BridgeError as exc:
            # waiting for the account or its password from the controller: up
            # all the same, so that the controller starts and gives it
            if exc.status != 503 or not self.terminal.waiting:
                raise
            return {"connected": False, "trade_allowed": False, "build": 0, "login": 0, "server": "",
                    "read_only": self.is_read_only(), "waiting": self.terminal.waiting, "error": str(exc),
                    "instance": self.terminal.instance,
                    "deadline": True, "timeout": True, "time": time.time()}

    def is_read_only(self):
        """Whether every trading request is refused: --read-only, or the
        account served read-only."""
        return bool(self.read_only or self.terminal.read_only())

    def session(self, query, body):
        """The account the bridge serves: never its password."""
        terminal = self.terminal
        current = terminal.session or {}
        logged = 0
        if terminal.connected and terminal.lock.acquire(timeout=5):
            try:
                account = mt5.account_info()
                logged = account.login if account else 0
            finally:
                terminal.lock.release()
        return {"login": current.get("login", 0), "server": current.get("server", ""),
                "allow_real": bool(current.get("allow_real")), "read_only": bool(current.get("read_only")),
                "legacy": bool(current.get("legacy")), "connected": terminal.connected, "logged_login": logged,
                "waiting": terminal.waiting, "instance": terminal.instance}

    def set_session(self, query, body):
        """Logs the terminal in the account of the body, from the controller:
        {login, server, password, allow_real, read_only}, the password the one
        the terminal kept when empty; the switches of the account served
        changed alone when it is the same. Its answer is GET /session."""
        session = session_of(body)
        password = body.get("password") or ""
        if not isinstance(password, str):
            raise BridgeError(400, "password must be a string")
        terminal = self.terminal
        if not terminal.lock.acquire(timeout=max(self.args_timeout(), 1)):
            raise BridgeError(504, "the terminal is busy: try again")
        try:
            current = terminal.session or {}
            same = (terminal.connected and current.get("login") == session["login"]
                    and current.get("server") == session["server"])
            if same:
                account = mt5.account_info()
                problem = terminal.account_problem(account, session) if account else "account_info failed"
                if problem:
                    raise BridgeError(409, problem)
                terminal.session = session
                if password:
                    terminal.password = password
                save_session(getattr(terminal.args, "session", ""), session)
            else:
                terminal.login(session, password)
        finally:
            terminal.lock.release()
        return self.session(query, None)

    def args_timeout(self):
        return getattr(self.terminal.args, "timeout", 120)

    def account(self, query, body):
        def fn():
            account = mt5.account_info()
            if account is None:
                raise terminal_error("account_info")
            return account
        return record(self.run(fn))

    def symbol(self, query, body):
        symbol = query_symbol(query)

        def fn():
            if not self.terminal.select(symbol):
                raise BridgeError(404, "no symbol %s" % symbol)
            info = mt5.symbol_info(symbol)
            if info is None and self.terminal.reselect(symbol):
                info = mt5.symbol_info(symbol)
            if info is None:
                raise terminal_error("symbol_info %s" % symbol)
            return info
        return record(self.run(fn))

    def tick(self, query, body):
        symbol = query_symbol(query)

        def fn():
            self.terminal.select(symbol)
            tick = mt5.symbol_info_tick(symbol)
            if tick is None and self.terminal.reselect(symbol):
                tick = mt5.symbol_info_tick(symbol)
            if tick is None:
                raise terminal_error("symbol_info_tick %s" % symbol)
            return tick
        return record(self.run(fn))

    def rates(self, query, body):
        symbol = query_symbol(query)
        name = (query.get("timeframe") or ["M1"])[0]
        timeframe = TIMEFRAMES.get(name)
        if timeframe is None:
            raise BridgeError(400, "invalid timeframe %s, one of %s" % (name, ", ".join(TIMEFRAMES)))
        if "from" in query:
            start, end = query_time(query, "from"), query_time(query, "to")
            what = "copy_rates_range %s" % symbol

            def copy():
                return mt5.copy_rates_range(symbol, timeframe, start, end)
        else:
            count, first = query_int(query, "count"), query_int(query, "start", 0)
            if not 0 < count <= MAX_RATES:
                raise BridgeError(400, "count must be in (0, %d]" % MAX_RATES)
            what = "copy_rates_from_pos %s" % symbol

            def copy():
                # within the "Max. bars in chart" of the terminal, beyond which
                # copy_rates_from_pos refuses the whole request
                bars = getattr(self.terminal.info, "maxbars", 0)
                if bars <= 0:
                    return mt5.copy_rates_from_pos(symbol, timeframe, first, count)
                if first >= bars:
                    return ()
                return mt5.copy_rates_from_pos(symbol, timeframe, first, min(count, bars - first))

        def fn():
            self.terminal.select(symbol)
            rates = copy()
            if rates is None and self.terminal.reselect(symbol):
                rates = copy()
            return rates_listing(what, symbol, timeframe, rates)
        return rates_json(self.run(fn))

    def positions(self, query, body):
        symbol = query_symbol(query, required=False)
        return record(self.run(lambda: listing("positions_get",
                                               mt5.positions_get(symbol=symbol) if symbol else mt5.positions_get())))

    def orders(self, query, body):
        symbol = query_symbol(query, required=False)
        return record(self.run(lambda: listing("orders_get",
                                               mt5.orders_get(symbol=symbol) if symbol else mt5.orders_get())))

    def deals(self, query, body):
        start, end = query_time(query, "from"), query_time(query, "to")
        return record(self.run(lambda: listing("history_deals_get", mt5.history_deals_get(start, end))))

    def history_orders(self, query, body):
        start, end = query_time(query, "from"), query_time(query, "to")
        return record(self.run(lambda: listing("history_orders_get", mt5.history_orders_get(start, end))))

    def trade_request(self, body):
        if self.is_read_only():
            raise BridgeError(403, "the bridge is read-only (--read-only, or the account read-only)")
        if not isinstance(body, dict):
            raise BridgeError(400, "the body must be a trade request object")
        unknown = set(body) - REQUEST_FIELDS
        if unknown:
            raise BridgeError(400, "unknown fields: %s" % ", ".join(sorted(unknown)))
        if "action" not in body:
            raise BridgeError(400, "missing action")
        request = dict(body)
        for field, value in request.items():
            if field in FLOAT_FIELDS or field in INT_FIELDS:
                request[field] = trade_number(field, value)
        return request

    def order(self, query, body):
        """order_send of the trade request, once the account checked and within
        the deadline of the request; an error before order_send is sent False,
        as is never one after it: its outcome is unknown then."""
        sent = False

        def fn():
            nonlocal sent
            self.terminal.check_account()
            if request.get("symbol"):
                mt5.symbol_select(request["symbol"], True)
            check_deadline(self.deadline, "selecting the symbol")
            sent = True
            result = mt5.order_send(request)
            if result is None:
                raise terminal_error("order_send")
            return result
        try:
            request = self.trade_request(body)
            return record(self.run(fn))
        except BridgeError as exc:
            if not sent:
                exc.sent = False
            raise

    def check(self, query, body):
        request = self.trade_request(body)

        def fn():
            result = mt5.order_check(request)
            if result is None:
                raise terminal_error("order_check")
            return result
        return record(self.run(fn))


ROUTES = {
    ("GET", "/health"): Handler.health,
    ("GET", "/account"): Handler.account,
    ("GET", "/symbol"): Handler.symbol,
    ("GET", "/tick"): Handler.tick,
    ("GET", "/rates"): Handler.rates,
    ("GET", "/positions"): Handler.positions,
    ("GET", "/orders"): Handler.orders,
    ("GET", "/deals"): Handler.deals,
    ("GET", "/history_orders"): Handler.history_orders,
    ("POST", "/order"): Handler.order,
    ("POST", "/check"): Handler.check,
    ("GET", "/session"): Handler.session,
    ("POST", "/session"): Handler.set_session,
}

# The reads shared by identical requests waiting for the terminal, see Flights:
# the GETs through the terminal, never a trading request nor the session.
SHARED = frozenset({"/health", "/account", "/symbol", "/tick", "/rates", "/positions", "/orders", "/deals",
                    "/history_orders"})


def loopback(host):
    """Reports whether host is a loopback address, eg: 127.0.0.1, ::1 or localhost."""
    if host == "localhost":
        return True
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--login", type=int, default=0,
                        help="account the terminal must be logged in until the controller gives one (legacy)")
    parser.add_argument("--server", default="", help="trade server, eg: Exness-MT5Trial11 (legacy, with --login)")
    parser.add_argument("--session", default="",
                        help="file keeping the account the controller gave (POST /session), without its password")
    parser.add_argument("--path", default="", help="terminal64.exe, when not the one already running")
    parser.add_argument("--host", default="127.0.0.1",
                        help="address to listen on, eg: 0.0.0.0 in a container; off the loopback, a token is required")
    parser.add_argument("--port", type=int, default=18812)
    parser.add_argument("--token", default=os.environ.get("MT5_BRIDGE_TOKEN", ""),
                        help="required value of the X-Bridge-Token header (default: MT5_BRIDGE_TOKEN)")
    parser.add_argument("--portable", action="store_true",
                        help="the terminal in its /portable mode: its data in its folder")
    parser.add_argument("--allow-real", action="store_true", help="accept a real account")
    parser.add_argument("--read-only", action="store_true", help="refuse every trading request")
    parser.add_argument("--timeout", type=int, default=120, help="seconds to wait for the terminal")
    parser.add_argument("--startup", type=int, default=15, help="seconds the terminal the bridge opens has to load")
    parser.add_argument("--verbose", action="store_true")
    args = parser.parse_args()
    if not args.token and not loopback(args.host):
        parser.error("--host %s: a token is required off the loopback (--token, or MT5_BRIDGE_TOKEN)" % args.host)

    terminal = Terminal(args)
    # fail now rather than on the first request, once the terminal had its time
    # to start: just after a boot, initialize fails ("IPC send failed") while
    # the terminal it opened is still loading
    deadline = time.time() + args.timeout
    while True:
        try:
            terminal.call(lambda: None)
            break
        except BridgeError as exc:
            if terminal.waiting:  # the account, or its password, comes from the controller
                print("mt5bridge: waiting for the %s from the controller: %s" % (terminal.waiting, exc), flush=True)
                break
            if time.time() >= deadline:
                raise
            print("mt5bridge: %s, trying again in 5s" % exc, flush=True)
            time.sleep(5)
    Handler.terminal, Handler.token, Handler.read_only = terminal, args.token, args.read_only
    server = ThreadingHTTPServer((args.host, args.port), Handler)
    server.verbose = args.verbose
    served = terminal.session or {}
    print("mt5bridge: account %s on %s, http://%s:%d%s%s" % (
        served.get("login") or "(none yet)", served.get("server") or "-", args.host, args.port,
        " (read-only)" if args.read_only else "", "" if orjson is not None else " (without orjson)"), flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:  # Ctrl+C, or the controller stopping it
        print("mt5bridge: stopped", flush=True)
    finally:
        mt5.shutdown()


if __name__ == "__main__":
    main()