"""Opt-in hosted smoke against the installed Go preview, never an API stub."""
from importlib.metadata import version
from urllib.parse import urlsplit
import uuid


def run(port, token, evidence):
    # Kept out of normal package runs and out of both runtime images.
    from playwright.sync_api import expect, sync_playwright

    origin = f"http://127.0.0.1:{port}"
    evidence.update({"playwright": version("playwright"), "outcomes": [], "status": "failed"})
    outcomes = evidence["outcomes"]
    phase = "launch"
    try:
        with sync_playwright() as driver:
            browser = driver.chromium.launch(headless=True, chromium_sandbox=True)
            try:
                evidence["chromium"] = browser.version
                context = browser.new_context(locale="en-US", viewport={"width": 1440, "height": 960})
                external = [0]

                def on_request(route):
                    if route.request.url.startswith(origin + "/"):
                        route.continue_()
                    else:
                        external[0] += 1
                        route.abort()

                context.route("**/*", on_request)
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

                phase = "logout"
                with page.expect_response(match("DELETE", "/api/auth/session")) as logout:
                    page.get_by_role("button", name="Disconnect", exact=True).click()
                assert logout.value.status == 200
                expect(connect).to_be_visible()
                assert page.evaluate(
                    "async () => (await fetch('/api/status', {"
                    "headers:{'X-HumanEngine-Session':'1'},credentials:'same-origin'})).status"
                ) == 401
                assert not any(c["name"] == "humanengine-session"
                               for c in context.cookies(origin + "/api/status"))
                assert not page_errors and not external[0]
                outcomes.append({"action": "logout", "status": 200})
                outcomes.append({"action": "protected-api-after-logout", "status": 401})
            finally:
                browser.close()
        evidence["status"] = "passed"
    except Exception as error:
        # Playwright errors can contain DOM and action arguments. Never publish
        # them or collect screenshots, traces, cookie values, URLs or payloads.
        evidence.update({"failed_phase": phase, "error_type": type(error).__name__})
        raise AssertionError(f"Chromium UI smoke failed at {phase}") from None
