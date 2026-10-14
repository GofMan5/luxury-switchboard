from pathlib import Path
from playwright.sync_api import sync_playwright

URL = "http://127.0.0.1:1420/?fixture=1"
OUT = Path("artifacts/qa")
OUT.mkdir(parents=True, exist_ok=True)

with sync_playwright() as p:
    browser = p.chromium.launch(headless=True)
    for width, height in ((800, 600), (1280, 800), (1600, 1000)):
        page = browser.new_page(viewport={"width": width, "height": height}, device_scale_factor=1)
        page.goto(URL, wait_until="networkidle")
        page.wait_for_timeout(1200)
        page.screenshot(path=str(OUT / f"overview-{width}x{height}.png"), full_page=False)
        page.get_by_role("button", name="Insights").click()
        page.wait_for_timeout(1200)
        page.screenshot(path=str(OUT / f"insights-{width}x{height}.png"), full_page=False)
        page.get_by_role("button", name="Model Routes").click()
        page.wait_for_timeout(1200)
        page.screenshot(path=str(OUT / f"routes-{width}x{height}.png"), full_page=False)
        page.get_by_role("button", name="Tests").click()
        page.wait_for_timeout(1200)
        page.screenshot(path=str(OUT / f"tests-{width}x{height}.png"), full_page=False)
        page.get_by_role("button", name="Guardrails").click()
        page.wait_for_timeout(1200)
        page.screenshot(path=str(OUT / f"guardrails-{width}x{height}.png"), full_page=False)
        page.get_by_role("button", name="Settings").click()
        page.wait_for_timeout(1200)
        page.screenshot(path=str(OUT / f"settings-{width}x{height}.png"), full_page=False)
        overflow = page.evaluate("({body: document.body.scrollWidth, client: document.documentElement.clientWidth, root: document.documentElement.scrollWidth})")
        print(width, height, overflow)
        page.close()
    browser.close()
