import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";
import { createWorkbench } from "./workbench-fixture.mjs";

const root = fileURLToPath(new URL("..", import.meta.url));
const vite = await createServer({root, configFile:`${root}/vite.config.js`, server:{host:"127.0.0.1",port:0}});
await vite.listen();
const url = `http://127.0.0.1:${vite.httpServer.address().port}`;
const screenshots = fileURLToPath(new URL("../../output/project-folders/", import.meta.url));
await mkdir(screenshots, {recursive:true});
try {
  for (const platform of ["darwin","windows","linux"]) {
    const a = platform === "windows" ? "C:/Projects/frontend" : "/projects/frontend";
    const b = platform === "windows" ? "C:/Projects/backend" : "/projects/backend";
    const fixture = await createWorkbench({url,
      clientContext:{mode:"local",connection_id:"local",client_platform:platform,server_platform:platform,connection:{state:"disconnected"}},
      beforeNavigate:async (_page,state) => {
        Object.assign(state.status,{ready:true,vm_running:true,projects:[]});
        state.settings.workspace="";
        state.chosenFolder=a;
      },
    });
    const {browser,page,state,errors} = fixture;
    try {
      await page.getByRole("button",{name:"打开项目",exact:true}).click();
      let dialog = page.getByRole("dialog",{name:"创建项目",exact:true});
      await dialog.getByLabel("项目名称",{exact:true}).fill("课程系统");
      await dialog.getByRole("button",{name:"选择文件夹…",exact:true}).click();
      await dialog.getByText(a,{exact:true}).waitFor();
      state.chosenFolder=b;
      await dialog.getByRole("button",{name:"选择文件夹…",exact:true}).click();
      await dialog.getByText(b,{exact:true}).waitFor();
      const folders = dialog.getByRole("list",{name:"已绑定文件夹"});
      assert.equal(await folders.getByRole("listitem").count(),2);
      assert.equal(await folders.getByText("默认",{exact:true}).count(),1);
      await page.screenshot({path:`${screenshots}/${platform}-create.png`});
      state.failJob=true;
      await dialog.getByRole("button",{name:"创建项目",exact:true}).click();
      await dialog.getByRole("alert").waitFor();
      assert.deepEqual(state.status.projects,[]);
      state.failJob=false;
      await dialog.getByRole("button",{name:"创建项目",exact:true}).click();
      await page.getByRole("region",{name:"项目 课程系统",exact:true}).waitFor();
      assert.equal(state.settings.workspace,a);
      assert.deepEqual(state.status.projects,[a,b]);
      await page.reload();
      const project = page.getByRole("region",{name:"项目 课程系统",exact:true});
      await project.waitFor();
      assert.equal(await page.locator(".sidebar-project").count(),1);
      await project.getByRole("button",{name:"打开文件夹 backend 的编辑器",exact:true}).click();
      await page.getByRole("tab",{name:"文件 · backend",exact:true}).waitFor();
      assert.equal(state.tabs.find(tab=>tab.kind==="editor").workspace,b);
      await page.getByRole("button",{name:"关闭 文件 · backend",exact:true}).click();
      await page.getByRole("dialog").getByRole("button",{name:"关闭编辑器",exact:true}).click();
      await project.getByRole("button",{name:"在 课程系统 新建终端",exact:true}).click();
      await page.getByRole("menuitem",{name:"普通终端",exact:true}).click();
      await page.getByRole("tab",{name:"终端 1",exact:true}).waitFor();
      assert.equal(state.terminals[0].workspace,a);
      await page.getByRole("button",{name:"添加项目",exact:true}).click();
      dialog=page.getByRole("dialog",{name:"打开项目",exact:true});
      await dialog.getByRole("button",{name:"编辑名称与文件夹…",exact:true}).click();
      dialog=page.getByRole("dialog",{name:"编辑项目",exact:true});
      assert.equal(await dialog.getByRole("button",{name:"移除文件夹 frontend",exact:true}).isDisabled(),true);
      await dialog.getByRole("button",{name:"移除文件夹 backend",exact:true}).click();
      await dialog.getByRole("button",{name:"保存项目",exact:true}).click();
      await dialog.getByRole("alert").waitFor();
      assert.deepEqual(state.status.projects,[a,b]);
      await dialog.getByRole("button",{name:"取消",exact:true}).click();
      await project.getByRole("tab",{name:"终端 1",exact:true}).hover();
      await project.getByRole("button",{name:"关闭终端",exact:true}).click();
      await page.getByRole("dialog").getByRole("button",{name:"关闭终端",exact:true}).click();
      await page.getByRole("button",{name:"添加项目",exact:true}).click();
      await page.getByRole("dialog").getByRole("button",{name:"编辑名称与文件夹…",exact:true}).click();
      dialog=page.getByRole("dialog",{name:"编辑项目",exact:true});
      await dialog.getByLabel("项目名称",{exact:true}).fill("课程工作区");
      await dialog.getByRole("button",{name:"移除文件夹 backend",exact:true}).click();
      await dialog.getByRole("button",{name:"保存项目",exact:true}).click();
      await page.getByRole("region",{name:"项目 课程工作区",exact:true}).waitFor();
      assert.deepEqual(state.status.projects,[a]);
      await page.reload();
      await page.getByRole("region",{name:"项目 课程工作区",exact:true}).waitFor();
      assert.deepEqual(errors,[]);
      console.log(`PASS ${platform}: create, retry, refresh, member editor, default terminal, active-session unbind guard, rename and unbind`);
    } finally {await browser.close();}
  }
} finally {await vite.close();}
