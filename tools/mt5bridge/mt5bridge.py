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
as the terminal reports them.

Endpoints (JSON):
    GET  /health                       terminal and account state
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
Errors: {"error": "...", "retcode": n} with an HTTP status. A list the package
fails to read (None) is an error (502), never an empty list. Candles are the
exception for a range the terminal holds none of, eg: before the start of its
history or beyond its "Max. bars in chart", which copy_rates_* refuse (None)
as the documentation of CopyRates says: when the latest candle of the symbol
reads, the range is outside the history, answered empty as a range without
candles, and only otherwise an error. The candles by position are read within
the "Max. bars in chart" of the terminal (terminal_info().maxbars).

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
an error after order_send (502, 503) leaves the outcome unknown.
"""

import argparse
import datetime as dt
import hmac
import ipaddress
import json
import os
import subprocess
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

import MetaTrader5 as mt5

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


class Terminal:
    """The terminal, used by one thread at a time: the MetaTrader5 package keeps a
    single connection per process."""

    def __init__(self, args):
        self.args = args
        self.lock = threading.Lock()
        self.connected = False
        self.process = None  # the terminal the bridge opened, if any

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
        self.launch()
        kwargs = {"login": self.args.login, "server": self.args.server, "timeout": self.args.timeout * 1000}
        if self.args.path:
            kwargs["path"] = self.args.path
        if self.args.portable:
            kwargs["portable"] = True
        password = os.environ.get("MT5_PASSWORD", "")
        if password:
            kwargs["password"] = password
        if not mt5.initialize(**kwargs):
            raise BridgeError(503, "initialize: %s" % (mt5.last_error(),))
        account = mt5.account_info()
        if account is None:
            mt5.shutdown()
            raise BridgeError(503, "no account: %s" % (mt5.last_error(),))
        problem = self.account_problem(account)
        if problem:
            mt5.shutdown()
            raise BridgeError(503, problem)
        self.connected = True

    def account_problem(self, account):
        """Why the account the terminal is logged in is not the one to trade,
        None when it is."""
        if account.login != self.args.login:
            return "the terminal is logged in %d, not %d" % (account.login, self.args.login)
        if account.trade_mode == mt5.ACCOUNT_TRADE_MODE_REAL and not self.args.allow_real:
            return "the account %d is real: start the bridge with --allow-real" % account.login
        return None

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

    def call(self, fn, deadline=None):
        """Runs fn with the terminal, connecting again when it was lost, eg: the
        terminal closed, which initialize starts again with --path. With a
        deadline (a time.monotonic()), it waits for the terminal until then at
        most, a request behind a slow one or a reconnection, and refuses to
        start fn after it (504)."""
        if deadline is None:
            self.lock.acquire()
        elif not self.lock.acquire(timeout=max(deadline - time.monotonic(), 0)):
            raise BridgeError(504, "the deadline of the request passed waiting for the terminal: not sent to it")
        try:
            check_deadline(deadline, "waiting for the terminal")
            if self.connected and mt5.terminal_info() is None:
                self.connected = False
                mt5.shutdown()
            if not self.connected:
                self.connect()
                check_deadline(deadline, "connecting to the terminal")
            try:
                return fn()
            except BridgeError:
                raise
            except Exception as exc:  # the terminal went away: reconnect on the next call
                self.connected = False
                mt5.shutdown()
                raise BridgeError(503, "terminal: %s" % exc)
        finally:
            self.lock.release()


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


def listing(what, value, convert=record):
    """The JSON list of the records the package returned, by convert: None is
    an error of the terminal (502), unless last_error() reports none, as for
    nothing to list; an empty tuple or array is an empty list. Under the lock,
    right after the call."""
    if value is None:
        code, message = mt5.last_error()
        if code != RES_S_OK:
            raise BridgeError(502, "%s: %s" % (what, message), code)
        return []
    return convert(value)


def rates_json(rates):
    names = rates.dtype.names
    return [{name: row[name].item() for name in names} for row in rates]


def rates_listing(what, symbol, timeframe, rates):
    """The JSON candles copy_rates_* returned, see listing: None with an error
    is a range outside the history of the terminal, answered empty, when the
    latest candle of the symbol reads, eg: before the start of its history or
    beyond its "Max. bars in chart" (-1 of CopyRates); else the error (502).
    Under the lock, right after the call."""
    if rates is not None:
        return rates_json(rates)
    code, message = mt5.last_error()
    if code == RES_S_OK:
        return []
    latest = mt5.copy_rates_from_pos(symbol, timeframe, 0, 1)
    if latest is not None and len(latest) > 0:
        return []
    raise BridgeError(502, "%s: %s" % (what, message), code)


def utc(seconds):
    return dt.datetime.fromtimestamp(int(seconds), tz=dt.timezone.utc)


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


def query_symbol(query, required=True):
    symbol = (query.get("symbol") or [""])[0]
    if required and not symbol:
        raise BridgeError(400, "missing symbol")
    return symbol


def terminal_error(what):
    code, message = mt5.last_error()
    return BridgeError(502, "%s: %s" % (what, message), code)


class Handler(BaseHTTPRequestHandler):
    terminal = None
    token = ""
    read_only = False
    deadline = None  # of the request handled, see request_deadline

    def log_message(self, fmt, *args):
        if self.server.verbose:
            super().log_message(fmt, *args)

    def reply(self, status, payload):
        data = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

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
        return self.terminal.call(fn, self.deadline)

    def handle_request(self, method):
        if not self.authorized():
            self.reply(401, {"error": "invalid X-Bridge-Token"})
            return
        url = urlparse(self.path)
        query = parse_qs(url.query)
        try:
            self.deadline = self.request_deadline()
            route = ROUTES.get((method, url.path))
            if route is None:
                raise BridgeError(404, "no route %s %s" % (method, url.path))
            body = None
            if method == "POST":
                length = int(self.headers.get("Content-Length") or 0)
                body = json.loads(self.rfile.read(length) or b"{}")
            self.reply(200, route(self, query, body))
        except BridgeError as exc:
            payload = {"error": str(exc)}
            if exc.retcode is not None:
                payload["retcode"] = exc.retcode
            if exc.sent is False:
                payload["sent"] = False
            self.reply(exc.status, payload)
        except json.JSONDecodeError as exc:
            self.reply(400, {"error": "invalid JSON: %s" % exc})

    def do_GET(self):
        self.handle_request("GET")

    def do_POST(self):
        self.handle_request("POST")

    # routes

    def health(self, query, body):
        def fn():
            info = mt5.terminal_info()
            account = mt5.account_info()
            return {
                "connected": bool(info and info.connected),
                "trade_allowed": bool(info and info.trade_allowed),
                "build": mt5.version()[1] if mt5.version() else 0,
                "login": account.login if account else 0,
                "server": account.server if account else "",
                "read_only": self.read_only,
                "deadline": True,  # X-Bridge-Deadline is honored
                "timeout": True,  # and X-Bridge-Timeout
                "time": time.time(),
            }
        return self.run(fn)

    def account(self, query, body):
        def fn():
            account = mt5.account_info()
            if account is None:
                raise terminal_error("account_info")
            return record(account)
        return self.run(fn)

    def symbol(self, query, body):
        symbol = query_symbol(query)

        def fn():
            if not mt5.symbol_select(symbol, True):
                raise BridgeError(404, "no symbol %s" % symbol)
            return record(mt5.symbol_info(symbol))
        return self.run(fn)

    def tick(self, query, body):
        symbol = query_symbol(query)

        def fn():
            mt5.symbol_select(symbol, True)
            tick = mt5.symbol_info_tick(symbol)
            if tick is None:
                raise terminal_error("symbol_info_tick %s" % symbol)
            return record(tick)
        return self.run(fn)

    def rates(self, query, body):
        symbol = query_symbol(query)
        name = (query.get("timeframe") or ["M1"])[0]
        timeframe = TIMEFRAMES.get(name)
        if timeframe is None:
            raise BridgeError(400, "invalid timeframe %s, one of %s" % (name, ", ".join(TIMEFRAMES)))
        if "from" in query:
            start, end = query_int(query, "from"), query_int(query, "to")

            def fn():
                mt5.symbol_select(symbol, True)
                return rates_listing("copy_rates_range %s" % symbol, symbol, timeframe,
                                     mt5.copy_rates_range(symbol, timeframe, utc(start), utc(end)))
        else:
            count, first = query_int(query, "count"), query_int(query, "start", 0)
            if not 0 < count <= MAX_RATES:
                raise BridgeError(400, "count must be in (0, %d]" % MAX_RATES)

            def fn():
                mt5.symbol_select(symbol, True)
                # within the "Max. bars in chart" of the terminal, beyond which
                # copy_rates_from_pos refuses the whole request
                info = mt5.terminal_info()
                bars = getattr(info, "maxbars", 0) if info is not None else 0
                wanted = count
                if bars > 0:
                    if first >= bars:
                        return []
                    wanted = min(count, bars - first)
                return rates_listing("copy_rates_from_pos %s" % symbol, symbol, timeframe,
                                     mt5.copy_rates_from_pos(symbol, timeframe, first, wanted))
        return self.run(fn)

    def positions(self, query, body):
        symbol = query_symbol(query, required=False)
        return self.run(lambda: listing("positions_get",
                                        mt5.positions_get(symbol=symbol) if symbol else mt5.positions_get()))

    def orders(self, query, body):
        symbol = query_symbol(query, required=False)
        return self.run(lambda: listing("orders_get", mt5.orders_get(symbol=symbol) if symbol else mt5.orders_get()))

    def deals(self, query, body):
        start, end = query_int(query, "from"), query_int(query, "to")
        return self.run(lambda: listing("history_deals_get", mt5.history_deals_get(utc(start), utc(end))))

    def history_orders(self, query, body):
        start, end = query_int(query, "from"), query_int(query, "to")
        return self.run(lambda: listing("history_orders_get", mt5.history_orders_get(utc(start), utc(end))))

    def trade_request(self, body):
        if self.read_only:
            raise BridgeError(403, "the bridge is read-only (--read-only)")
        if not isinstance(body, dict):
            raise BridgeError(400, "the body must be a trade request object")
        unknown = set(body) - REQUEST_FIELDS
        if unknown:
            raise BridgeError(400, "unknown fields: %s" % ", ".join(sorted(unknown)))
        if "action" not in body:
            raise BridgeError(400, "missing action")
        request = dict(body)
        for field, value in request.items():
            number = isinstance(value, (int, float)) and not isinstance(value, bool)
            if field in FLOAT_FIELDS:
                if not number:
                    raise BridgeError(400, "%s must be a number" % field)
                request[field] = float(value)
            elif field in INT_FIELDS:
                if not number or value != int(value):
                    raise BridgeError(400, "%s must be an integer" % field)
                request[field] = int(value)
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
            return record(result)
        try:
            request = self.trade_request(body)
            return self.run(fn)
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
            return record(result)
        return self.run(fn)


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
}


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
    parser.add_argument("--login", type=int, required=True, help="account the terminal must be logged in")
    parser.add_argument("--server", required=True, help="trade server, eg: Exness-MT5Trial11")
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
            if time.time() >= deadline:
                raise
            print("mt5bridge: %s, trying again in 5s" % exc, flush=True)
            time.sleep(5)
    Handler.terminal, Handler.token, Handler.read_only = terminal, args.token, args.read_only
    server = ThreadingHTTPServer((args.host, args.port), Handler)
    server.verbose = args.verbose
    print("mt5bridge: account %d on %s, http://%s:%d%s" % (
        args.login, args.server, args.host, args.port, " (read-only)" if args.read_only else ""), flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:  # Ctrl+C, or the controller stopping it
        print("mt5bridge: stopped", flush=True)
    finally:
        mt5.shutdown()


if __name__ == "__main__":
    main()
