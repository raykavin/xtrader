"""Tests of mt5bridge.py over HTTP, with a simulated MetaTrader5 package put in
sys.modules before the bridge is imported: the real package is never loaded,
and no MetaTrader5.py lies on the path of the bridge.

    python3 -B -m unittest discover -s tools/mt5bridge -v
"""

import collections
import http.client
import json
import os
import sys
import threading
import time
import types
import unittest
import unittest.mock
import urllib.error
import urllib.request
from http.server import ThreadingHTTPServer

try:
    import numpy
except ImportError:
    numpy = None

LOGIN = 198974329
TOKEN = "secret"

Account = collections.namedtuple("Account", "login trade_mode server currency")
TerminalInfo = collections.namedtuple("TerminalInfo", "connected trade_allowed maxbars")
Result = collections.namedtuple("Result", "retcode order deal volume price comment")
Position = collections.namedtuple("Position", "ticket symbol volume magic comment")


class Scalar(float):
    """A numpy scalar: item() is its Python value."""

    def item(self):
        return float(self)


class Rates(list):
    """A structured numpy array, as copy_rates_* return it: tolist() makes its
    rows tuples of Python values."""

    class dtype:
        names = ("time", "open")

    def tolist(self):
        return [tuple(row[name].item() for name in self.dtype.names) for row in self]


class Package:
    """The state of the simulated MetaTrader5 package, reset by every test."""

    LISTS = ("positions_get", "orders_get", "history_deals_get", "history_orders_get", "copy_rates_from_pos",
             "copy_rates_range")

    def reset(self):
        self.login, self.trade_mode = LOGIN, 0
        self.connected = True  # terminal_info() is not None
        self.error = (1, "Success")
        self.lists = {name: () for name in self.LISTS}  # or a function of the arguments
        self.calls = []  # the calls of the lists, with their arguments
        self.maxbars = 0  # of terminal_info(), 0 for none
        self.slow = {}  # seconds a call takes, by name
        self.initialize_delay = 0.0
        self.initialized = 0
        self.sends = []  # the requests of order_send, with when
        self.send_none = False
        self.logins = []  # the calls of login, with their arguments
        self.login_fails = False

    def module(self):
        mt5 = types.ModuleType("MetaTrader5")
        for i, name in enumerate("M1 M3 M5 M10 M15 M30 H1 H2 H4 D1 W1 MN1".split()):
            setattr(mt5, "TIMEFRAME_" + name, i + 1)
        mt5.ACCOUNT_TRADE_MODE_REAL = 2

        def listed(name):
            def fn(*args, **kwargs):
                self.calls.append((name, args))
                time.sleep(self.slow.get(name, 0))
                value = self.lists[name]
                return value(*args) if callable(value) else value
            return fn

        def initialize(**kwargs):
            time.sleep(self.initialize_delay)
            self.initialized += 1
            self.connected = True
            if not self.login and kwargs.get("login"):  # a terminal logged in no account takes the one given
                self.login = kwargs["login"]
            return True

        def order_send(request):
            self.sends.append((time.time(), request))
            time.sleep(self.slow.get("order_send", 0))
            if self.send_none:
                return None
            return Result(10009, 680000001, 530000001, request.get("volume", 0.0), 4286.5, "Request executed")

        def login(account, password=None, server=None, timeout=None):
            self.logins.append((account, password, server))
            if self.login_fails:
                self.error = (-6, "Terminal: Authorization failed")
                return False
            self.login = account
            return True

        mt5.initialize = initialize
        mt5.login = login
        mt5.shutdown = lambda: None
        mt5.last_error = lambda: self.error
        mt5.version = lambda: (500, 5200, "01 Jan 2026")
        mt5.terminal_info = lambda: TerminalInfo(True, True, self.maxbars) if self.connected else None
        mt5.account_info = lambda: Account(self.login, self.trade_mode, "Exness-MT5Trial11", "USD")
        mt5.symbol_select = lambda symbol, enable: True
        mt5.order_send = order_send
        mt5.order_check = lambda request: Result(0, 0, 0, 0.0, 0.0, "Done")
        for name in self.LISTS:
            setattr(mt5, name, listed(name))
        return mt5


PACKAGE = Package()
PACKAGE.reset()
sys.modules["MetaTrader5"] = PACKAGE.module()
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import mt5bridge  # noqa: E402  (after the simulated package)


class QuietServer(ThreadingHTTPServer):
    daemon_threads = True

    def handle_error(self, request, client_address):
        pass  # a client gone before its answer


