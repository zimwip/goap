// The directory of a project (ADR 0178): folders made of smart tags, arranged by drag and drop, nodes dropped into them
// from a link to a node (here the title of a node editor, reached by dragging over the tab of the project).
import { expect, test, type Page } from '@playwright/test';
import { demoProject } from './session';

/** The id of a node of the demo data (a unit of its organisation, held by the platform graph), by key, read at the head of main of its namespace. */
async function nodeId(page: Page, namespace: string, key: string): Promise<string> {
  return page.evaluate(
    async ({ namespace, key }) => {
      const r = await fetch('/goap.graph.v1.GraphService/ListCommitNodes', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${localStorage.getItem('goap.token')}` },
        body: JSON.stringify({ view: { namespace, branch: 'main' }, query: key, limit: 20 }),
      });
      const text = await r.text();
      const body = JSON.parse(text) as { nodes?: { id: string; key: string }[] };
      return body.nodes?.find((n) => n.key === key)?.id ?? `no ${key}: ${r.status} ${text.slice(0, 400)}`;
    },
    { namespace, key },
  );
}

test('folders of a project are arranged and filled by drag and drop', async ({ page }) => {
  const stamp = Date.now().toString(36);
  const [docs, specs] = [`Docs ${stamp}`, `Specs ${stamp}`];
  await page.goto(`/project?key=${demoProject.key}`);
  await page.getByRole('tab', { name: 'Directory' }).click();
  const pane = page.getByRole('tabpanel');
  const tree = pane.getByRole('tree');

  // two top-level folders, then the second dragged into the first
  for (const name of [docs, specs]) {
    await pane.getByLabel('Folder name').fill(name);
    await pane.getByRole('button', { name: 'New folder', exact: true }).click();
    await expect(tree.getByRole('treeitem', { name: new RegExp(name) })).toBeVisible();
  }
  await tree.getByRole('treeitem', { name: new RegExp(specs) }).dragTo(tree.getByRole('treeitem', { name: new RegExp(docs) }));
  const docsItem = tree.getByRole('treeitem', { name: new RegExp(docs) });
  await expect(docsItem).toHaveAttribute('aria-expanded', 'true');
  await expect(tree.getByRole('treeitem', { name: new RegExp(specs) })).toHaveAttribute('style', /padding-left: 1\.35rem/);

  // a node: its editor's title dragged over the tab of the project, then onto the folder
  const id = await nodeId(page, 'organisation', 'ORG-ACME');
  expect(id).not.toMatch(/^no /);
  const projectTab = page.getByRole('tablist', { name: 'Open tabs' }).getByRole('tab').filter({ hasText: /PROJ-DEMO|Online commerce/ }).first();
  await projectTab.dblclick(); // pinned: opening the node keeps it
  // the node's tab, opened in the page (a reload would close the tabs): the route of a tab is its location
  await page.evaluate((url) => {
    history.pushState(null, '', url);
    dispatchEvent(new PopStateEvent('popstate'));
  }, `/node?id=${id}&key=ORG-ACME`);
  const title = page.getByRole('heading', { name: 'ORG-ACME', level: 2 });
  await expect(title).toBeVisible();

  // what the title carries, kept to drop it below
  await page.evaluate(() => {
    document.addEventListener('dragstart', (e) => ((window as unknown as { dragged: string }).dragged = e.dataTransfer?.getData('application/x-goap-node') ?? ''));
  });
  await title.hover();
  await page.mouse.down();
  const box = (await projectTab.boundingBox())!;
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2, { steps: 5 });
  const tree2 = page.getByRole('tree', { name: /^Folders of/ });
  await expect(tree2).toBeVisible(); // the tab under a dragged node shows after a moment, on the pane it was left on
  await page.mouse.up();
  // the editor of the node, the source of the drag, is gone with its tab: the browser automation stops delivering the
  // drag there, so the rest of it is dispatched as the browser would
  const dragged = await page.evaluate(() => (window as unknown as { dragged: string }).dragged);
  expect(dragged).toContain(id);
  const data = await page.evaluateHandle((d) => {
    const t = new DataTransfer();
    t.setData('application/x-goap-node', d);
    return t;
  }, dragged);
  await tree2.getByRole('treeitem', { name: new RegExp(docs) }).dispatchEvent('dragover', { dataTransfer: data });
  const specsItem = tree2.getByRole('treeitem', { name: new RegExp(specs) });
  await expect(specsItem).toBeVisible(); // a closed folder under a drag unfolds after a moment
  await specsItem.dispatchEvent('dragover', { dataTransfer: data });
  await specsItem.dispatchEvent('drop', { dataTransfer: data });

  await specsItem.click();
  await expect(page.getByRole('tabpanel').getByRole('table').getByRole('button', { name: 'ORG-ACME' })).toBeVisible();
  await expect(page.getByRole('tabpanel').getByRole('table')).toContainText('follows');
});
