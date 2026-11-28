"""Opt-in hosted smoke against the installed Go preview, never an API stub."""
from datetime import timedelta
from importlib.metadata import version
import os
import re
import subprocess
from urllib.parse import parse_qs, urlsplit
import uuid


def launch_category(error):
    message = str(error).lower()
    if "error while loading shared libraries" in message or "missing dependencies" in message:
        return "missing_browser_libraries"
    if ("distribution 'chrome' is not found" in message or "executable doesn't exist" in message):
        return "missing_chrome_stable"
    if any(hint in message for hint in (
        "no usable sandbox", "failed to move to new namespace", "sandbox is not supported",
        "running as root without --no-sandbox", "userns_create",
    )):
        return "sandbox_launch_blocked"
    return "unclassified_launch_failure"


def host_evidence():
    result = {"backend": "host-chrome-stable", "chromium_sandbox": True}
    for key, env, pattern in (
        ("runner_os", "ImageOS", r"ubuntu[0-9]{4}"),
        ("runner_image_version", "ImageVersion", r"[0-9]+(?:\.[0-9]+){1,3}"),
    ):
        value = os.environ.get(env, "")
        if re.fullmatch(pattern, value):
            result[key] = value
    try:
        package = subprocess.run(
            ["dpkg-query", "-W", "-f", "${Version}", "google-chrome-stable"],
            capture_output=True, text=True, check=True, timeout=5,
        ).stdout
    except (OSError, subprocess.SubprocessError):
        return result
    if re.fullmatch(r"[0-9]+(?:\.[0-9]+){3}(?:-[0-9]+)?", package):
        result["chrome_package_version"] = package
    return result


def assert_usage_today(page, origin, start, expect, phase):
    zone = "Asia/Taipei"
    end = start + timedelta(minutes=90)

    def iso(value):
        return value.isoformat(timespec="milliseconds").replace("+00:00", "Z")

    summary_query = {"start": [iso(start)], "end": [iso(end)]}
    hourly_query = {**summary_query, "bucket": ["hour"], "timeZone": [zone]}

    def usage_query(response):
        parts = urlsplit(response.url)
        if response.request.method == "GET" and response.url.startswith(origin + "/") and parts.path == "/api/usage":
            return parse_qs(parts.query)
        return None

    phase("usage-today-open-models")
    page.get_by_role("link", name="Models", exact=True).click()
    phase("usage-today-open-usage")
    page.get_by_role("button", name="Usage & costs", exact=True).click()
    overview = page.get_by_role("heading", name="Usage overview", exact=True)
    expect(overview).to_be_visible()
    phase("usage-today-open-range")
    page.clock.set_fixed_time(end)
    # The dashboard also has a global language combobox.
    overview.locator("xpath=ancestor::section[1]").get_by_role("combobox").click()
    today_queries = []

    def record_today(response):
        query = usage_query(response)
        if query and query.get("start") == summary_query["start"] and query.get("end") == summary_query["end"]:
            today_queries.append(query)

    page.on("response", record_today)
    with page.expect_response(lambda r: usage_query(r) == summary_query) as summary, \
         page.expect_response(lambda r: usage_query(r) == hourly_query) as hourly:
        phase("usage-today-select-range")
        page.get_by_role("option", name="Today", exact=True).click()
        phase("usage-today-wait-responses")

    def metrics(response):
        assert response.status == 200
        assert response.request.all_headers().get("x-humanengine-session") == "1"
        assert response.header_value("X-Usage-Incomplete") != "true"
        rows = response.json()
        assert all((row["modelId"], row["role"]) == ("browser-model", "planner") for row in rows)
        return sorted((row["day"], row["calls"], row["reportedInputTokens"], row["reportedOutputTokens"])
                      for row in rows)

    phase("usage-today-summary-response")
    assert metrics(summary.value) == [(start.date().isoformat(), 2, 330, 40)]
    phase("usage-today-hourly-response")
    assert metrics(hourly.value) == [(iso(start), 1, 100, 11),
                                     (iso(start + timedelta(hours=1)), 1, 230, 29)]
    phase("usage-today-render-summary")
    calls = page.locator(".usage-stat").filter(has=page.get_by_text("Calls", exact=True))
    expect(calls.locator(".usage-stat-value")).to_have_text("2")
    tokens = page.locator(".usage-stat").filter(has=page.get_by_text("Reported token subtotal", exact=True))
    expect(tokens.locator(".usage-stat-value")).to_have_text("370")
    expect(tokens).to_contain_text("Input tokens 330")
    expect(tokens).to_contain_text("Output tokens 40")
    phase("usage-today-render-chart")
    chart = page.get_by_role("heading", name="Token usage over time", exact=True).locator("xpath=ancestor::section[1]")
    for label in ("00:00", "01:00"):
        expect(chart.get_by_text(label, exact=True)).to_be_visible()
    phase("usage-today-render-details")
    detail = page.locator("details").filter(has=page.locator("summary", has_text="Time bucket details"))
    detail.locator("summary").click()
    rows = detail.locator("tbody tr")
    expect(rows).to_have_count(2)
    for index, (label, input_tokens, output_tokens) in enumerate((("00:00", "100", "11"), ("01:00", "230", "29"))):
        cells = rows.nth(index).get_by_role("cell")
        expect(cells.nth(0)).to_contain_text(label)
        expect(cells.nth(1)).to_have_text(input_tokens)
        expect(cells.nth(2)).to_have_text(output_tokens)
    page.remove_listener("response", record_today)
    phase("usage-today-response-count")
    assert len(today_queries) == 2 and summary_query in today_queries and hourly_query in today_queries