class BridgeTest(unittest.TestCase):
    allow_real = False

    def setUp(self):
        PACKAGE.reset()
        args = types.SimpleNamespace(login=LOGIN, server="Exness-MT5Trial11", path="", portable=False,
                                     allow_real=self.allow_real, timeout=1, startup=0)
        terminal = mt5bridge.Terminal(args)
        terminal.call(lambda: None)
        mt5bridge.Handler.terminal, mt5bridge.Handler.token, mt5bridge.Handler.read_only = terminal, TOKEN, False
        self.server = QuietServer(("127.0.0.1", 0), mt5bridge.Handler)
        self.server.verbose = False
        threading.Thread(target=self.server.serve_forever, args=(0.05,), daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)

    def request(self, method, path, body=None, deadline=None, timeout=5, left=None):
        """Sends a request, returning its status and its JSON answer: deadline
        in Unix seconds, left the seconds left until it from now."""
        headers = {"X-Bridge-Token": TOKEN}
        if deadline is not None:
            headers[mt5bridge.DEADLINE_HEADER] = str(int(deadline * 1000))
        if left is not None:
            headers[mt5bridge.TIMEOUT_HEADER] = str(int(left * 1000))
        data = json.dumps(body).encode() if body is not None else None
        request = urllib.request.Request("http://127.0.0.1:%d%s" % (self.server.server_address[1], path),
                                         method=method, data=data, headers=headers)
        try:
            with urllib.request.urlopen(request, timeout=timeout) as response:
                return response.status, json.loads(response.read())
        except urllib.error.HTTPError as exc:
            return exc.code, json.loads(exc.read())

    def order(self, deadline=None, timeout=5, left=None):
        return self.request("POST", "/order", {"action": 1, "symbol": "XAUUSDm", "volume": 0.01, "type": 0,
                                               "magic": 424242, "comment": "nb:test#a1b2c3"}, deadline, timeout,
                            left)

    def hold_terminal(self, seconds):
        """Holds the terminal seconds with a slow GET /positions, from another
        thread, returning it."""
        PACKAGE.slow["positions_get"] = seconds
        thread = threading.Thread(target=lambda: self.request("GET", "/positions", timeout=seconds + 5))
        thread.start()
        time.sleep(0.1)  # the GET holds the terminal
        return thread


class ListsTest(BridgeTest):
    ROUTES = {
        "/positions": "positions_get",
        "/orders": "orders_get",
        "/deals?from=0&to=10": "history_deals_get",
        "/history_orders?from=0&to=10": "history_orders_get",
        "/rates?symbol=XAUUSDm&timeframe=M5&count=2": "copy_rates_from_pos",
        "/rates?symbol=XAUUSDm&timeframe=M5&from=0&to=10": "copy_rates_range",
    }

    def test_failure_is_502_not_empty(self):
        PACKAGE.error = (-10005, "IPC timeout")
        for path, name in self.ROUTES.items():
            PACKAGE.lists[name] = None
            status, answer = self.request("GET", path)
            self.assertEqual(502, status, path)
            self.assertEqual(-10005, answer["retcode"], path)
            self.assertIn("IPC timeout", answer["error"], path)

    def test_empty_is_200(self):
        for path, name in self.ROUTES.items():
            PACKAGE.lists[name] = Rates() if name.startswith("copy_rates") else ()
            self.assertEqual((200, []), self.request("GET", path), path)

    def test_none_without_error_is_empty(self):
        for path, name in self.ROUTES.items():
            PACKAGE.lists[name] = None
            self.assertEqual((200, []), self.request("GET", path), path)

    def test_records(self):
        PACKAGE.lists["positions_get"] = (Position(680000001, "XAUUSDm", 0.03, 424242, "nb:test#a1b2c3"),)
        status, answer = self.request("GET", "/positions")
        self.assertEqual(200, status)
        self.assertEqual([{"ticket": 680000001, "symbol": "XAUUSDm", "volume": 0.03, "magic": 424242,
                           "comment": "nb:test#a1b2c3"}], answer)
        PACKAGE.lists["copy_rates_from_pos"] = Rates([{"time": Scalar(60), "open": Scalar(4286.5)}])
        self.assertEqual((200, [{"time": 60.0, "open": 4286.5}]),
                         self.request("GET", "/rates?symbol=XAUUSDm&timeframe=M5&count=2"))


class RatesTest(BridgeTest):
    """copy_rates_* return None for a range entirely outside the history of the
    terminal, before its start or beyond its "Max. bars in chart" (-1 of
    CopyRates), and an empty array for a range without candles in it."""

    LATEST = Rates([{"time": Scalar(600), "open": Scalar(4286.5)}])

    def setUp(self):
        super().setUp()
        PACKAGE.error = (-1, "Terminal: Call failed")

    def test_range_outside_the_history_is_empty(self):
        PACKAGE.lists["copy_rates_range"] = None
        PACKAGE.lists["copy_rates_from_pos"] = self.LATEST  # the latest candle reads
        self.assertEqual((200, []), self.request("GET", "/rates?symbol=XAUUSDm&timeframe=M5&from=0&to=10"))

    def test_positions_beyond_the_history_are_empty(self):
        PACKAGE.lists["copy_rates_from_pos"] = lambda symbol, timeframe, start, count: (
            self.LATEST if start == 0 else None)
        self.assertEqual((200, []),
                         self.request("GET", "/rates?symbol=XAUUSDm&timeframe=M5&count=100&start=5000"))
        self.assertEqual((200, [{"time": 600.0, "open": 4286.5}]),
                         self.request("GET", "/rates?symbol=XAUUSDm&timeframe=M5&count=100"))

    def test_range_without_candles_is_empty(self):
        PACKAGE.error = (1, "Success")
        PACKAGE.lists["copy_rates_range"] = Rates()  # eg: a weekend
        self.assertEqual((200, []), self.request("GET", "/rates?symbol=XAUUSDm&timeframe=M5&from=0&to=10"))

    def test_failure_of_the_terminal_is_502(self):
        PACKAGE.error = (-10005, "IPC timeout")
        PACKAGE.lists["copy_rates_range"] = None
        PACKAGE.lists["copy_rates_from_pos"] = None  # the latest candle doesn't read either
        for path in ("/rates?symbol=XAUUSDm&timeframe=M5&from=0&to=10",
                     "/rates?symbol=XAUUSDm&timeframe=M5&count=2&start=50"):
            status, answer = self.request("GET", path)
            self.assertEqual(502, status, path)
            self.assertEqual(-10005, answer["retcode"], path)

    def test_positions_within_the_bars_of_the_chart(self):
        PACKAGE.maxbars = 1000
        PACKAGE.lists["copy_rates_from_pos"] = self.LATEST
        self.assertEqual((200, []),
                         self.request("GET", "/rates?symbol=XAUUSDm&timeframe=M5&count=10&start=1000"))
        self.assertEqual([], PACKAGE.calls, "beyond the bars of the chart: not asked")
        self.assertEqual(200, self.request("GET", "/rates?symbol=XAUUSDm&timeframe=M5&count=500&start=800")[0])
        self.assertEqual(("copy_rates_from_pos", ("XAUUSDm", 3, 800, 200)), PACKAGE.calls[-1],
                         "the count cut to the bars of the chart")


