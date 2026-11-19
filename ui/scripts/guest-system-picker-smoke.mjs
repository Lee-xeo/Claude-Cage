import assert from "node:assert/strict";
import { createWorkbench } from "./workbench-fixture.mjs";

// A new environment always installs Debian 13, on every host, without a
// system choice. An existing environment starts with no system at all.
for (const platform of ["darwin", "windows", "linux"]) {
  for (const fresh of [true, false]) {
    const fixture = await createWorkbench({
      clientContext: {mode:"local", connection_id:"local", client_platform:platform, server_platform:platform},
      async beforeNavigate(page, state) {
        state.status.local_vm_storage = true;
        await page.route("**/api/environment/guest-systems", route => route.fulfill({json:{fresh, available:false}}));
      },
    });
    const { browser, page, errors } = fixture;
    try {
      const start = page.getByRole("button",{name:"启动 Cage 环境",exact:true});
      await start.waitFor();
      assert.equal(await page.locator(".guest-system-picker").count(), 0);
      assert.equal(await page.getByRole("combobox",{name:"客户机系统"}).count(), 0);
      const submitted = page.waitForRequest(req => req.url().endsWith("/api/start") && req.method()==="POST");
      await start.click();
      assert.deepEqual((await submitted).postDataJSON(), fresh ? {guest_system:"debian-13"} : {});
      assert.deepEqual(errors,[]);
    } finally { await browser.close(); }
  }
}

// Installations recorded by earlier versions: an Ubuntu one is explained and
// can only be withdrawn; a Debian one can continue.
for (const [system, name, available, reason] of [
  ["ubuntu-22.04", "Ubuntu 22.04", false, "Cage 已不再支持 Ubuntu 客机，现在只提供 Debian 环境；原有客机数据仍保留在数据目录中，未被改动。"],
  ["debian-12", "Debian 12", true, undefined],
]) {
  let pending = { id: "11111111-1111-4111-8111-111111111111", system }, withdrawn = 0;
  const fixture = await createWorkbench({
    clientContext: {mode:"local", connection_id:"local", client_platform:"linux", server_platform:"linux"},
    async beforeNavigate(page, state) {
      state.status.local_vm_storage = true;
      await page.route("**/api/environment/guest-systems", route => {
        if (route.request().method() === "DELETE") {
          assert.deepEqual(route.request().postDataJSON(), { id: pending.id });
          withdrawn++; pending = undefined;
          return route.fulfill({json:{cleared:true}});
        }
        return route.fulfill({json: pending ? {fresh:true, available, reason, reselect:true, pending} : {fresh:true, available:false}});
      });
    },
  });
  const { browser, page, errors } = fixture;
  try {
    const region = page.locator(".guest-system-picker");
    await region.getByText(name, {exact:true}).waitFor();
    assert.equal(await region.getByRole("combobox").count(), 0);
    if (available) {
      const submitted = page.waitForRequest(req => req.url().endsWith("/api/start") && req.method()==="POST");
      await page.getByRole("button",{name:"继续安装",exact:true}).click();
      assert.deepEqual((await submitted).postDataJSON(), {guest_system:system});
    } else {
      await region.getByText(reason, {exact:true}).waitFor();
      assert.ok(await page.getByRole("button",{name:"继续安装",exact:true}).isDisabled(), "an abandoned installation cannot be continued");
      await region.getByRole("button",{name:"撤销原安装选择，改用 Debian 13",exact:true}).click();
      await region.waitFor({state:"hidden"});
      assert.equal(withdrawn, 1);
      const submitted = page.waitForRequest(req => req.url().endsWith("/api/start") && req.method()==="POST");
      await page.getByRole("button",{name:"启动 Cage 环境",exact:true}).click();
      assert.deepEqual((await submitted).postDataJSON(), {guest_system:"debian-13"});
    }
    assert.deepEqual(errors,[]);
  } finally { await browser.close(); }
}
console.log("Debian 13 is the only new environment; recorded installations are continued or withdrawn.");