def browser_request_guard(origin, fixture_avatars, blocked):
    def intercept(route):
        request = route.request
        if request.url.startswith(origin + "/"):
            return route.continue_()
        if request.method == "GET" and request.resource_type == "image" and request.url in fixture_avatars:
            blocked["fixture_avatars"] += 1
        else:
            blocked["unexpected_external"] += 1
        # The synthetic avatar exception is only to the assertion, not the network block.
        route.abort()

    return intercept


def run(port, token, evidence, usage_start, chat_history=None, chat_image=False, operator_plugins=False):
    # Kept out of normal package runs and out of both runtime images.
    from playwright.sync_api import expect, sync_playwright

    origin = f"http://127.0.0.1:{port}"
    evidence.update({"playwright": version("playwright"), "outcomes": [], "status": "failed"})
    evidence.update(host_evidence())
    outcomes = evidence["outcomes"]
    fixture_avatars = frozenset()
    if chat_history is not None:
        import browser_chat_fixture
        fixture_avatars = browser_chat_fixture.AVATAR_URLS
    blocked = {"fixture_avatars": 0, "unexpected_external": 0}
    evidence["blocked_browser_requests"] = blocked
    phase = "launch"
    try:
        with sync_playwright() as driver:
            browser = driver.chromium.launch(channel="chrome", headless=True, chromium_sandbox=True)
            try:
                if re.fullmatch(r"[0-9]+(?:\.[0-9]+){3}", browser.version):
                    evidence["chromium"] = browser.version
                context = browser.new_context(locale="en-US", timezone_id="Asia/Taipei", viewport={"width": 1440, "height": 960})
                context.route("**/*", browser_request_guard(origin, fixture_avatars, blocked))
                context.set_default_timeout(15000)
                page = context.new_page()
                page_errors = []
                page.on("pageerror", lambda _: page_errors.append(True))
                dashboard = {"/api/status", "/api/scenes", "/api/logs", "/api/models", "/api/usage"}
                successful = set()

                def on_response(response):
                    path = urlsplit(response.url).path
                    if path in dashboard and response.request.method == "GET" and response.status == 200:
                        successful.add(path)

                page.on("response", on_response)

                def match(method, path):
                    return lambda response: (
                        response.url == origin + path and response.request.method == method
                    )

                phase = "signed-out"
                with page.expect_response(match("GET", "/api/auth/session")) as initial:
                    response = page.goto(origin + "/")
                    assert response.status == 200
                assert initial.value.status == 401
                connect = page.get_by_role("button", name="Connect", exact=True)
                expect(connect).to_be_visible()
                outcomes.append({"action": "hydrated-signed-out", "status": 401})

                phase = "login"
                page.locator('input[type="password"]').fill(token)
                with page.expect_response(match("POST", "/api/auth/session")) as login:
                    connect.click()
                assert login.value.status == 200
                settings = page.get_by_role("link", name="Settings", exact=True)
                expect(settings).to_be_visible()
                cookie = next(c for c in context.cookies(origin + "/api/status")
                              if c["name"] == "humanengine-session")
                assert cookie["httpOnly"] and cookie["sameSite"] == "Strict" and cookie["path"] == "/api"
                assert page.evaluate(
                    "(token) => !Object.values(localStorage).some(v => v.includes(token)) && "
                    "!Object.values(sessionStorage).some(v => v.includes(token))", token
                )
                outcomes.append({"action": "cookie-login", "status": 200})

                phase = "dashboard"
                # Playwright dispatches response callbacks while browser calls are
                # pending. The dashboard clears its busy state after all five reads.
                for _ in range(150):
                    if dashboard <= successful:
                        break
                    page.wait_for_timeout(100)
                assert dashboard <= successful
                outcomes.append({"action": "dashboard-real-api", "status": 200, "reads": len(successful)})

                phase = "settings-write"
                settings.click()
                name = page.locator('#identity input[maxlength="100"]')
                expect(name).to_have_count(1)
                expect(name).to_be_visible()
                edited = "Synthetic browser preview " + uuid.uuid4().hex
                name.fill(edited)
                with page.expect_response(match("PUT", "/api/settings")) as saved:
                    page.get_by_role("button", name="Save settings", exact=True).click()
                assert saved.value.status == 200
                expect(page.get_by_text("Settings saved", exact=True)).to_be_visible()
                outcomes.append({"action": "settings-write", "status": 200})

                phase = "settings-reload"
                with page.expect_response(match("GET", "/api/settings")) as read:
                    assert page.goto(origin + "/settings").status == 200
                assert read.value.status == 200
                expect(name).to_have_value(edited)
                outcomes.append({"action": "settings-reload-found-write", "status": 200})

                phase = "plugin-api-badges"
                with page.expect_response(match("GET", "/api/plugins")) as listed:
                    assert page.goto(origin + "/plugins").status == 200
                assert listed.value.status == 200
                expect(page.get_by_role("heading", name="Plugins", exact=True)).to_be_visible()
                fixtures = page.get_by_role("button", name=re.compile(r"^Package acceptance fixture\b"))
                if operator_plugins:
                    from operator_packages import PACKAGES
                    expect(fixtures).to_have_count(0)
                    rows = listed.value.json()
                    assert sorted(row["id"] for row in rows) == sorted(PACKAGES)
                    for row in rows:
                        assert row["apiVersion"] == 2 and row["builtin"] is False
                        plugin = page.get_by_role("button", name=re.compile("^" + re.escape(row["name"]) + r"\b"))
                        expect(plugin.get_by_text("V2", exact=True)).to_be_visible()
                        expect(plugin).to_contain_text("Local plugin")
                    outcomes.append({"action": "operator-four-external-v2-plugin-badges", "status": 200})
                else:
                    expect(fixtures).to_have_count(2)
                    for i in range(2):
                        fixture = fixtures.nth(i)
                        expect(fixture.get_by_text("Disabled", exact=True)).to_be_visible()
                        expect(fixture.get_by_text("V1", exact=True)).to_be_visible()
                        expect(fixture).to_contain_text("v1 · Local plugin")
                    typing = page.get_by_role("button", name=re.compile(r"^Typing delay\b"))
                    expect(typing.get_by_text("V2", exact=True)).to_be_visible()
                    expect(typing).to_contain_text("v0.1.0 · Built-in")
                outcomes.append({"action": "plugin-api-version-badges", "status": 200})

                def usage_phase(code):
                    nonlocal phase
                    phase = code

                assert_usage_today(page, origin, usage_start, expect, usage_phase)
                outcomes.append({"action": "usage-today-local-day-and-hourly-chart", "status": 200})

                if chat_history is not None:
                    phase = "dense-chat-local-before-platform"
                    browser_chat_fixture.assert_history(page, origin, expect, chat_history, with_image=chat_image)
                    if chat_image:
                        outcomes.append({"action": "standalone-signed-unnamed-group-profile-and-card", "status": 200})
                        outcomes.append({"action": "standalone-expired-sticker-rendered", "status": 200,
                                         "width": 3, "height": 2})
                    outcomes.append({"action": "dense-chat-local-before-platform", "status": 200})

                phase = "logout-submit"
                with page.expect_response(match("DELETE", "/api/auth/session")) as logout:
                    page.get_by_role("button", name="Disconnect", exact=True).click()
                    phase = "logout-response"
                assert logout.value.status == 200
                phase = "logout-signed-out"
                expect(connect).to_be_visible()
                phase = "logout-protected-api"
                assert page.evaluate(
                    "async () => (await fetch('/api/status', {"
                    "headers:{'X-HumanEngine-Session':'1'},credentials:'same-origin'})).status"
                ) == 401
                phase = "logout-cookie-removed"
                assert not any(c["name"] == "humanengine-session"
                               for c in context.cookies(origin + "/api/status"))
                outcomes.append({"action": "logout", "status": 200})
                outcomes.append({"action": "protected-api-after-logout", "status": 401})
                phase = "browser-page-errors"
                assert not page_errors
                phase = "browser-unexpected-external"
                assert not blocked["unexpected_external"]
            finally:
                browser.close()
        evidence["status"] = "passed"
    except Exception as error:
        # Playwright errors can contain DOM and action arguments. Never publish
        # them or collect screenshots, traces, cookie values, URLs or payloads.
        evidence["failed_phase"] = phase
        if phase == "launch":
            evidence["launch_category"] = launch_category(error)
        raise AssertionError(f"Chromium UI smoke failed at {phase}") from None
