import { chromium } from 'playwright';
import fs from 'node:fs';
const [site, out] = process.argv.slice(2);
const canvas = JSON.parse(fs.readFileSync(`${site}/canvas.json`, 'utf8'));
const proxy = process.env.HTTPS_PROXY ? { server: process.env.HTTPS_PROXY } : undefined;
const browser = await chromium.launch({ proxy, args: ['--ignore-certificate-errors'] });
for (const name of canvas.order) {
  const b = canvas.boards[name];
  const page = await browser.newPage({ viewport: { width: b.w, height: b.h }, deviceScaleFactor: 2, colorScheme: 'light' });
  page.on('pageerror', (e) => console.error(name, e.message));
  await page.goto(`file://${site}/${name}`);
  await page.waitForSelector('body[data-ready="1"]', { timeout: 10000 });
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(500);
  const file = `${out}/${name.replace('.dc.html', '')}.png`;
  await page.screenshot({ path: file, fullPage: true });
  console.log('ok', file);
  await page.close();
}
await browser.close();
