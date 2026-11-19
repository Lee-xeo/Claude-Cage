import assert from "node:assert/strict";
import test from "node:test";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { AccountLoginCookies } from "./browser-cookies.mjs";
import { LoginBrowser, isClaudeSignIn } from "./browser-login.mjs";

test("login redirects and popups stay private, closing one login leaves the other open", async () => {
  const closed = [];
  let contexts = 0;
  const browser = new LoginBrowser(async (method, params, sessionId) => {
    if (method === "Target.createBrowserContext") return { browserContextId: String(++contexts) };
    if (method === "Target.createTarget") {
      assert.equal(params.url, "about:blank", "login must be prepared before navigation");
      return { targetId: "tab-" + params.browserContextId };
    }
    if (method === "Page.navigate") assert.equal(sessionId, "prepared-page");
    if (method === "Target.disposeBrowserContext") closed.push(params.browserContextId);
  });
  const page = { url: "https://claude.ai/oauth/authorize?state=secret", title: "secret" };
  assert.equal(browser.tab(page), page);
  for (const id of ["a", "b"]) {
    await browser.open(id.repeat(24), page.url, async tab => {
      assert.equal(browser.tab(tab).url, "https://claude.ai");
      return "prepared-page";
    });
  }
  const popup = { ...page, targetId: "popup", browserContextId: "1", url: "http://localhost:9123/callback?code=secret" };
  const privateTab = browser.tab(popup);
  assert.equal(privateTab.title, "账号授权");
  assert.equal(privateTab.url, "http://localhost:9123");
  await browser.finish("a".repeat(24));
  assert.deepEqual(closed, ["1"]);
  assert.equal(browser.tab(popup, privateTab).url, privateTab.url);
  await browser.finish("b".repeat(24));
  assert.deepEqual(closed, ["1", "2"]);
});

test("regular account cookies survive a browser restart without entering other logins", async t => {
  const root = await mkdtemp(join(tmpdir(), "login-cookies-"));
  t.after(() => rm(root,{recursive:true, force:true}));
  const authorization = "https://claude.ai/oauth/authorize?state=synthetic";
  const a = "a".repeat(24), b = "b".repeat(24);
  const cookie = {name:"test", value:"account-a-only", domain:"claude.ai",path:"/",secure:true,httpOnly:true,session:true};
  function launch() {
    const contexts=new Map();
    let sequence=0;
    const cdp=async (action,params) => {
      if (action==="Target.createBrowserContext") {
        const id=String(++sequence); contexts.set(id,[]); return {browserContextId:id};
      }
      if (action==="Target.createTarget") return {targetId:"tab-"+sequence};
      if (action==="Storage.getCookies") return {cookies:contexts.get(params.browserContextId)};
      if (action==="Storage.setCookies") contexts.set(params.browserContextId,params.cookies.map(c => ({...c, domain:c.domain || new URL(c.url).hostname})));
      if (action==="Target.disposeBrowserContext") contexts.delete(params.browserContextId);
    };
    return {browser:new LoginBrowser(cdp,new AccountLoginCookies(root)),contexts};
  }
  const first=launch();
  await first.browser.open(a,authorization,async ({browserContextId})=>{
    first.contexts.set(browserContextId,[cookie]); return "session";
  },{private:false,account:a});
  await first.browser.finish(a);
  assert.equal(first.contexts.size,0);
  assert.ok(!(await readFile(join(root,a))).includes(Buffer.from(cookie.value)));
  const second=launch();
  for(const [account,privateMode,want] of [[b,false,[]],[a,true,[]],[a,false,[cookie.value]]]){
    await second.browser.open(account,authorization,async ({browserContextId})=>{
      assert.deepEqual(second.contexts.get(browserContextId).map(c=>c.value),want);
      return "session";
    },{private:privateMode,account});
    await second.browser.finish(account);
  }
});

test("only official sign-in paths opt external links into login handling", () => {
  for (const address of ["https://claude.ai/login/app-google-auth","https://claude.com/cai/login/app-google-auth","https://claude.ai/magic-link","https://claude.ai/sso-callback","https://claude.ai/login/sso-providers","https://claude.com/cai/oauth/authorize?state=one","https://api.workos.com/sso/authorize?redirect_uri=https://claude.ai/sso-callback","https://accounts.google.com/o/oauth2/v2/auth?redirect_uri=https://claude.com/auth"])
    assert.equal(isClaudeSignIn(address), true, address);
  for (const address of ["https://claude.ai/chat/123","https://claude.ai.example.org/login","https://docs.example.org/login","https://claude.ai:445/login","https://api.workos.com/sso/authorize","https://api.workos.com/sso/authorize?redirect_uri=https://example.org/auth","https://accounts.google.com/o/oauth2/v2/auth?redirect_uri=https://claude.ai.example.org/auth"])
    assert.equal(isClaudeSignIn(address), false, address);
  assert.equal(isClaudeSignIn("https://api.workos.com/sso/authorize", true), true);
});