class DeadlineTest(BridgeTest):
    def test_health_advertises_deadlines(self):
        status, health = self.request("GET", "/health")
        self.assertEqual(200, status)
        self.assertIs(True, health["deadline"])
        self.assertIs(True, health["timeout"])

    def test_waiting_for_the_terminal_past_the_deadline_is_refused_unsent(self):
        held = self.hold_terminal(1.5)
        started = time.time()
        status, answer = self.order(deadline=started + 0.3)
        self.assertEqual(504, status)
        self.assertIs(False, answer["sent"])
        self.assertLess(time.time() - started, 1.0, "refused at its deadline, not once the terminal is free")
        held.join()
        time.sleep(0.2)
        self.assertEqual([], PACKAGE.sends, "never sent, even once the terminal is free")

    def test_reconnecting_past_the_deadline_is_refused_unsent(self):
        PACKAGE.connected, PACKAGE.initialize_delay = False, 0.5  # the terminal lost
        initialized = PACKAGE.initialized
        status, answer = self.order(deadline=time.time() + 0.2)
        self.assertEqual(504, status)
        self.assertIs(False, answer["sent"])
        self.assertEqual([], PACKAGE.sends)
        self.assertEqual(initialized + 1, PACKAGE.initialized, "connected again for the next requests")
        self.assertEqual(200, self.request("GET", "/account")[0])

    def test_deadline_passed_already(self):
        status, answer = self.order(deadline=time.time() - 1)
        self.assertEqual(504, status)
        self.assertIs(False, answer["sent"])
        self.assertEqual([], PACKAGE.sends)

    def test_within_the_deadline(self):
        held = self.hold_terminal(0.3)
        status, answer = self.order(deadline=time.time() + 3)
        held.join()
        self.assertEqual(200, status)
        self.assertEqual(10009, answer["retcode"])
        self.assertEqual(1, len(PACKAGE.sends))

    def test_without_the_header_as_before(self):
        held = self.hold_terminal(0.3)
        status, answer = self.order()
        held.join()
        self.assertEqual(200, status)
        self.assertEqual(10009, answer["retcode"])
        self.assertEqual("nb:test#a1b2c3", PACKAGE.sends[0][1]["comment"])
        self.assertEqual(200, self.request("GET", "/positions")[0])

    def test_invalid_header(self):
        request = urllib.request.Request("http://127.0.0.1:%d/positions" % self.server.server_address[1],
                                         headers={"X-Bridge-Token": TOKEN, mt5bridge.DEADLINE_HEADER: "soon"})
        with self.assertRaises(urllib.error.HTTPError) as caught:
            urllib.request.urlopen(request, timeout=5)
        self.assertEqual(400, caught.exception.code)

    def test_far_deadline_capped(self):
        handler = mt5bridge.Handler.__new__(mt5bridge.Handler)
        handler.headers = {mt5bridge.DEADLINE_HEADER: str(int((time.time() + 3600) * 1000))}
        self.assertLessEqual(handler.request_deadline(), time.monotonic() + mt5bridge.MAX_DEADLINE)
        handler.headers = {mt5bridge.TIMEOUT_HEADER: "3600000"}
        self.assertLessEqual(handler.request_deadline(), time.monotonic() + mt5bridge.MAX_DEADLINE)
        handler.headers = {}
        self.assertIsNone(handler.request_deadline())

    def test_the_earliest_of_the_two_deadlines(self):
        handler = mt5bridge.Handler.__new__(mt5bridge.Handler)
        handler.headers = {mt5bridge.DEADLINE_HEADER: str(int((time.time() + 10) * 1000)),
                           mt5bridge.TIMEOUT_HEADER: "2000"}
        self.assertAlmostEqual(time.monotonic() + 2, handler.request_deadline(), delta=0.5)
        handler.headers = {mt5bridge.DEADLINE_HEADER: str(int((time.time() + 2) * 1000)),
                           mt5bridge.TIMEOUT_HEADER: "10000"}
        self.assertAlmostEqual(time.monotonic() + 2, handler.request_deadline(), delta=0.5)

    def test_timeout_of_a_clock_behind_the_client(self):
        """The clock of the bridge 10s behind the one of the client, eg: on
        another host: the deadline of the client is 10s further by the clock of
        the bridge, the time left until it is not."""
        held = self.hold_terminal(1.5)
        started = time.time()
        status, answer = self.order(deadline=started + 10.3, left=0.3)
        self.assertEqual(504, status)
        self.assertIs(False, answer["sent"])
        self.assertLess(time.time() - started, 1.0, "refused at the time left, not at the deadline of the client")
        held.join()
        time.sleep(0.2)
        self.assertEqual([], PACKAGE.sends, "never sent, even once the terminal is free")

    def test_timeout_alone(self):
        held = self.hold_terminal(1.5)
        status, answer = self.order(left=0.3)
        held.join()
        self.assertEqual(504, status)
        self.assertIs(False, answer["sent"])
        status, answer = self.order(left=3)
        self.assertEqual(200, status)
        self.assertEqual(1, len(PACKAGE.sends))

    def test_invalid_timeout_header(self):
        request = urllib.request.Request("http://127.0.0.1:%d/positions" % self.server.server_address[1],
                                         headers={"X-Bridge-Token": TOKEN, mt5bridge.TIMEOUT_HEADER: "soon"})
        with self.assertRaises(urllib.error.HTTPError) as caught:
            urllib.request.urlopen(request, timeout=5)
        self.assertEqual(400, caught.exception.code)

    def test_failure_of_order_send_is_unknown(self):
        PACKAGE.send_none, PACKAGE.error = True, (-10005, "IPC timeout")
        status, answer = self.order(deadline=time.time() + 3)
        self.assertEqual(502, status)
        self.assertNotIn("sent", answer, "sent to the terminal: the outcome is unknown")
        self.assertEqual(1, len(PACKAGE.sends))

    def test_refusals_before_order_send_are_unsent(self):
        mt5bridge.Handler.read_only = True
        status, answer = self.order()
        self.assertEqual(403, status)
        self.assertIs(False, answer["sent"])
        mt5bridge.Handler.read_only = False
        status, answer = self.request("POST", "/order", {"symbol": "XAUUSDm"})
        self.assertEqual(400, status)
        self.assertIs(False, answer["sent"])
        self.assertEqual([], PACKAGE.sends)


