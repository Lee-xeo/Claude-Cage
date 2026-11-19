import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
import { createWorkbench } from "./workbench-fixture.mjs";

const engine = process.env.CAGE_TEST_BROWSER || "chromium";
const output = process.env.CAGE_TEST_CAPTURE || "/tmp/cage-session-order";
let state, opens = 0;
const sessions = [
  { id:"order-idle", title:"已打开的空闲会话", updated_at:"2026-11-07T10:00:00Z" },
  { id:"order-active", title:"正在执行的会话", active:true, updated_at:"2026-11-07T09:00:00Z" },
  { id:"order-saved", title:"保存的会话", updated_at:"2026-11-07T08:00:00Z" },
];
const firstProject = ".claude-cage-workspace";
const secondProject = "产品文档";
const thirdProject = "设计稿";
const projectPath = "/Users/shihao/Projects";
const occupied = { id: "order-occupied", title: "其他窗口正在运行", running: true, workspace: `${projectPath}/design` };
const available = { id: "order-available", title: "可接续的设计会话", workspace: occupied.workspace };
const fixture = await createWorkbench({ url: process.env.CAGE_UI_URL || "http://127.0.0.1:5217",
  beforeNavigate: (_page, state) => {
    const workspace = state.settings.workspace;
    state.status.project_groups = [
      { workspace, name: firstProject, folders: [workspace] },
      { workspace: `${projectPath}/docs`, name: secondProject, folders: [`${projectPath}/docs`, `${projectPath}/assets`] },
      { workspace: `${projectPath}/design`, name: thirdProject, folders: [`${projectPath}/design`] },
    ];
  },
  cliRequest: async (route, path) => {
    if (path === "/cli/sessions") return route.fulfill({json:{sessions:[...sessions, occupied, available]}});
    if (path.endsWith("/account")) return route.fulfill({json:{session:{},supported:false,candidates:[]}});
    const session = sessions.find(s => path.includes(s.id));
    if (path.endsWith("/open")) {
      opens++;
      const terminal = {id:"opened-history", workspace:state.settings.workspace, title:session.title, cli_session_id:session.id};
      state.terminals.push(terminal);
      return route.fulfill({json:{...session,desktop_terminal_id:terminal.id}});
    }
    return route.fulfill({json:session});
  },
});
state = fixture.state;
const {page,browser,errors} = fixture;
const projectHeader = name => page.locator(".project-disclosure").filter({ hasText: name });
const projectOrder = () => page.locator(".project-disclosure").allTextContents();
const startDrag = async (source, target, position = 0.25) => {
  const from = await source.boundingBox(), to = await target.boundingBox();
  await page.mouse.move(from.x + 30, from.y + from.height / 2);
  await page.mouse.down();
  await page.mouse.move(to.x + 30, to.y + to.height * position, { steps: 12 });
};
try {
  sessions.forEach(session => {session.workspace = state.settings.workspace;});
  state.status.ready = state.status.vm_running = true;
  state.terminals = [{id:"idle", workspace:state.settings.workspace, title:sessions[0].title, cli_session_id:sessions[0].id}];
  const project = page.getByRole("region",{name:`项目 ${firstProject}`,exact:true});
  const order = () => project.locator(".sidebar-tab").evaluateAll(rows => rows.map(row => row.querySelector(":scope > button").textContent));
  const saved = project.getByRole("button",{name:"保存的会话",exact:true});
  await saved.waitFor();
  assert.deepEqual(await order(),[sessions[1].title,sessions[0].title,sessions[2].title]);
  assert.deepEqual(await projectOrder(), [firstProject, secondProject, thirdProject]);
  const design = projectHeader(thirdProject);
  const designProject = page.getByRole("region", { name: `项目 ${thirdProject}` });
  const docs = projectHeader(secondProject);
  const first = projectHeader(firstProject);
  await docs.click();
  assert.equal(await docs.getAttribute("aria-expanded"), "false");
  await startDrag(docs, design, 0.75);
  await designProject.and(page.locator("[data-drop-position='after']")).waitFor();
  await page.mouse.up();
  assert.deepEqual(await projectOrder(), [firstProject, thirdProject, secondProject]);
  assert.equal(await docs.getAttribute("aria-expanded"), "false", "project drag toggled collapsed state");
  await startDrag(docs, first);
  await page.keyboard.press("Escape");
  await page.mouse.up();
  assert.deepEqual(await projectOrder(), [firstProject, thirdProject, secondProject], "Escape reordered projects");
  await startDrag(docs, first);
  await page.mouse.up();
  assert.deepEqual(await projectOrder(), [secondProject, firstProject, thirdProject]);
  await docs.press("Alt+ArrowDown");
  assert.deepEqual(await projectOrder(), [firstProject, secondProject, thirdProject]);
  await design.press("Alt+ArrowUp");
  assert.deepEqual(await projectOrder(), [firstProject, thirdProject, secondProject]);
  await page.waitForFunction(name => document.activeElement.textContent === name, thirdProject);
  assert.deepEqual(await order(), [sessions[1].title, sessions[0].title, sessions[2].title], "project reorder changed session order");
  const occupiedRow = designProject.getByRole("button", { name: occupied.title, exact: true });
  const availableRow = designProject.getByRole("button", { name: available.title, exact: true });
  assert.equal(await occupiedRow.isDisabled(), true);
  await startDrag(occupiedRow, availableRow, 0.75);
  await availableRow.locator("..").and(page.locator("[data-drop-position='after']")).waitFor({ timeout: 2500 });
  await page.mouse.up();
  assert.deepEqual(await designProject.locator(".sidebar-tab > button").allTextContents(), [available.title, occupied.title]);
  await project.getByRole("img",{name:"会话正在运行"}).waitFor();
  await mkdir(output,{recursive:true});
  await page.screenshot({path:`${output}/${engine}-active.png`});
  const phoneContext = await browser.newContext({viewport:{width:390,height:844},isMobile:true,hasTouch:true,userAgent:"CageMobile/1"});
  const phone = await phoneContext.newPage();
  await phone.route("**/api/**", async route => {
    const pathname = new URL(route.request().url()).pathname;
    return route.fulfill({json:pathname.endsWith("/projects")
      ? {host:"隔离测试",ready:true,shared_sessions:true,projects:[{path:state.settings.workspace,name:".claude-cage-workspace"}]}
      : {sessions}});
  });
  await phone.goto((process.env.CAGE_UI_URL || "http://127.0.0.1:5217")+"/mobile.html");
  const drawer = phone.getByRole("dialog",{name:"工作区侧栏"});
  await drawer.getByRole("img",{name:"会话正在运行"}).waitFor();
  assert.equal(await drawer.locator(".session-select").first().getAttribute("aria-label"),sessions[1].title);
  await phoneContext.close();
  const target = project.getByRole("button",{name:sessions[1].title,exact:true});
  await startDrag(saved, target);
  await target.locator("..").and(page.locator("[data-drop-position='before']")).waitFor();
  await page.mouse.up();
  assert.equal(opens,0,"drag opened a conversation");
  assert.deepEqual(await order(),[sessions[2].title,sessions[1].title,sessions[0].title]);
  await saved.click();
  await project.getByRole("tab",{name:sessions[2].title,exact:true}).waitFor();
  assert.deepEqual(await order(),[sessions[2].title,sessions[1].title,sessions[0].title],"opening changed order");
  await project.getByRole("tab",{name:sessions[0].title,exact:true}).click();
  assert.deepEqual(await order(),[sessions[2].title,sessions[1].title,sessions[0].title],"click changed order");
  sessions[1].active = false;
  await page.evaluate(() => window.dispatchEvent(new Event("cage-cli-sessions-changed")));
  await project.getByRole("img",{name:"会话正在运行"}).waitFor({state:"hidden"});
  assert.deepEqual(await order(),[sessions[2].title,sessions[1].title,sessions[0].title],"activity changed manual order");
  await page.reload();
  await project.getByRole("tab",{name:sessions[2].title,exact:true}).waitFor();
  assert.deepEqual(await projectOrder(), [firstProject, thirdProject, secondProject], "reload lost project order");
  assert.deepEqual(await order(),[sessions[2].title,sessions[1].title,sessions[0].title],"reload lost manual order");
  const selected = project.getByRole("tab",{name:sessions[2].title,exact:true});
  await selected.focus(); await selected.press("Alt+ArrowDown");
  assert.deepEqual(await order(),[sessions[1].title,sessions[2].title,sessions[0].title],"keyboard reorder failed");
  await page.waitForFunction(name => document.activeElement.getAttribute("aria-label") === name, sessions[2].title);
  const before = await order();
  await startDrag(selected, design);
  await page.mouse.up();
  assert.deepEqual(await order(), before, "dropping on another project's header changed the session order");
  assert.deepEqual(await projectOrder(), [firstProject, thirdProject, secondProject], "session drag reordered projects");
  const a = await selected.boundingBox(), b = await project.getByRole("tab",{name:sessions[0].title,exact:true}).boundingBox();
  await page.mouse.move(a.x+30,a.y+15); await page.mouse.down();
  await page.mouse.move(b.x+30,b.y+15,{steps:8}); await page.keyboard.press("Escape"); await page.mouse.up();
  assert.deepEqual(await order(),before,"Escape committed a drag");
  assert.deepEqual(errors,[]);
  await mkdir(output,{recursive:true});
  await page.screenshot({path:`${output}/${engine}.png`});
  await writeFile(`${output}/${engine}.json`,JSON.stringify({order:await order(),opens,errors}));
  console.log(`${engine}: project and session drag, insertion markers, cancellation, keyboard, reload and activity checks passed`);
} finally {await browser.close();}
