#!/usr/bin/env node
/**
 * Scrape eBay completed/sold listings for a query and return median sale price.
 * SERP-only — no item page visits, intentionally lightweight.
 *
 * Usage: node scripts/ebay-sold.mjs "2019 toyota camry" [limit] [sacat]
 *   sacat: optional eBay category ID (e.g. "6001" for Cars & Trucks)
 * Output (stdout): {"median":15500,"count":18,"prices":[...]}
 */
import * as cheerio from "cheerio";
import { firefox } from "playwright";

const query = (process.argv[2] || "").trim();
if (!query) {
  console.error("ebay-sold: missing query");
  process.exit(1);
}
const limit = Math.min(Math.max(parseInt(process.argv[3] || "25", 10) || 25, 5), 50);
const sacat = (process.argv[4] || "").trim();
const url =
  `https://www.ebay.com/sch/i.html?_nkw=${encodeURIComponent(query)}` +
  `&LH_Complete=1&LH_Sold=1&_ipg=${limit}` +
  (sacat ? `&_sacat=${encodeURIComponent(sacat)}` : "");

function parsePriceDollars(text) {
  // "$15,500" / "US $15,500" / "$15,500.00" → 15500
  // Reject price ranges ("$X to $Y")
  const t = (text || "").replace(/,/g, "").replace(/\s+/g, " ").trim();
  if (/ to /i.test(t)) return null;
  const m = t.match(/\$([\d]+(?:\.\d+)?)/);
  if (!m) return null;
  const n = Math.round(parseFloat(m[1]));
  return isNaN(n) || n <= 0 ? null : n;
}

function median(arr) {
  if (arr.length === 0) return 0;
  const s = [...arr].sort((a, b) => a - b);
  const mid = Math.floor(s.length / 2);
  return s.length % 2 !== 0 ? s[mid] : Math.round((s[mid - 1] + s[mid]) / 2);
}

const headless = process.env.EBAY_PW_HEADLESS !== "0";
const browser = await firefox.launch({
  headless,
  args: ["--disable-dev-shm-usage", "--no-sandbox"],
});

try {
  const page = await browser.newPage();
  await page.setExtraHTTPHeaders({ "Accept-Language": "en-US,en;q=0.9" });
  console.error(`[ebay-sold] GET ${url}`);
  await page.goto(url, { waitUntil: "domcontentloaded", timeout: 30000 });
  await page
    .waitForSelector("ul.srp-results li.s-item, ul.srp-results li.s-card", { timeout: 15000 })
    .catch(() => {});
  await new Promise((r) => setTimeout(r, 1500));

  const html = await page.evaluate(() => {
    const root =
      document.querySelector("ul.srp-results") ||
      document.querySelector(".srp-river-main") ||
      document.body;
    return root ? root.outerHTML : "";
  });
  await page.close();

  const $ = cheerio.load(html || "<ul></ul>");
  const prices = [];
  const listings = [];

  $("li.s-item, li.s-card").each((_, el) => {
    const $el = $(el);
    const title =
      $el.find(".s-item__title, .s-card__title").first().text().trim();
    if (!title || title.includes("Shop on eBay")) return;

    // Extract item URL
    let itemUrl = "";
    $el.find('a[href*="/itm/"]').each((__, a) => {
      if (!itemUrl) {
        const h = ($( a).attr("href") || "").trim();
        const m = h.match(/\/itm\/(\d+)/);
        if (m) itemUrl = `https://www.ebay.com/itm/${m[1]}`;
      }
    });

    // Extract thumbnail image from SERP (avoids extra per-item HTTP requests)
    let imageUrl = "";
    $el.find("img").each((__, img) => {
      if (imageUrl) return;
      const src = $(img).attr("src") || $(img).attr("data-src") || "";
      if (src && src.startsWith("https://") && !src.includes("pixel") && !src.includes("1x1")) {
        imageUrl = src;
      }
    });

    const priceText = $el
      .find(".s-item__price, .s-card__price")
      .first()
      .text()
      .trim();
    const p = parsePriceDollars(priceText);
    if (p !== null) {
      prices.push(p);
      listings.push({ title, price: p, url: itemUrl, image_url: imageUrl });
    }
  });

  console.error(`[ebay-sold] query="${query}" found ${listings.length} sold listings`);
  process.stdout.write(
    JSON.stringify({ median: median(prices), count: prices.length, prices, listings }),
  );
} finally {
  await browser.close();
}
