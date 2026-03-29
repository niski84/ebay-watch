#!/usr/bin/env node
/**
 * Public eBay search (www.ebay.com) — no API, no login.
 * Default: Playwright **Firefox** (Chromium can OOM on huge SERP DOM).
 * eBay rotates markup: classic `li.s-item` vs newer `li.s-card` — we parse both.
 *
 * Env:
 *   EBAY_PW_BROWSER=chromium   — optional override
 *   EBAY_PW_HEADLESS=0         — show browser (debug bot blocks / layout)
 *   EBAY_PW_SLOWMO_MS=80       — slow down actions when headed (optional)
 *   EBAY_SKIP_ITEM_GALLERY=1   — skip /itm/ visits (SERP thumb only; faster)
 *   EBAY_ITEM_GALLERY_CONCURRENCY=3 — parallel tabs for item-page gallery scrape
 *
 * Setup: npm install (runs playwright install)
 *
 * Args:
 *   Keyword mode: query, limit[, LH_ItemCondition pipe] e.g. "1000|4000" (optional 3rd arg).
 *   URL mode: --url <full eBay search URL> limit
 */
import * as cheerio from "cheerio";
import { chromium, firefox } from "playwright";

const argv = process.argv.slice(2);
const headless = process.env.EBAY_PW_HEADLESS !== "0" && process.env.EBAY_PW_HEADLESS !== "false";
const useChromium = process.env.EBAY_PW_BROWSER === "chromium";
const slowMo = Math.min(Math.max(parseInt(process.env.EBAY_PW_SLOWMO_MS || "0", 10) || 0, 0), 500);

let url = "";
let limit = 50;
if (argv[0] === "--url") {
  const rawUrl = (argv[1] || "").trim();
  limit = Math.min(Math.max(parseInt(argv[2] || "50", 10) || 50, 1), 60);
  if (!rawUrl) {
    console.error("ebay-search: --url requires URL");
    process.exit(2);
  }
  let u;
  try {
    u = new URL(rawUrl.startsWith("http") ? rawUrl : `https://${rawUrl}`);
  } catch {
    console.error("ebay-search: invalid URL");
    process.exit(2);
  }
  const host = u.hostname.replace(/^www\./i, "");
  if (!/ebay\.com$/i.test(host)) {
    console.error("ebay-search: URL host must be *.ebay.com");
    process.exit(2);
  }
  if (!/\/sch\//i.test(u.pathname)) {
    console.error("ebay-search: URL path should include /sch/");
    process.exit(2);
  }
  u.protocol = "https:";
  u.hostname = "www.ebay.com";
  u.searchParams.set("_ipg", String(limit));
  url = u.toString();
} else {
  const q = (argv[0] || "").trim();
  limit = Math.min(Math.max(parseInt(argv[1] || "50", 10) || 50, 1), 60);
  const conditionPipe = (argv[2] || "").trim();
  if (!q) {
    console.error("ebay-search: missing query argv");
    process.exit(2);
  }
  if (conditionPipe && !/^[\d|]+$/.test(conditionPipe)) {
    console.error("ebay-search: invalid condition pipe (expect digits and | only)");
    process.exit(2);
  }
  url = `https://www.ebay.com/sch/i.html?_nkw=${encodeURIComponent(q)}&_ipg=${limit}`;
  if (conditionPipe) {
    url += `&LH_ItemCondition=${encodeURIComponent(conditionPipe)}`;
  }
}

function normalizeUrl(href) {
  if (!href) {
    return "";
  }
  let u = href;
  if (u.startsWith("//")) {
    u = "https:" + u;
  } else if (u.startsWith("/")) {
    u = "https://www.ebay.com" + u;
  }
  return u.split("?")[0].split("#")[0];
}