class AccountTest(BridgeTest):
    def test_another_login_refuses_the_orders_only(self):
        PACKAGE.login = 999  # switched by hand in the terminal
        status, answer = self.order(deadline=time.time() + 3)
        self.assertEqual(409, status)
        self.assertIs(False, answer["sent"])
        self.assertIn("logged in 999, not %d" % LOGIN, answer["error"])
        self.assertEqual([], PACKAGE.sends)
        self.assertEqual(200, self.request("GET", "/positions")[0], "the reads go on")
        self.assertEqual(999, self.request("GET", "/health")[1]["login"])

        PACKAGE.login = LOGIN  # back
        self.assertEqual(200, self.order()[0])
        self.assertEqual(1, len(PACKAGE.sends))

    def test_real_account_without_allow_real(self):
        PACKAGE.trade_mode = 2
        status, answer = self.order()
        self.assertEqual(409, status)
        self.assertIs(False, answer["sent"])
        self.assertIn("allow the real account", answer["error"])
        self.assertEqual([], PACKAGE.sends)


class RealAccountTest(BridgeTest):
    allow_real = True

    def test_real_account_with_allow_real(self):
        PACKAGE.trade_mode = 2
        self.assertEqual(200, self.order()[0])
        self.assertEqual(1, len(PACKAGE.sends))


class TokenTest(BridgeTest):
    def get(self, token):
        """GET /health with the token header as raw bytes, None for none."""
        connection = http.client.HTTPConnection("127.0.0.1", self.server.server_address[1], timeout=5)
        self.addCleanup(connection.close)
        connection.putrequest("GET", "/health")
        if token is not None:
            connection.putheader("X-Bridge-Token", token)
        connection.endheaders()
        return connection.getresponse().status

    def test_the_token_alone_is_accepted(self):
        self.assertEqual(200, self.get(TOKEN.encode()))
        self.assertEqual(401, self.get(None))
        self.assertEqual(401, self.get(b""))
        self.assertEqual(401, self.get(b"secreT"))
        self.assertEqual(401, self.get(TOKEN.encode() + b"x"))
        self.assertEqual(401, self.get(TOKEN.encode()[:-1]))
        self.assertEqual(401, self.get("s\u00e9cret".encode()), "not ASCII: refused, never a 500")

    def test_compared_in_constant_time(self):
        with unittest.mock.patch.object(mt5bridge.hmac, "compare_digest",
                                        wraps=mt5bridge.hmac.compare_digest) as compare:
            self.assertEqual(401, self.get(b"wrong"))
            self.assertEqual(200, self.get(TOKEN.encode()))
        self.assertEqual(2, compare.call_count)

    def test_a_token_not_ascii(self):
        mt5bridge.Handler.token = "s\u00e9nha"
        self.addCleanup(setattr, mt5bridge.Handler, "token", TOKEN)
        self.assertEqual(200, self.get("s\u00e9nha".encode()), "as a Go client sends it: UTF-8")
        self.assertEqual(401, self.get("s\u00e9nha".encode("latin-1")))
        self.assertEqual(401, self.get(b"senha"))


