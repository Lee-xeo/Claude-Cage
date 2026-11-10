import { test } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { readFile } from 'node:fs/promises';
import { resolve, sep } from 'node:path';
import { pathToFileURL } from 'node:url';

const enabled = process.env.CAGE_BROWSER_TEST_PLAYWRIGHT && process.env.CAGE_BROWSER_TEST_CHROMIUM;
(enabled ? test : test.skip)('browser sends one bounded pixel wheel packet', async t => {
  const { chromium } = await import(pathToFileURL(process.env.CAGE_BROWSER_TEST_PLAYWRIGHT));
  const root = resolve('ui/vendor/novnc');
  const server = http.createServer(async (req,res) => {
    const path = resolve(root, '.' + new URL(req.url,'http://localhost').pathname);
    if (!path.startsWith(root + sep)) {res.end('<html></html>');return;}
    try {res.setHeader('Content-Type','text/javascript');res.end(await readFile(path));} catch {res.writeHead(404);res.end();}
  });
  await new Promise(resolve => server.listen(0,'127.0.0.1',resolve));
  const browser = await chromium.launch({executablePath:process.env.CAGE_BROWSER_TEST_CHROMIUM,headless:true});
  t.after(async()=>{await browser.close();await new Promise(resolve=>server.close(resolve));});
  const page=await browser.newPage();await page.goto(`http://127.0.0.1:${server.address().port}/`);
  const result=await page.evaluate(async()=>{
    const {default:Cursor}=await import('/core/util/cursor.js');
    const {default:RFB}=await import('/core/rfb.js');
    const target=document.createElement('canvas');document.body.append(target);
    const cursor=new Cursor('default');cursor.attach(target);
    const empty=target.style.cursor;
    cursor.change(new Uint8Array([0,0,0,255]),0,0,1,1);
    const shape=target.style.cursor;cursor.clear();const cleared=target.style.cursor;
    const packets=[];let flushed=0;
    const client={_rfbConnectionState:'connected',_viewOnly:false,_cagePixelWheel:true,_canvas:target,
      _display:{absX:x=>x,absY:y=>y},_sock:{sQpushBytes:b=>packets.push([...b]),flush:()=>flushed++}};
    const wheel=(dy)=>RFB.prototype._handleWheel.call(client,{clientX:20,clientY:20,buttons:0,deltaMode:0,deltaX:0,deltaY:dy,stopPropagation(){},preventDefault(){}});
    wheel(720);wheel(-9000);client._viewOnly=true;wheel(100);
    return {empty,shape,cleared,packets,flushed};
  });

  assert.equal(result.packets.length,2);assert.equal(result.flushed,2);
  assert.equal(result.packets[0][0],250);
  assert.equal(new DataView(Uint8Array.from(result.packets[0]).buffer).getInt32(10),720);
  assert.equal(new DataView(Uint8Array.from(result.packets[1]).buffer).getInt32(10),-2048);
});
