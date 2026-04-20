import { chromium } from "playwright";

const BASE = "http://localhost:9202";
const browser = await chromium.launch({ headless: true });
const page = await browser.newPage();
let passed = 0; let failed = 0;

function ok(label) { console.log("  ✓", label); passed++; }
function fail(label, detail) { console.log("  ✗", label, detail || ""); failed++; }

page.on("pageerror", e => fail("JS page error: " + e.message));

console.log("\n── Test 1: page loads without auth ──");
await page.goto(BASE, { waitUntil: "networkidle" });
const title = await page.title();
title.includes("eBay") ? ok("page title: " + title) : fail("unexpected title: " + title);

console.log("\n── Test 2: login modal appears ──");
const overlay = page.locator("#login-overlay");
await overlay.waitFor({ state: "visible", timeout: 8000 }).catch(() => {});
const overlayVisible = await overlay.evaluate(el => window.getComputedStyle(el).display !== "none");
overlayVisible ? ok("login modal visible") : fail("login modal not visible");

console.log("\n── Test 3: wrong credentials show error ──");
await page.fill("#login-user", "wrong");
await page.fill("#login-pass", "bad");
await page.click("#login-form button[type=submit]");
await page.waitForTimeout(2000);
const errVisible = await page.locator("#login-error").evaluate(el => el.style.display !== "none" && el.textContent.length > 0);
const errText = await page.locator("#login-error").textContent();
errVisible ? ok("error shown: " + errText) : fail("error not shown after bad credentials");

console.log("\n── Test 4: correct credentials close modal ──");
await page.fill("#login-user", "admin");
await page.fill("#login-pass", "ebaywatch");
await page.click("#login-form button[type=submit]");
await page.waitForTimeout(3000);
const modalGone = await overlay.evaluate(el => window.getComputedStyle(el).display === "none");
modalGone ? ok("modal closed after correct login") : fail("modal still open after correct login");

console.log("\n── Test 5: API accessible after login ──");
const health = await page.evaluate(async () => {
  const stored = localStorage.getItem("ebay-watch-auth"); const r = await fetch("/api/health", { headers: stored ? { Authorization: "Basic " + stored } : {} });
  return r.status;
});
health === 200 ? ok("API returns 200 after login") : fail("API returned " + health);

console.log(`\n${passed} passed, ${failed} failed`);
await browser.close();
process.exit(failed > 0 ? 1 : 0);
