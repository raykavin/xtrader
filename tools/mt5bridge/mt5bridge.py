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
Errors: {"error": "...", "retcode": n} with an HTTP status.
"""

import argparse
import datetime as dt
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


class BridgeError(Exception):
    def __init__(self, status, message, retcode=None):
        super().__init__(message)
        self.status = status
        self.retcode = retcode


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
        if account.login != self.args.login:
            mt5.shutdown()
            raise BridgeError(503, "the terminal is logged in %d, not %d" % (account.login, self.args.login))
        if account.trade_mode == mt5.ACCOUNT_TRADE_MODE_REAL and not self.args.allow_real:
            mt5.shutdown()
            raise BridgeError(503, "the account %d is real: start the bridge with --allow-real" % account.login)
        self.connected = True

    def call(self, fn):
        """Runs fn with the terminal, connecting again when it was lost, eg: the
        terminal closed, which initialize starts again with --path."""
        with self.lock:
            if self.connected and mt5.terminal_info() is None:
                self.connected = False
                mt5.shutdown()
            if not self.connected:
                self.connect()
            try:
                return fn()
            except BridgeError:
                raise
            except Exception as exc:  # the terminal went away: reconnect on the next call
                self.connected = False
                mt5.shutdown()
                raise BridgeError(503, "terminal: %s" % exc)


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


def rates_json(rates):
    if rates is None:
        return []
    names = rates.dtype.names
    return [{name: row[name].item() for name in names} for row in rates]


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
        return not self.token or self.headers.get("X-Bridge-Token") == self.token

    def handle_request(self, method):
        if not self.authorized():
            self.reply(401, {"error": "invalid X-Bridge-Token"})
            return
        url = urlparse(self.path)
        query = parse_qs(url.query)
        try:
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
                "time": time.time(),
            }
        return self.terminal.call(fn)

    def account(self, query, body):
        def fn():
            account = mt5.account_info()
            if account is None:
                raise terminal_error("account_info")
            return record(account)
        return self.terminal.call(fn)

    def symbol(self, query, body):
        symbol = query_symbol(query)

        def fn():
            if not mt5.symbol_select(symbol, True):
                raise BridgeError(404, "no symbol %s" % symbol)
            return record(mt5.symbol_info(symbol))
        return self.terminal.call(fn)

    def tick(self, query, body):
        symbol = query_symbol(query)

        def fn():
            mt5.symbol_select(symbol, True)
            tick = mt5.symbol_info_tick(symbol)
            if tick is None:
                raise terminal_error("symbol_info_tick %s" % symbol)
            return record(tick)
        return self.terminal.call(fn)

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
                return mt5.copy_rates_range(symbol, timeframe, utc(start), utc(end))
        else:
            count, first = query_int(query, "count"), query_int(query, "start", 0)
            if not 0 < count <= MAX_RATES:
                raise BridgeError(400, "count must be in (0, %d]" % MAX_RATES)

            def fn():
                mt5.symbol_select(symbol, True)
                return mt5.copy_rates_from_pos(symbol, timeframe, first, count)
        return rates_json(self.terminal.call(fn))

    def positions(self, query, body):
        symbol = query_symbol(query, required=False)
        return self.terminal.call(lambda: record(mt5.positions_get(symbol=symbol) if symbol else mt5.positions_get()) or [])

    def orders(self, query, body):
        symbol = query_symbol(query, required=False)
        return self.terminal.call(lambda: record(mt5.orders_get(symbol=symbol) if symbol else mt5.orders_get()) or [])

    def deals(self, query, body):
        start, end = query_int(query, "from"), query_int(query, "to")
        return self.terminal.call(lambda: record(mt5.history_deals_get(utc(start), utc(end))) or [])

    def history_orders(self, query, body):
        start, end = query_int(query, "from"), query_int(query, "to")
        return self.terminal.call(lambda: record(mt5.history_orders_get(utc(start), utc(end))) or [])

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
        request = self.trade_request(body)

        def fn():
            if request.get("symbol"):
                mt5.symbol_select(request["symbol"], True)
            result = mt5.order_send(request)
            if result is None:
                raise terminal_error("order_send")
            return record(result)
        return self.terminal.call(fn)

    def check(self, query, body):
        request = self.trade_request(body)

        def fn():
            result = mt5.order_check(request)
            if result is None:
                raise terminal_error("order_check")
            return record(result)
        return self.terminal.call(fn)


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