class ConnectionTest(BridgeTest):
    """Connections persist (HTTP/1.1): every answer leaves the connection at the
    start of the next request, the body read even of a request refused."""

    def connection(self):
        connection = http.client.HTTPConnection("127.0.0.1", self.server.server_address[1], timeout=5)
        self.addCleanup(connection.close)
        return connection

    def send(self, connection, method, path, body=None, headers=None):
        connection.request(method, path, body=body, headers={"X-Bridge-Token": TOKEN, **(headers or {})})
        response = connection.getresponse()
        return response.status, json.loads(response.read()), response.getheader("Connection")

    def test_one_connection_for_the_requests(self):
        connection = self.connection()
        self.assertEqual(200, self.send(connection, "GET", "/health")[0])
        sock = connection.sock
        self.assertIsNotNone(sock, "kept open after the answer")
        self.assertEqual(401, self.send(connection, "POST", "/order", b'{"action": 1}' * 100,
                                        {"X-Bridge-Token": "wrong"})[0])
        self.assertEqual(404, self.send(connection, "POST", "/nowhere", b'{"action": 1}')[0])
        self.assertEqual(400, self.send(connection, "POST", "/order", b'{"action": 1}',
                                        {mt5bridge.DEADLINE_HEADER: "soon"})[0])
        self.assertEqual(200, self.send(connection, "GET", "/positions")[0])
        self.assertIs(sock, connection.sock, "the bodies refused read: the connection goes on")
        self.assertEqual([], PACKAGE.sends)

    def test_a_chunked_body_closes_the_connection(self):
        connection = self.connection()
        connection.request("POST", "/order", body=iter([b'{"action": 1}']), encode_chunked=True,
                           headers={"X-Bridge-Token": TOKEN, "Transfer-Encoding": "chunked"})
        response = connection.getresponse()
        self.assertEqual(411, response.status)
        self.assertEqual("close", response.getheader("Connection"))
        self.assertEqual([], PACKAGE.sends)

    def test_an_invalid_length_closes_the_connection(self):
        for length in ("abc", "-1", str(mt5bridge.MAX_BODY + 1)):
            connection = self.connection()
            connection.putrequest("POST", "/order")
            connection.putheader("X-Bridge-Token", TOKEN)
            connection.putheader("Content-Length", length)
            connection.endheaders()
            response = connection.getresponse()
            self.assertEqual(400, response.status, length)
            self.assertEqual("close", response.getheader("Connection"), length)
        self.assertEqual([], PACKAGE.sends)


class ErrorsTest(BridgeTest):
    def test_times_out_of_range_are_400_keeping_the_terminal(self):
        initialized = PACKAGE.initialized
        for path in ("/deals?from=0&to=99999999999999", "/history_orders?from=-99999999999999&to=10",
                     "/rates?symbol=XAUUSDm&timeframe=M5&from=0&to=99999999999999"):
            status, answer = self.request("GET", path)
            self.assertEqual(400, status, path)
            self.assertIn("invalid", answer["error"], path)
        self.assertEqual([], PACKAGE.calls, "never asked to the terminal")
        self.assertEqual(initialized, PACKAGE.initialized, "not connected again")

    def test_an_error_of_the_bridge_keeps_the_terminal(self):
        PACKAGE.lists["positions_get"] = lambda: 1 / 0
        initialized = PACKAGE.initialized
        status, answer = self.request("GET", "/positions")
        self.assertEqual(500, status)
        self.assertIn("ZeroDivisionError", answer["error"])
        PACKAGE.lists["positions_get"] = ()
        self.assertEqual((200, []), self.request("GET", "/positions"))
        self.assertEqual(initialized, PACKAGE.initialized, "the terminal still answers: not connected again")

    def test_the_terminal_gone_during_a_call_is_503(self):
        def gone():
            PACKAGE.connected = False
            raise RuntimeError("IPC broken")
        PACKAGE.lists["positions_get"] = gone
        initialized = PACKAGE.initialized
        self.assertEqual(503, self.request("GET", "/positions")[0])
        PACKAGE.lists["positions_get"] = ()
        self.assertEqual((200, []), self.request("GET", "/positions"))
        self.assertEqual(initialized + 1, PACKAGE.initialized, "connected again by the next request")

    def test_numbers_not_finite_are_refused_unsent(self):
        for fields in ({"magic": float("inf")}, {"sl": float("nan")}, {"volume": 10 ** 400}, {"type": 1.5}):
            status, answer = self.request("POST", "/order", {"action": 1, "symbol": "XAUUSDm", **fields})
            self.assertEqual(400, status, fields)
            self.assertIs(False, answer["sent"], fields)
        self.assertEqual([], PACKAGE.sends)

    def test_the_integers_of_floats_are_floats(self):
        self.assertEqual(200, self.request("POST", "/order", {"action": 1, "symbol": "XAUUSDm", "volume": 1,
                                                              "sl": 0, "tp": 0, "magic": 7.0})[0])
        request = PACKAGE.sends[0][1]
        self.assertEqual((float, float, float, int), tuple(type(request[k]) for k in ("volume", "sl", "tp", "magic")))

    def test_a_symbol_unread_is_502(self):
        PACKAGE.error = (-1, "Terminal: Call failed")
        with unittest.mock.patch.object(mt5bridge.mt5, "symbol_info", create=True, return_value=None):
            status, answer = self.request("GET", "/symbol?symbol=XAUUSDm")
        self.assertEqual(502, status)
        self.assertEqual(-1, answer["retcode"])