/** Map thumb CDN paths to full-size image paths where applicable. */
function canonicalizeEbayImageUrlInput(src) {
  if (!src || typeof src !== "string") {
    return "";
  }
  let u = src.trim();
  if (u.startsWith("//")) {
    u = "https:" + u;
  }
  return u.replace(/\/thumbs\/images\/g\//i, "/images/g/");
}

/** Prefer large eBay CDN still (s-l1600) for dashboard lightbox. */
function upgradeEbayImageUrl(src) {
  const t = canonicalizeEbayImageUrlInput(src);
  if (!t || !/^https?:\/\//i.test(t)) {
    return "";
  }
  const q = t.includes("?") ? t.slice(t.indexOf("?")) : "";
  const base = t.split("?")[0].split("#")[0];
  const m = base.match(/^(https?:\/\/i\.ebayimg\.com\/images\/g\/[^/]+\/)s-l\d+(\.[^.]+)$/i);
  if (m) {
    return m[1] + "s-l1600" + m[2] + q;
  }
  return base + q;
}

/** One key per distinct listing photo (eBay folder under /images/g/<id>/). */
function galleryDedupKey(fullUrl) {
  const noq = fullUrl.split("?")[0].split("#")[0];
  const m = noq.match(/^(https?:\/\/i\.ebayimg\.com\/images\/g\/[^/]+\/)/i);
  if (m) {
    return m[1].toLowerCase();
  }
  const m2 = noq.match(/^(https?:\/\/[^/]+\.ebayimg\.com\/images\/g\/[^/]+\/)/i);
  if (m2) {
    return m2[1].toLowerCase();
  }
  return noq.toLowerCase();
}

function resolutionScore(url) {
  const m = (url || "").match(/\/s-l(\d+)(?:\.|-|\/)/i);
  return m ? parseInt(m[1], 10) : 0;
}

/** Dedupe by image id, keep highest-res URL per photo, preserve gallery order (max 24). */
function finalizeGalleryUrls(stringUrls) {
  const keyOrder = [];
  const best = new Map();
  for (const s of stringUrls) {
    if (!s || typeof s !== "string") {
      continue;
    }
    const t = canonicalizeEbayImageUrlInput(s);
    const up =
      upgradeEbayImageUrl(t) ||
      (t.startsWith("//") ? "https:" + t : "") ||
      normalizeUrl(t) ||
      t;
    if (!up || !/^https?:\/\//i.test(up)) {
      continue;
    }
    const key = galleryDedupKey(up);
    if (!best.has(key)) {
      keyOrder.push(key);
    }
    const prev = best.get(key);
    if (!prev || resolutionScore(up) > resolutionScore(prev)) {
      best.set(key, up);
    }
  }
  return keyOrder.map((k) => best.get(k)).filter(Boolean).slice(0, 24);
}

function mergeGalleries(existing, fromPage) {
  return finalizeGalleryUrls([...(existing || []), ...(fromPage || [])]);
}

function parseSrcsetUrls(srcset) {
  if (!srcset || typeof srcset !== "string") {
    return [];
  }
  return srcset
    .split(",")
    .map((part) => part.trim().split(/\s+/)[0])
    .filter((x) => x && !x.startsWith("data:"));
}

function collectListingImageUrls($, $el) {
  const raw = new Set();
  const add = (v) => {
    const t = (v || "").trim();
    if (t && !t.startsWith("data:")) {
      raw.add(t);
    }
  };
  $el
    .find(
      "img.s-card__image, img.s-item__image-img, .s-item__image-wrapper img, .s-card__image-container img, a.s-item__link img",
    )
    .each((_, im) => {
      const $img = $(im);
      add($img.attr("src"));
      for (const p of parseSrcsetUrls($img.attr("srcset") || "")) {
        add(p);
      }
    });
  $el.find("img[src*='ebayimg']").each((_, im) => {
    const $img = $(im);
    add($img.attr("src"));
    for (const p of parseSrcsetUrls($img.attr("srcset") || "")) {
      add(p);
    }
  });
  return finalizeGalleryUrls(Array.from(raw)).slice(0, 16);
}

/** eBay SERP subtitle: "Pre-Owned · Size 36 in x 30 in" or "Brand New · DVD · ..." */
function parseConditionLine(raw) {
  const t = (raw || "").replace(/\s+/g, " ").trim();
  if (!t) {
    return { condition: "", listingDetails: "" };
  }
  const parts = t.split(/\s*·\s*/).map((p) => p.trim()).filter(Boolean);
  if (parts.length === 0) {
    return { condition: "", listingDetails: "" };
  }
  const condition = parts[0];
  const listingDetails = parts.length > 1 ? parts.slice(1).join(" · ") : "";
  return { condition, listingDetails };
}

function extractRows($, limit) {
  const rows = [];
  const seen = new Set();

  $("ul.srp-results > li.s-card, ul.srp-results > li.s-item, ul.srp-results li.s-card, ul.srp-results li.s-item").each(
    (_, el) => {
      if (rows.length >= limit) {
        return false;
      }
      const $el = $(el);
      if (!$el.is(".s-card, .s-item")) {
        return;
      }

      let href = "";
      let itemId = "";
      $el.find('a[href*="/itm/"]').each((__, a) => {
        const h = ($(a).attr("href") || "").trim();
        const m = h.match(/\/itm\/(\d+)/);
        if (m && !itemId) {
          itemId = m[1];
          href = h;
        }
      });
      if (!itemId || seen.has(itemId)) {
        return;
      }

      let title =
        $el.find(".s-card__title").first().text() ||
        $el.find(".s-item__title").first().text() ||
        $el.find("img.s-card__image").attr("alt") ||
        $el.find("img.s-item__image-img").attr("alt") ||
        "";
      title = title
        .replace(/\s+/g, " ")
        .replace(/\s*Opens in (a )?new window( or tab)?\.?\s*$/gi, "")
        .replace(/['\u2019]+\s*$/g, "")
        .trim();

      if (!title || title.includes("Shop on eBay")) {
        return;
      }

      const priceText = $el
        .find(".s-card__price, .s-item__price")
        .first()
        .text()
        .replace(/\s+/g, " ")
        .trim();

      const primarySrc =
        $el.find("img.s-card__image, img.s-item__image-img").first().attr("src") ||
        $el.find("img[src*='ebayimg']").first().attr("src") ||
        "";
      let gallery = collectListingImageUrls($, $el);
      if (gallery.length === 0 && primarySrc) {
        const up = upgradeEbayImageUrl(primarySrc) || normalizeUrl(primarySrc) || primarySrc;
        gallery = up ? [up] : [];
      }
      const img = gallery[0] || "";

      const subtitleRaw =
        $el.find(".s-card__subtitle").first().text() ||
        $el.find(".s-card__subtitle-row").first().text() ||
        $el.find(".s-item__subtitle").first().text() ||
        $el.find(".s-item__subtitle-row").first().text() ||
        "";
      const { condition, listingDetails } = parseConditionLine(subtitleRaw);

      seen.add(itemId);
      rows.push({
        itemId,
        title,
        itemWebUrl: normalizeUrl(href) || `https://www.ebay.com/itm/${itemId}`,
        imageUrl: img,
        imageUrls: gallery,
        priceValue: priceText,
        priceCurrency: "USD",
        condition,
        listingDetails,
      });
    },
  );

  return rows;
}

async function scrapeItemPageGalleryUrls(page) {
  const blocked = await page.evaluate(() => {
    const h = document.documentElement?.innerHTML || "";
    const t = document.body?.innerText?.slice(0, 500) || "";
    return (
      /pardon our interruption|unusual traffic|robot check/i.test(h) ||
      /checking your browser/i.test(t)
    );
  });
  if (blocked) {
    return [];
  }
  /**
   * Only collect images from the **listing** gallery. Full-page / script / HTML regex
   * scraping pulled in unrelated ebayimg URLs (related items, ads, recommendations).
   */
  return page.evaluate(() => {
    const raw = [];
    const add = (u) => {
      if (!u || typeof u !== "string") {
        return;
      }
      let t = u.trim();
      if (t.startsWith("//")) {
        t = "https:" + t;
      }
      if (/^https?:\/\//i.test(t) && !t.startsWith("data:") && /ebayimg\.com/i.test(t)) {
        raw.push(t);
      }
    };

    function addSrcsetFrom(el) {
      const ss = el.getAttribute && el.getAttribute("srcset");
      if (!ss) {
        return;
      }
      ss.split(",").forEach((part) => {
        const u = part.trim().split(/\s+/)[0];
        add(u);
      });
    }

    /** Main item photo module only (not SERP, not “more items”, not footer). */
    function getListingGalleryRoot() {
      const panel = document.querySelector("#PicturePanel");
      if (panel) {
        return panel;
      }
      const xphotos = document.querySelector('[data-testid="x-photos-max-view"]');
      if (xphotos) {
        return xphotos;
      }
      const carousel = document.querySelector(
        ".ux-image-carousel-container, .ux-image-magnify__container, .vi-image-gallery",
      );
      if (carousel) {
        return carousel;
      }
      return null;
    }

    function collectImagesFromRoot(root) {
      if (!root || !root.querySelectorAll) {
        return;
      }
      const scoped =
        '[data-testid="x-photos-max-view"] img, ' +
        ".ux-image-filmstrip img, " +
        ".ux-image-filmstrip button img, " +
        ".ux-image-carousel-item img, " +
        ".ux-image-carousel img, " +
        ".ux-image-generic img, " +
        ".ux-image-magnify img, " +
        ".vi-image-gallery img, " +
        "div[itemscope] img[itemprop='image'], " +
        "picture img";
      try {
        root.querySelectorAll(scoped).forEach((img) => {
          add(img.getAttribute("src"));
          add(img.getAttribute("data-src"));
          add(img.getAttribute("data-lazy-src"));
          add(img.getAttribute("data-original"));
          add(img.getAttribute("data-zoom-src"));
          add(img.getAttribute("data-zoom-image"));
          add(img.getAttribute("data-large-image"));
          addSrcsetFrom(img);
        });
      } catch (_) {}

      try {
        root.querySelectorAll("picture source[srcset], picture source[src]").forEach((el) => {
          add(el.getAttribute("src"));
          addSrcsetFrom(el);
        });
      } catch (_) {}

      try {
        root.querySelectorAll("[style*='ebayimg']").forEach((el) => {
          const st = el.getAttribute("style") || "";
          const re = /https?:\/\/[^)'"\s]+ebayimg[^)'"\s]*/gi;
          let m;
          while ((m = re.exec(st))) {
            add(m[0]);
          }
        });
      } catch (_) {}
    }

    const pathMatch = location.pathname.match(/\/itm\/(\d+)/);
    const pageItemId = pathMatch ? pathMatch[1] : "";

    function productJsonLdMatchesThisListing(obj) {
      if (!pageItemId) {
        return true;
      }
      const urls = [];
      if (obj.url) {
        urls.push(String(obj.url));
      }
      const off = obj.offers;
      if (off) {
        if (Array.isArray(off)) {
          off.forEach((o) => {
            if (o && o.url) {
              urls.push(String(o.url));
            }
          });
        } else if (typeof off === "object" && off.url) {
          urls.push(String(off.url));
        }
      }
      if (urls.length === 0) {
        return true;
      }
      const needle = `/itm/${pageItemId}`;
      return urls.some((u) => u.includes(needle));
    }

    function isProductType(t) {
      if (!t) {
        return false;
      }
      const arr = Array.isArray(t) ? t : [t];
      return arr.some((x) => /product/i.test(String(x)));
    }

    function walkJsonLdCollectProductImages(obj, seen) {
      if (!obj || typeof obj !== "object" || seen.has(obj)) {
        return;
      }
      seen.add(obj);
      if (Array.isArray(obj)) {
        obj.forEach((x) => walkJsonLdCollectProductImages(x, seen));
        return;
      }
      if (obj["@graph"]) {
        walkJsonLdCollectProductImages(obj["@graph"], seen);
      }
      if (isProductType(obj["@type"]) && productJsonLdMatchesThisListing(obj)) {
        const im = obj.image;
        if (typeof im === "string") {
          add(im);
        } else if (Array.isArray(im)) {
          im.forEach((x) => {
            if (typeof x === "string") {
              add(x);
            } else if (x && typeof x === "object" && x.url) {
              add(String(x.url));
            }
          });
        } else if (im && typeof im === "object" && im.url) {
          add(String(im.url));
        }
      }
      for (const k of Object.keys(obj)) {
        if (k === "@context" || k === "@type") {
          continue;
        }
        const v = obj[k];
        if (v && typeof v === "object") {
          walkJsonLdCollectProductImages(v, seen);
        }
      }
    }

    try {
      document.querySelectorAll('script[type="application/ld+json"]').forEach((sc) => {
        const txt = sc.textContent || "";
        if (!txt.includes("Product") && !txt.includes("product")) {
          return;
        }
        try {
          walkJsonLdCollectProductImages(JSON.parse(txt), new WeakSet());
        } catch (_) {}
      });
    } catch (_) {}

    const root = getListingGalleryRoot();
    collectImagesFromRoot(root);

    return raw;
  });
}

async function primeItemGalleryDom(page) {
  await page
    .evaluate(() => {
      const roots = [
        ".ux-image-filmstrip",
        ".ux-image-carousel-container",
        ".ux-image-carousel",
        '[data-testid="x-photos-max-view"]',
        ".vi-image-gallery",
        "#PicturePanel",
      ];
      const seen = new Set();
      roots.forEach((sel) => {
        document.querySelectorAll(sel).forEach((el) => {
          if (seen.has(el)) {
            return;
          }
          seen.add(el);
          try {
            el.scrollIntoView({ block: "center", inline: "nearest" });
            const sw = el.scrollWidth;
            const cw = el.clientWidth;
            if (typeof sw === "number" && sw > cw + 8) {
              el.scrollLeft = sw;
              el.scrollLeft = Math.max(0, sw / 2);
              el.scrollLeft = 0;
            }
          } catch (_) {}
        });
      });
    })
    .catch(() => {});
}

async function enrichRowsGallery(browser, rows) {
  if (process.env.EBAY_SKIP_ITEM_GALLERY === "1") {
    console.error("[ebay-search] skipping item-page galleries (EBAY_SKIP_ITEM_GALLERY=1)");
    return;
  }
  const conc = Math.min(
    Math.max(parseInt(process.env.EBAY_ITEM_GALLERY_CONCURRENCY || "3", 10) || 3, 1),
    6,
  );
  console.error(
    `[ebay-search] item-page galleries: ${rows.length} listings, concurrency=${conc}`,
  );

  let next = 0;
  const settleMs = Math.min(
    Math.max(parseInt(process.env.EBAY_ITEM_GALLERY_SETTLE_MS || "700", 10) || 700, 200),
    3000,
  );

  async function runWorker(page) {
    await page.setExtraHTTPHeaders({ "Accept-Language": "en-US,en;q=0.9" });
    while (true) {
      const i = next++;
      if (i >= rows.length) {
        break;
      }
      const row = rows[i];
      const itemUrl = row.itemWebUrl || `https://www.ebay.com/itm/${row.itemId}`;
      try {
        await page.goto(itemUrl, { waitUntil: "domcontentloaded", timeout: 45000 });
        await new Promise((r) => setTimeout(r, settleMs));
        await primeItemGalleryDom(page);
        await new Promise((r) => setTimeout(r, 650));
        const raw1 = await scrapeItemPageGalleryUrls(page);
        await primeItemGalleryDom(page);
        await new Promise((r) => setTimeout(r, 400));
        const raw2 = await scrapeItemPageGalleryUrls(page);
        const mergedRaw = [...raw1, ...raw2];
        if (mergedRaw.length) {
          row.imageUrls = mergeGalleries(row.imageUrls, mergedRaw);
          row.imageUrl = row.imageUrls[0] || row.imageUrl;
        }
      } catch (e) {
        console.error(`[ebay-search] gallery fetch item=${row.itemId}:`, e?.message || e);
      }
    }
  }

  const pages = await Promise.all(Array.from({ length: conc }, () => browser.newPage()));
  try {
    await Promise.all(pages.map((p) => runWorker(p)));
  } finally {
    await Promise.all(pages.map((p) => p.close().catch(() => {})));
  }
}

async function loadSerpThenEnrich() {
  const launcher = useChromium ? chromium : firefox;
  const browser = await launcher.launch({
    headless,
    ...(slowMo > 0 ? { slowMo } : {}),
    ...(useChromium
      ? { args: ["--no-sandbox", "--disable-dev-shm-usage", "--disable-gpu"] }
      : {}),
  });
  try {
    const page = await browser.newPage();
    await page.setExtraHTTPHeaders({
      "Accept-Language": "en-US,en;q=0.9",
    });
    console.error(
      `[ebay-search] GET ${url} engine=${useChromium ? "chromium" : "firefox"} headless=${headless} slowMo=${slowMo}`,
    );
    await page.goto(url, { waitUntil: "domcontentloaded", timeout: 120000 });
    await page
      .waitForSelector("ul.srp-results li.s-card, ul.srp-results li.s-item", { timeout: 90000 })
      .catch(() => {});
    await new Promise((r) => setTimeout(r, 2000));

    const fullHtml = await page.content();
    const fragment = await page.evaluate(() => {
      const root =
        document.querySelector("ul.srp-results") ||
        document.querySelector(".srp-river-main") ||
        document.querySelector("[class*='srp-river']");
      return root ? root.outerHTML : document.body ? document.body.innerHTML : "";
    });
    await page.close();

    const $ = cheerio.load(fragment || "<ul></ul>");
    const rows = extractRows($, limit);
    if (rows.length > 0) {
      await enrichRowsGallery(browser, rows);
    }
    return { fullHtml, rows };
  } finally {
    await browser.close();
  }
}

let result;
for (let attempt = 1; attempt <= 2; attempt++) {
  try {
    result = await loadSerpThenEnrich();
    break;
  } catch (e) {
    if (attempt === 2) {
      console.error("[ebay-search] failed after 2 attempts:", e?.message || e);
      process.exit(7);
    }
    console.error(`[ebay-search] retry ${attempt + 1}:`, e?.message || e);
    await new Promise((r) => setTimeout(r, 2000));
  }
}

const fullHtml = result.fullHtml || "";
if (
  /pardon our interruption/i.test(fullHtml) ||
  /unusual traffic|robot check/i.test(fullHtml)
) {
  console.error("[ebay-search] eBay bot-interstitial. Try EBAY_PW_HEADLESS=0 (visible browser).");
  process.exit(4);
}

const rows = result.rows;
if (rows.length === 0) {
  console.error("[ebay-search] no listings parsed (empty results or markup changed).");
  process.exit(5);
}

process.stdout.write(JSON.stringify(rows));
