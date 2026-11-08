async (page) => {
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  const views = ["overview", "sms", "calls", "esim", "network", "radio", "tasks", "notify", "at"];
  const results = [];
  for (const viewport of [{ width: 1440, height: 960 }, { width: 390, height: 844 }]) {
    await page.setViewportSize(viewport);
    for (const view of views) {
      await page.locator(`button.tab[data-view="${view}"]`).click();
      await page.locator(`#${view}.active`).waitFor({ state: "visible" });
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1);
      if (overflow) throw new Error(`${view}: page overflows at width ${viewport.width}`);
      if (view === "notify") {
        const checkbox = await page.locator("#notify-sms").boundingBox();
        if (!checkbox || checkbox.width !== 16 || checkbox.height !== 16) throw new Error("notification checkbox is distorted");
        await page.screenshot({ path: `output/playwright/optimization-notify-${viewport.width}.png`, fullPage: true });
      }
      if (view === "tasks") {
        const select = await page.locator("#task-kind").boundingBox();
        if (!select || select.height < 36) throw new Error("task selector is too small");
      }
      results.push(`${viewport.width}:${view}`);
    }
    await page.locator('button.tab[data-view="radio"]').click();
    await page.route("**/api/ussd/ims", route => route.fulfill({ json: {
      ims_registered: false, ims_state: "unknown", ims_error: "unsupported",
    }}));
    await page.route("**/api/radio/cell-lock", route => route.fulfill({ json: { supported: false, locked: false }}));
    await page.route("**/api/radio/operators", route => route.fulfill({ json: { state: "idle", operators: [] }}));
    await page.evaluate(() => loadRadio());
    if (!(await page.locator("#ussd-ims-line").textContent()).includes("未知")) throw new Error("IMS unknown shown as unregistered");
    if (!(await page.locator("#cell-lock-status").textContent()).includes("无法查询")) throw new Error("unsupported lock shown as unlocked");
    if (!(await page.locator("#operator-scan-status").textContent()).includes("尚未扫描")) throw new Error("idle scan shown as completed");
    await page.screenshot({ path: `output/playwright/optimization-radio-${viewport.width}.png`, fullPage: true });
  }
  // Exercise actual call UI rendering without a device or a phone call.
  await page.evaluate(() => {
    clearInterval(sipTimer);
    sipTimer = null;
  });
  await page.waitForFunction(() => !sipInFlight);
  await page.evaluate(() => {
    window.__auditCall = { id: "ui-audit", direction: "inbound", state: "ringing", number: "10086", local_audio: true };
    updateIncomingCall({ calls: [window.__auditCall], native_alert_call_id: "ui-audit" });
  });
  if (await page.locator("#incoming-call-mute").isVisible()) throw new Error("browser offered separate ringing while native alert owns it");
  const nativeTimers = await page.evaluate(() => callRingTimer === null);
  if (!nativeTimers) throw new Error("duplicate browser ringing timer");
  await page.evaluate(() => updateIncomingCall({ calls: [window.__auditCall], native_alert_call_id: "" }));
  if (!(await page.locator("#incoming-call-mute").isVisible())) throw new Error("browser ringing fallback missing");
  await page.route("**/api/sip/status", route => route.fulfill({ json: {
    enabled: true, listening: true, calls: [
      { id: "ui-audit", direction: "inbound", state: "ringing", number: "10086", local_audio: true },
    ], native_alert_call_id: "",
  }}));
  await page.route("**/api/sip/answer", route => route.fulfill({ status: 502, json: { error: "接听失败，请重试" } }));
  await page.locator("#incoming-call-answer").click();
  await page.locator("#incoming-call-error").waitFor({ state: "visible" });
  if (!(await page.locator("#incoming-call-answer").isEnabled())) throw new Error("answer failed but retry stayed disabled");
  await page.evaluate(() => updateIncomingCall({ calls: [] }));
  if (await page.locator("#incoming-call").isVisible()) throw new Error("cancelled call left popup open");
  await page.locator('button.tab[data-view="overview"]').click();
  for (const theme of ["dark", "light", "auto"]) {
    await page.locator(`[data-theme-option="${theme}"]`).click();
    if (await page.locator(`[data-theme-option="${theme}"]`).getAttribute("aria-pressed") !== "true") {
      throw new Error(`theme ${theme} did not apply`);
    }
  }
  if (errors.length) throw new Error(JSON.stringify(errors));
  return { passed: results, states: ["IMS unknown", "cell lock unsupported", "scan idle",
    "native ringing ownership", "browser ringing fallback", "answer retry", "caller cancellation", "three themes"],
    pageErrors: errors };
}