class SelectTest(BridgeTest):
    """A symbol is selected in the Market Watch once per connection, and again
    after a read of it failed, eg: removed by hand."""

    RATES = "/rates?symbol=XAUUSDm&timeframe=M5&count=2"

    def test_selected_once_per_connection(self):
        with unittest.mock.patch.object(mt5bridge.mt5, "symbol_select", return_value=True) as select:
            for _ in range(3):
                self.assertEqual((200, []), self.request("GET", self.RATES))
            self.assertEqual(1, select.call_count)
            PACKAGE.connected = False  # the terminal restarted
            self.assertEqual((200, []), self.request("GET", self.RATES))
            self.assertEqual(2, select.call_count, "selected again on the new connection")

    def test_a_failed_read_selects_again(self):
        PACKAGE.error = (-1, "Terminal: Call failed")
        reads = iter([RatesTest.LATEST, None, RatesTest.LATEST])
        PACKAGE.lists["copy_rates_from_pos"] = lambda symbol, timeframe, start, count: next(reads)
        candle = [{"time": 600.0, "open": 4286.5}]
        with unittest.mock.patch.object(mt5bridge.mt5, "symbol_select", return_value=True) as select:
            self.assertEqual((200, candle), self.request("GET", self.RATES))
            self.assertEqual((200, candle), self.request("GET", self.RATES))
        self.assertEqual(2, select.call_count)


class SharedReadsTest(BridgeTest):
    """Identical reads waiting for the terminal are served by one call, read
    after they all arrived; a read arriving once the call has the terminal gets
    a call of its own; other queries and the trading requests are never shared;
    each request keeps its deadline."""

    def concurrently(self, requests):
        """Sends requests, (method, path, kwargs of request), together, while
        the terminal is held, returning their answers in their order."""
        answers = [None] * len(requests)

        def send(i, method, path, kwargs):
            answers[i] = self.request(method, path, **kwargs)
        threads = [threading.Thread(target=send, args=(i,) + tuple(r)) for i, r in enumerate(requests)]
        for thread in threads:
            thread.start()
            time.sleep(0.02)  # in their order
        for thread in threads:
            thread.join()
        return answers

    def calls(self, name):
        return [args for called, args in PACKAGE.calls if called == name]

    def test_identical_reads_queued_share_one_call(self):
        PACKAGE.lists["orders_get"] = ({"ticket": 7},)
        held = self.hold_terminal(0.5)
        answers = self.concurrently([("GET", "/orders", {})] * 5)
        held.join()
        self.assertEqual([(200, [{"ticket": 7}])] * 5, answers)
        self.assertEqual(1, len(self.calls("orders_get")), "one call for the five, read after they arrived")

    def test_a_read_arriving_during_the_call_gets_its_own(self):
        reads = iter([1, 2])
        PACKAGE.lists["orders_get"] = lambda: ({"ticket": next(reads)},)
        PACKAGE.slow["orders_get"] = 0.4
        first = threading.Thread(target=lambda: self.assertEqual((200, [{"ticket": 1}]),
                                                                 self.request("GET", "/orders")))
        first.start()
        time.sleep(0.15)  # the first one reading
        self.assertEqual((200, [{"ticket": 2}]), self.request("GET", "/orders"), "never an answer read before it")
        first.join()
        self.assertEqual(2, len(self.calls("orders_get")))

    def test_other_queries_are_not_shared(self):
        held = self.hold_terminal(0.4)
        answers = self.concurrently([("GET", "/deals?from=0&to=10", {}), ("GET", "/deals?from=0&to=20", {}),
                                     ("GET", "/deals?to=10&from=0", {})])
        held.join()
        self.assertEqual([(200, [])] * 3, answers)
        self.assertEqual([10, 20], sorted(int(to.timestamp()) for _, to in self.calls("history_deals_get")),
                         "the same query in another order shared")

    def test_trading_requests_are_never_shared(self):
        held = self.hold_terminal(0.4)
        answers = self.concurrently([("POST", "/order", {"body": {"action": 1, "symbol": "XAUUSDm", "volume": 0.01,
                                                                   "type": 0, "magic": 424242, "comment": "a"}})] * 2)
        held.join()
        self.assertEqual([200, 200], [status for status, _ in answers])
        self.assertEqual(2, len(PACKAGE.sends))

    def test_a_failure_is_the_answer_of_all(self):
        PACKAGE.lists["orders_get"] = None
        PACKAGE.error = (-10005, "IPC timeout")
        held = self.hold_terminal(0.4)
        answers = self.concurrently([("GET", "/orders", {})] * 3)
        held.join()
        self.assertEqual([502] * 3, [status for status, _ in answers])
        self.assertEqual(1, len(self.calls("orders_get")))

    def test_each_keeps_its_deadline(self):
        held = self.hold_terminal(0.8)
        started = time.time()
        answers = self.concurrently([("GET", "/orders", {}), ("GET", "/orders", {"left": 0.2})])
        held.join()
        self.assertEqual(200, answers[0][0])
        self.assertEqual(504, answers[1][0], "the one joined gave up at its own deadline")
        self.assertLess(time.time() - started, 2)
        self.assertEqual(1, len(self.calls("orders_get")))

    def test_one_joined_queues_again_when_the_call_gave_up_first(self):
        held = self.hold_terminal(0.6)
        answers = self.concurrently([("GET", "/orders", {"left": 0.2}), ("GET", "/orders", {"left": 3})])
        held.join()
        self.assertEqual(504, answers[0][0], "the first one gave up waiting for the terminal")
        self.assertEqual(200, answers[1][0], "the second one, with time left, queued again")
        self.assertEqual(1, len(self.calls("orders_get")))


