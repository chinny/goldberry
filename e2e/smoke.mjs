// Goldberry end-to-end smoke test (plan §13.3 step 4), run against a live
// server or the built image:
//
//   BASE_URL=http://localhost:8080 SETUP_TOKEN=... node smoke.mjs
//
// setup → add kid → deposit → kid PIN sign-in → request → admin approves
// from the bell → balances correct. Exits non-zero on any failure.
import { chromium } from "playwright";
import assert from "node:assert/strict";

const base = process.env.BASE_URL ?? "http://localhost:8080";
const token = process.env.SETUP_TOKEN;
if (!token) throw new Error("SETUP_TOKEN is required (it's in the server log)");

const browser = await chromium.launch(process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {});
const errors = [];
async function page(width = 390) {
  const ctx = await browser.newContext({ viewport: { width, height: 844 } });
  const p = await ctx.newPage();
  p.on("pageerror", (e) => errors.push(e.message));
  p.on("console", (m) => m.type() === "error" && errors.push(m.text()));
  return p;
}
const submit = (p) => p.click("main button[type=submit], .auth-card button[type=submit]");
const step = (name) => console.log(`· ${name}`);

try {
  const mom = await page();
  step("first run redirects to setup");
  await mom.goto(base + "/");
  assert.match(mom.url(), /\/setup$/);

  step("setup with the one-time token");
  await mom.fill("#setup_token", token);
  await mom.fill("#household_name", "Smoke Test Family");
  await mom.fill("#display_name", "Mom");
  await mom.fill("#username", "mom");
  await mom.fill("#password", "correct horse battery");
  await submit(mom);
  await mom.waitForURL("**/admin/users/new?role=kid");

  step("add a kid");
  await mom.fill("#display_name", "Ava");
  await mom.fill("#username", "ava");
  await mom.fill("#pin", "2468");
  await submit(mom);
  await mom.waitForURL("**/admin/kids/*");
  const kidURL = mom.url();

  step("deposit $25.00 with a comment");
  await mom.goto(kidURL + "/funds");
  await mom.fill("#amount", "25.00");
  await mom.click('button[data-text="Chores"]');
  await mom.click("button[data-label]");
  await mom.waitForURL(kidURL);
  assert.match(await mom.textContent("main"), /\$25\.00/);

  step("kid signs in with the PIN pad");
  const ava = await page(820);
  await ava.goto(base + "/login");
  await ava.fill("#username", "ava");
  await submit(ava);
  await ava.waitForSelector(".pin-pad button[data-digit='2']");
  for (const d of "2468") await ava.click(`.pin-pad button[data-digit="${d}"]`);
  await ava.waitForURL(base + "/");
  assert.match(await ava.textContent(".jar .big"), /\$25\.00/);

  step("kid asks for $20.00; it is held");
  await ava.goto(base + "/requests/new");
  await ava.fill("#amount", "20");
  await ava.fill("#reason", "Movie with Jess");
  await submit(ava);
  await ava.waitForURL(base + "/");
  assert.match(await ava.textContent(".jar .big"), /\$5\.00/);
  assert.match(await ava.textContent(".jar .held"), /\$20\.00 waiting/);

  step("parent approves from the bell");
  await mom.goto(base + "/notifications");
  assert.match(await mom.textContent("main"), /Ava asked for \$20\.00/);
  await mom.click("main .actions form button.btn-primary");
  await mom.waitForURL("**/notifications");
  assert.match(await mom.textContent("main"), /Approved \$20\.00 for Ava/);

  step("balances are correct");
  await ava.goto(base + "/");
  assert.match(await ava.textContent(".jar .big"), /\$5\.00/);
  assert.equal(await ava.locator(".jar .held").count(), 0);
  await mom.goto(kidURL);
  assert.match(await mom.textContent(".jar-row"), /\$5\.00/);

  assert.deepEqual(errors, [], "browser console errors");
  console.log("smoke test passed");
} finally {
  await browser.close();
}
