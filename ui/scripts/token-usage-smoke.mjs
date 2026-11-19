import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import { createWorkbench } from "./workbench-fixture.mjs";

const output=process.env.CAGE_TOKEN_USAGE_EVIDENCE;
if(output) await mkdir(output,{recursive:true});
const rows=[];
for(let day=0;day<108;day++) {
  const date=new Date(Date.UTC(2026,7,1+day));
  if(date.getUTCDay()===0)continue;
  rows.push({date:date.toISOString().slice(0,10),model:day%3?"claude-sonnet-4-6":"claude-opus-4-6",source:day%4?"default":"a".repeat(24),
    client:["terminal","desktop_code","terminal","automation","cowork","unknown","ide"][Math.floor(day/2)%7],
    input_tokens:1200+day*60,output_tokens:2300+day*90,cache_read_input_tokens:10000+(day*1703)%30000,cache_creation_input_tokens:3000+day*75,requests:5+day%8});
}
let stored={enabled:true,available:true,cutoff:0,sources:{default:"默认配置（归属未核验）",["a".repeat(24)]:"工作配置"},
  snapshot:{version:1,updated_at:1794787200,timezone:"CST",today:"2026-11-16",pending:false,invalid_records:0,days:rows}};
let fail=false;
const operations=[];
const {browser,page,errors}=await createWorkbench({beforeNavigate:async page=>{
  await page.route("**/api/token-usage",route=>{
    const body=route.request().postDataJSON();
    if(body){
      operations.push(body);
      if(body.action==="refresh"&&fail)return route.fulfill({status:502,contentType:"text/plain; charset=utf-8",body:"暂时无法读取 Claude Code 用量，已保存的统计仍然保留"});
      if(body.action==="configure")stored.enabled=body.enabled;
      if(body.action==="clear"){stored.snapshot.days=[];stored.cutoff=1794787200;}
    }
    return route.fulfill({json:stored});
  });
}});
try {
  assert.equal(await page.getByRole("button",{name:"Token 用量",exact:true}).count(),0);
  await page.getByRole("button",{name:/^账户菜单，/}).click();
  await page.getByRole("menuitem",{name:"设置",exact:true}).click();
  const main=page.getByRole("region",{name:"Claude 本地用量",exact:true});
  await main.getByRole("heading",{name:"2026 年活动"}).waitFor();
  await main.getByRole("button",{name:"刷新统计",exact:true}).waitFor();
  assert.equal(await main.locator("button.token-cell").count(),365);
  await main.getByRole("button",{name:"2026-11-16，尚未到来"}).count().then(count=>assert.equal(count,0));
  const totals=main.locator(".token-totals");
  const totalsOffset=async()=>(await totals.boundingBox()).y-(await main.boundingBox()).y;
  const totalsY=await totalsOffset();
  const model=main.getByRole("combobox",{name:"筛选模型",exact:true});
  await model.click();
  assert.ok(await totalsOffset()>totalsY+30,"inline list must push totals down");
  assert.equal(await main.getByRole("listbox").evaluate(el=>getComputedStyle(el).position),"relative");
  await page.keyboard.press("End");
  await page.keyboard.press("Enter");
  assert.equal(await model.getAttribute("aria-expanded"),"false");
  assert.ok(Math.abs(await totalsOffset()-totalsY)<=1,"closing list restores layout");
  await model.click();
  await main.getByRole("option",{name:"全部模型",exact:true}).click();
  const origin=main.getByRole("combobox",{name:"筛选运行入口",exact:true});
  await main.getByRole("table",{name:"运行入口用量"}).getByRole("button",{name:"只看Claude Desktop · Code",exact:true}).click();
  assert.equal(await origin.textContent(),"Claude Desktop · Code");
  assert.equal(await main.getByRole("table",{name:"运行入口用量"}).locator("tbody tr").count(),1);
  const desktopCount=rows.filter(row=>row.client==="desktop_code").reduce((sum,row)=>sum+row.requests,0);
  assert.equal(await totals.locator("dd").nth(2).textContent(),desktopCount.toLocaleString());
  await origin.click();
  await page.keyboard.press("Home");
  await page.keyboard.press("Enter");
  assert.equal(await origin.textContent(),"全部入口");
  const classificationRows=main.getByRole("table",{name:"运行入口用量"}).locator("tbody tr");
  assert.equal(await classificationRows.count(),6);
  const day=main.getByRole("button",{name:/^2026-11-16，/});
  await day.click();
  await page.keyboard.press("ArrowUp");
  assert.equal(await page.locator(":focus").getAttribute("id"),"token-day-2026-11-15");
  for(const [label,curve] of [["每周","每周全部 Token曲线"],["累计总量","累计全部 Token曲线"],["每天","每日全部 Token曲线"]]){
    await main.getByRole("button",{name:label,exact:true}).click();
    await main.getByRole("group",{name:curve,exact:true}).waitFor();
  }
  if(output) {
    await page.locator(".settings-content").evaluate(el=>el.scrollTop=0);
    await page.screenshot({path:output+"/desktop.png",animations:"disabled"});
  }
  if(output)await main.locator(".token-trend").screenshot({path:output+"/curve.png",animations:"disabled"});
  if(output)await main.locator(".token-breakdown").screenshot({path:output+"/clients.png",animations:"disabled"});
  await page.setViewportSize({width:375,height:812});
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
  assert.ok(await main.locator(".token-curve-scroll").evaluate(el=>el.scrollWidth>el.clientWidth));
  assert.ok(await main.getByRole("table",{name:"运行入口用量"}).evaluate(el=>el.getBoundingClientRect().width<=innerWidth));
  if(output)await page.screenshot({path:output+"/narrow.png",animations:"disabled"});
  if(output)await main.locator(".token-breakdown").screenshot({path:output+"/clients-narrow.png",animations:"disabled"});
  await page.setViewportSize({width:1280,height:850});
  await page.evaluate(()=>document.documentElement.style.zoom="2");
  await page.locator(".settings-content").evaluate(el=>{el.scrollTop=0;});
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
  if(output)await page.screenshot({path:output+"/zoom-200.png",animations:"disabled"});
  await page.evaluate(()=>document.documentElement.style.zoom="");
  await main.locator(".token-settings summary").click();
  const enable=main.getByRole("checkbox",{name:"启用本地统计",exact:true});
  await enable.click();
  await main.getByText("统计已暂停，已保存的计数继续保留。",{exact:true}).waitFor();
  assert.equal(await main.getByRole("button",{name:"刷新统计",exact:true}).isDisabled(),true);
  await page.getByRole("button",{name:"返回工作台",exact:true}).click();
  assert.equal(await page.getByRole("button",{name:"Token 用量",exact:true}).count(),0);
  await page.getByRole("button",{name:/^账户菜单，/}).click();
  await page.getByRole("menuitem",{name:"设置",exact:true}).click();
  await main.getByText("统计已暂停，已保存的计数继续保留。",{exact:true}).waitFor();
  await main.locator(".token-settings summary").click();
  assert.equal(await enable.isChecked(),false);
  await enable.click();
  await main.getByRole("button",{name:"刷新统计",exact:true}).waitFor();
  fail=true;
  await main.getByRole("button",{name:"刷新统计",exact:true}).click();
  await main.getByRole("alert").waitFor();
  assert.ok(await totals.locator("dd").first().textContent()!=="0");
  fail=false;
  await main.getByRole("button",{name:"重试",exact:true}).click();
  await main.getByRole("alert").waitFor({state:"hidden"});
  await main.getByRole("button",{name:"清除统计",exact:true}).click();
  await page.getByRole("dialog").getByRole("button",{name:"取消",exact:true}).click();
  assert.equal(operations.filter(op=>op.action==="clear").length,0);
  await main.getByRole("button",{name:"清除统计",exact:true}).click();
  await page.getByRole("dialog").getByRole("button",{name:"清除统计",exact:true}).click();
  await main.getByText(/^尚无可统计的 Token 记录/).waitFor();
  assert.equal(operations.filter(op=>op.action==="clear").length,1);
  assert.equal(await totals.locator("dd").first().textContent(),"0");
  assert.deepEqual(errors,[]);
  console.log("PASS: heatmap and curves, entrypoint distribution and filtering, matching request totals, model filter, inline keyboard selection, day navigation, 375px/200% layout, pause persistence, retry, clear/cancel and empty state.");
}finally{await browser.close();}