class EncodingTest(unittest.TestCase):
    @unittest.skipIf(numpy is None, "without numpy")
    def test_rates_json_of_a_structured_array(self):
        dtype = numpy.dtype([("time", "<i8"), ("open", "<f8"), ("tick_volume", "<u8"), ("spread", "<i4")])
        rates = numpy.array([(60, 4286.5, 12, 3), (120, 4287.25, 7, 2)], dtype=dtype)
        candles = mt5bridge.rates_json(rates)
        self.assertEqual([{"time": 60, "open": 4286.5, "tick_volume": 12, "spread": 3},
                          {"time": 120, "open": 4287.25, "tick_volume": 7, "spread": 2}], candles)
        self.assertEqual([int, float, int, int], [type(v) for v in candles[0].values()], "Python values")
        self.assertEqual([], mt5bridge.rates_json(rates[:0]))

    def test_dumps_with_and_without_orjson(self):
        payload = {"retcode": 10009, "price": 4286.5, "comment": "nb:test#a1b2c3", "deals": []}
        self.assertEqual(payload, json.loads(mt5bridge.dumps(payload)))
        with unittest.mock.patch.object(mt5bridge, "orjson", None):
            self.assertEqual(b'{"retcode":10009,"price":4286.5,"comment":"nb:test#a1b2c3","deals":[]}',
                             mt5bridge.dumps(payload))


if __name__ == "__main__":
    unittest.main()

