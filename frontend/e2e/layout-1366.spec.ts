import { expect, test, type Page } from '@playwright/test';

async function assertGeometricLayout(page: Page, routeLabel: string, keySelectors: string[]): Promise<void> {
  const overflow = await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    innerWidth: window.innerWidth,
  }));
  expect(
    overflow.scrollWidth,
    `${routeLabel} 出现横向溢出: scrollWidth=${overflow.scrollWidth}, innerWidth=${overflow.innerWidth}`,
  ).toBeLessThanOrEqual(overflow.innerWidth + 1);

  const headerText = await page.locator('.app-header').innerText();
  expect(headerText, `${routeLabel} 顶栏不应暴露英文状态 unavailable`).not.toContain('unavailable');

  for (const selector of keySelectors) {
    const loc = page.locator(selector).first();
    await expect(loc, `${routeLabel} 核心元素不可见: ${selector}`).toBeVisible();
    const box = await loc.boundingBox();
    expect(box, `${routeLabel} 缺少边界框: ${selector}`).not.toBeNull();
    if (box) {
      expect(box.x, `${routeLabel} 元素左越界: ${selector}`).toBeGreaterThanOrEqual(-1);
      expect(box.x + box.width, `${routeLabel} 元素右越界: ${selector}`).toBeLessThanOrEqual(1367);
    }
  }
}

test('NFR-11: 核心界面在 1366x768 视口下布局完整、无横向溢出且无控制台异常', async ({ page }) => {
  const errors: string[] = [];
  page.on('pageerror', (err) => errors.push(`pageerror: ${err.message}`));
  page.on('console', (msg) => {
    if (msg.type() === 'error') {
      const text = msg.text();
      // 忽略内存模式下预期未挂载路由引发的资源 404 控制台日志
      if (!text.includes('404')) {
        errors.push(`console.error: ${text}`);
      }
    }
  });

  await page.goto('/login');
  await assertGeometricLayout(page, '/login', ['.app-header', '.auth-card', 'input[type="email"]', 'button[type="submit"]']);

  await page.goto('/register');
  await assertGeometricLayout(page, '/register', ['.app-header', '.auth-card', 'button[type="submit"]']);

  const email = `e2e-${Date.now()}@example.edu.cn`;
  await page.fill('input[type="email"]', email);
  await page.fill('input[placeholder*="姓名"], input[type="text"]', '布局验收用户');
  await page.fill('input[type="password"]', 'Abc12345');
  await page.click('button[type="submit"]');
  await page.waitForURL('**/login*');

  await page.fill('input[type="email"]', email);
  await page.fill('input[type="password"]', 'Abc12345');
  await page.click('button[type="submit"]');
  await page.waitForURL('**/kbs');
  await assertGeometricLayout(page, '/kbs', ['.app-header', '.page-title', 'form button[type="submit"]']);

  await page.fill('input[placeholder*="知识库"]', '1366布局验收知识库');
  await page.click('form button[type="submit"]');
  const openLink = page.locator('a', { hasText: '打开' }).first();
  await expect(openLink).toBeVisible();
  const kbHref = await openLink.getAttribute('href');
  expect(kbHref).toBeTruthy();

  await openLink.click();
  await page.waitForURL('**/kbs/*');
  await assertGeometricLayout(page, '/kbs/:kbId', ['.breadcrumb', '.page-head', '.grid-2', 'input[type="file"]']);

  await page.goto(`${kbHref}/settings`);
  await assertGeometricLayout(page, '/kbs/:kbId/settings', ['.breadcrumb', '.page-title', 'form button[type="submit"]']);

  await page.goto(`${kbHref}/graph`);
  await assertGeometricLayout(page, '/kbs/:kbId/graph', ['.breadcrumb', '.page-title']);

  await page.goto('/graph');
  await assertGeometricLayout(page, '/graph', ['.breadcrumb', '.page-title']);

  await page.goto('/qa');
  await assertGeometricLayout(page, '/qa', ['.qa-layout', '.qa-sidebar', '.qa-main', '#qa-question', 'button[type="submit"]']);

  await page.goto('/settings');
  await assertGeometricLayout(page, '/settings', ['.page-title', 'button[type="submit"]']);

  await page.goto('/documents/00000000-0000-0000-0000-000000000000/reading?page=1');
  await assertGeometricLayout(page, '/documents/:id/reading', ['.app-header', '.breadcrumb', '.reading-pager']);

  expect(errors, `发现页面运行时错误:\n${errors.join('\n')}`).toEqual([]);
});
