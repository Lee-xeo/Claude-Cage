import test from "node:test";
import assert from "node:assert/strict";
import { heatLevel, metricValue, summarizeUsage, usageBreakdown, usageSeries, yearCalendar } from "./token-usage.mjs";

test("all four token categories are counted once, with model and source filters",()=>{
  const rows=[{date:"2026-01-01",model:"a",source:"default",input_tokens:10,output_tokens:20,cache_read_input_tokens:30,cache_creation_input_tokens:40,requests:1},
    {date:"2026-01-01",model:"b",source:"managed",input_tokens:2,output_tokens:3,requests:1}];
  assert.equal(metricValue(summarizeUsage(rows).get("2026-01-01"),"total"),105);
  assert.equal(metricValue(summarizeUsage(rows,{model:"a"}).get("2026-01-01"),"total"),100);
  assert.equal(summarizeUsage(rows,{source:"managed"}).get("2026-01-01").requests,1);
  assert.equal(metricValue(undefined,"total"),null);
});
test("calendar uses Monday columns, handles leap years and never shifts dates with viewer timezone",()=>{
  const calendar=yearCalendar("2024");
  assert.equal(calendar.length,366);
  assert.deepEqual(calendar[0],{date:"2024-01-01",column:0,row:0});
  assert.ok(calendar.find(day=>day.date==="2024-02-29"));
  assert.equal(yearCalendar("2026")[0].row,3);
  assert.equal(yearCalendar("2026").at(-1).date,"2026-12-31");
});
test("daily gaps stay unknown, weekly sums do not overlap, cumulative sums only recorded tokens",()=>{
  const calendar=yearCalendar("2026");
  const days=summarizeUsage([{date:"2026-01-01",input_tokens:10},{date:"2026-01-03",input_tokens:30},{date:"2026-01-05",input_tokens:5}]);
  assert.deepEqual(usageSeries(calendar,days,"total","daily","2026-01-05").map(day=>day.value),[10,null,30,null,5]);
  assert.deepEqual(usageSeries(calendar,days,"total","weekly","2026-01-05").map(day=>day.value),[40,5]);
  assert.deepEqual(usageSeries(calendar,days,"total","cumulative","2026-01-05").map(day=>day.value),[10,10,40,40,45]);
  assert.equal(heatLevel(null,100),-1);
  assert.equal(heatLevel(0,100),0);
});
test("entrypoint totals align with the selected year, model, source and token metric; old snapshots remain unknown",()=>{
  const rows=[{date:"2026-01-01",model:"a",source:"default",client:"terminal",input_tokens:10,output_tokens:20,requests:2},
    {date:"2026-01-02",model:"a",source:"default",client:"desktop_code",input_tokens:30,output_tokens:40,requests:3},
    {date:"2026-01-03",model:"b",source:"managed",input_tokens:50,requests:4},
    {date:"2025-01-01",model:"a",source:"default",client:"cowork",input_tokens:999,requests:1}];
  const groups=usageBreakdown(rows,{year:"2026"},"total");
  assert.equal(groups.reduce((sum,item)=>sum+item.value,0),150);
  assert.equal(groups.find(item=>item.client==="unknown").value,50);
  assert.deepEqual(usageBreakdown(rows,{year:"2026",model:"a",source:"default",client:"desktop_code"},"output_tokens"),[{client:"desktop_code",value:40,requests:3}]);
  assert.equal(metricValue(summarizeUsage(rows,{client:"terminal"}).get("2026-01-01"),"total"),30);
});