class SessionTest(unittest.TestCase):
    """The account given by the controller: POST /session logs the terminal in
    it and keeps it, without its password, over the legacy --login; without
    any, the bridge serves /health and waits."""

    def serve(self, args):
        terminal = mt5bridge.Terminal(args)
        try:
            terminal.call(lambda: None)
        except mt5bridge.BridgeError:
            pass  # waiting for the session
        mt5bridge.Handler.terminal, mt5bridge.Handler.token, mt5bridge.Handler.read_only = terminal, TOKEN, False
        server = QuietServer(("127.0.0.1", 0), mt5bridge.Handler)
        server.verbose = False
        threading.Thread(target=server.serve_forever, args=(0.05,), daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        self.port = server.server_address[1]
        return terminal

    def request(self, method, path, body=None):
        data = json.dumps(body).encode() if body is not None else None
        request = urllib.request.Request("http://127.0.0.1:%d%s" % (self.port, path), method=method, data=data,
                                         headers={"X-Bridge-Token": TOKEN})
        try:
            with urllib.request.urlopen(request, timeout=5) as response:
                return response.status, json.loads(response.read())
        except urllib.error.HTTPError as exc:
            return exc.code, json.loads(exc.read())

    def args(self, login=0, server="", **extra):
        values = dict(login=login, server=server, path="", portable=False, allow_real=False, timeout=1,
                      startup=0, session=os.path.join(self.dir, "session.json"))
        values.update(extra)
        return types.SimpleNamespace(**values)

    def setUp(self):
        PACKAGE.reset()
        self.dir = self.enterContext(__import__("tempfile").TemporaryDirectory())

    def test_waits_then_takes_the_session(self):
        self.serve(self.args())
        status, health = self.request("GET", "/health")
        self.assertEqual(200, status, health)
        self.assertEqual("session", health["waiting"])
        self.assertFalse(health["connected"])
        status, answer = self.request("GET", "/account")
        self.assertEqual(503, status)
        self.assertIn("Contas MT5", answer["error"])

        PACKAGE.login = 0
        status, session = self.request("POST", "/session", {"login": 478378599, "server": "Exness-MT5Real11",
                                                            "password": "s3cret!", "allow_real": True})
        self.assertEqual(200, status, session)
        self.assertEqual(478378599, session["login"])
        self.assertEqual(478378599, session["logged_login"])
        self.assertTrue(session["connected"])
        self.assertEqual("", session["waiting"])
        self.assertNotIn("s3cret!", json.dumps(session), "never the password")
        with open(os.path.join(self.dir, "session.json"), encoding="utf-8") as f:
            kept = f.read()
        self.assertNotIn("s3cret!", kept, "never on the disk")
        self.assertEqual({"login": 478378599, "server": "Exness-MT5Real11", "allow_real": True,
                          "read_only": False}, json.loads(kept))
        self.assertEqual(0o600, os.stat(os.path.join(self.dir, "session.json")).st_mode & 0o777)
        status, health = self.request("GET", "/health")
        self.assertEqual(200, status)
        self.assertTrue(health["connected"])
        self.assertNotIn("waiting", health)

    def test_the_kept_session_over_the_legacy_login(self):
        mt5bridge.save_session(os.path.join(self.dir, "session.json"),
                               {"login": 478378599, "server": "Exness-MT5Real11", "allow_real": True,
                                "read_only": False})
        PACKAGE.login = 478378599
        terminal = self.serve(self.args(login=LOGIN, server="Exness-MT5Trial11"))
        self.assertEqual(478378599, terminal.session["login"], "the controller's, not the legacy one")
        self.assertFalse(terminal.session.get("legacy"))
        status, session = self.request("GET", "/session")
        self.assertEqual(200, status)
        self.assertEqual((478378599, True), (session["login"], session["connected"]))

    def test_the_instance_kept_apart_from_other_terminals(self):
        terminal = self.serve(self.args())
        status, health = self.request("GET", "/health")
        self.assertEqual(200, status)
        status, session = self.request("GET", "/session")
        self.assertEqual(200, status)
        self.assertTrue(terminal.instance)
        self.assertEqual(terminal.instance, health["instance"])
        self.assertEqual(terminal.instance, session["instance"])
        again = mt5bridge.Terminal(self.args())
        self.assertEqual(terminal.instance, again.instance, "the same across its restarts")
        other = self.enterContext(__import__("tempfile").TemporaryDirectory())
        apart = mt5bridge.Terminal(self.args(session=os.path.join(other, "session.json")))
        self.assertNotEqual(terminal.instance, apart.instance, "another terminal, on the same host too")

    def test_legacy_login_until_a_session(self):
        terminal = self.serve(self.args(login=LOGIN, server="Exness-MT5Trial11"))
        self.assertTrue(terminal.session["legacy"])
        status, session = self.request("GET", "/session")
        self.assertEqual((LOGIN, True, True), (session["login"], session["legacy"], session["connected"]))

    def test_refusals_leave_the_account_served(self):
        self.serve(self.args(login=LOGIN, server="Exness-MT5Trial11"))
        for body, code in (({"login": 0, "server": "x"}, 400), ({"login": 5, "server": " "}, 400),
                           ({"login": 5, "server": "x", "allow_real": "yes"}, 400),
                           ({"login": 5, "server": "x", "password": 7}, 400)):
            status, _ = self.request("POST", "/session", body)
            self.assertEqual(code, status, body)

        # a real account without allow_real: refused, the session not kept
        PACKAGE.trade_mode = 2
        status, answer = self.request("POST", "/session", {"login": 478378599, "server": "Exness-MT5Real11"})
        self.assertEqual(409, status, answer)
        self.assertIn("real", answer["error"])
        self.assertFalse(os.path.exists(os.path.join(self.dir, "session.json")))

        # the login refused by the terminal
        PACKAGE.trade_mode, PACKAGE.login_fails = 0, True
        status, answer = self.request("POST", "/session", {"login": 87654321, "server": "Exness-MT5Trial11",
                                                           "password": "wrong"})
        self.assertEqual(502, status)
        self.assertIn("Authorization failed", answer["error"])
        self.assertNotIn("wrong", answer["error"])

    def test_the_same_account_changes_its_switches_only(self):
        self.serve(self.args(login=LOGIN, server="Exness-MT5Trial11"))
        status, session = self.request("POST", "/session", {"login": LOGIN, "server": "Exness-MT5Trial11",
                                                            "read_only": True})
        self.assertEqual(200, status, session)
        self.assertTrue(session["read_only"])
        self.assertEqual([], PACKAGE.logins, "no login again")
        status, answer = self.request("POST", "/order", {"action": 1, "symbol": "XAUUSDm", "volume": 0.01})
        self.assertEqual(403, status, "read-only")
        self.assertFalse(answer.get("sent", True))
        status, _ = self.request("POST", "/session", {"login": LOGIN, "server": "Exness-MT5Trial11"})
        self.assertEqual(200, status)

    def test_switches_the_account_of_the_open_terminal(self):
        self.serve(self.args(login=LOGIN, server="Exness-MT5Trial11"))
        status, session = self.request("POST", "/session", {"login": 87654321, "server": "Exness-MT5Trial11",
                                                            "password": "pw"})
        self.assertEqual(200, status, session)
        self.assertEqual([(87654321, "pw", "Exness-MT5Trial11")], PACKAGE.logins)
        self.assertEqual(87654321, session["logged_login"])
