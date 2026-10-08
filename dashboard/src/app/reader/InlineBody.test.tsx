// @vitest-environment jsdom
import { expect, it, vi } from "vitest";
import { render } from "preact";
import { act } from "preact/test-utils";
import { MessageBody } from "./Body";

it("fetches CID bytes with account identity and revokes its blob on unmount", async () => {
 const host=document.createElement("div");document.body.append(host);
 const fetcher=vi.fn(async()=>new Response(new Uint8Array([137,80,78,71])));
 vi.stubGlobal("fetch",fetcher);
 const create=vi.fn(()=>"blob:http://localhost/inline");const revoke=vi.fn();
 const oldCreate=URL.createObjectURL, oldRevoke=URL.revokeObjectURL;
 URL.createObjectURL=create;URL.revokeObjectURL=revoke;
 try {
  await act(async()=>{render(<MessageBody html='<img src="cid:logo">' text="" messageId="message" account="account" sender="sender" inlineParts={[{part_id:"2",content_id:"logo",type:"image/png",size:4}]} />,host);});
  await vi.waitFor(()=>expect(create).toHaveBeenCalledTimes(1));
  expect(fetcher).toHaveBeenCalledWith("/api/messages/message/attachment/2?account=account",expect.objectContaining({credentials:"same-origin"}));
  await act(async()=>{render(null,host)});
  expect(revoke).toHaveBeenCalledWith("blob:http://localhost/inline");
 } finally {render(null,host);host.remove();URL.createObjectURL=oldCreate;URL.revokeObjectURL=oldRevoke;vi.unstubAllGlobals()}
});

import { fetchInlineImages, inlineImageCap, inlineAggregateCap, normalizeCID, referencedInlineParts, replaceInlineImages } from "./Body";
const raster = (id: string, cid = id, size = 1) => ({part_id:id,content_id:cid,type:"image/png",size});

it("selects referenced case-sensitive normalized IDs once and refuses SVG and malformed IDs", () => {
 const parts=[raster("first","<Logo@x>"),raster("duplicate","Logo@x"),raster("unreferenced"),raster("lower","logo@x"),{...raster("svg"),type:"image/svg+xml"},raster("bad","bad%zz")];
 const selected=referencedInlineParts('<img src="cid:%3CLogo%40x%3E"><img src="cid:svg"><img src="cid:bad%zz">',parts);
 expect(selected.map(p=>p.part_id)).toEqual(["first"]);
 expect(normalizeCID(" <Logo@x> ")).toBe("Logo@x");
 expect(normalizeCID("bad%zz")).toBeUndefined();
 expect(normalizeCID("<broken")).toBeUndefined();
 expect(replaceInlineImages('<img src="cid:%3CLogo%40x%3E">',new Map([["Logo@x","blob:fixture"]]))).toContain('src="blob:fixture"');
});

it.each([-1,0,1])("counts streamed bytes despite lying metadata at the image cap %+d", async delta => {
 const cancel=vi.fn(); let read=0;
 vi.stubGlobal("fetch",vi.fn(async()=>({ok:true,body:new ReadableStream({pull(controller){if(read++===0)controller.enqueue(new Uint8Array(inlineImageCap+delta));else controller.close()},cancel},{highWaterMark:0})})));
 const publish=vi.fn();
 try {
  await fetchInlineImages([raster("large", "large", 0)],"message","account",new AbortController().signal,()=>true,publish);
  expect(publish).toHaveBeenCalledTimes(delta>0?0:1);
  if(delta>0)expect(cancel).toHaveBeenCalledTimes(1);
 } finally {vi.unstubAllGlobals()}
});

it.each([-1,0,1])("stops the next request at aggregate exhaustion %+d", async delta => {
 const fetcher=vi.fn(async()=>({ok:true,body:new ReadableStream({start(controller){
  const index=fetcher.mock.calls.length;
  const size=index===1?inlineImageCap:(index===2?inlineAggregateCap-inlineImageCap+delta:1);
  controller.enqueue(new Uint8Array(size));controller.close();
 }})}));
 vi.stubGlobal("fetch",fetcher);const publish=vi.fn();
 try {
  await fetchInlineImages([raster("one"),raster("two"),raster("three")],"message","account",new AbortController().signal,()=>true,publish);
  expect(fetcher).toHaveBeenCalledTimes(delta<0?3:2);
  expect(publish).toHaveBeenCalledTimes(delta>0?1:(delta<0?3:2));
 } finally {vi.unstubAllGlobals()}
});

it("cancels stale response before publication or next fetch on owner change and abort", async () => {
 for(const aborted of [false,true]) {
  let current=true; const abort=new AbortController();const cancel=vi.fn();
  vi.stubGlobal("fetch",vi.fn(async()=>{if(aborted)abort.abort();else current=false;return {ok:true,body:new ReadableStream({cancel})}}));
  const publish=vi.fn();
  try {
   await fetchInlineImages([raster("one"),raster("two")],"message","account",abort.signal,()=>current,publish);
   expect(publish).not.toHaveBeenCalled();expect(fetch).toHaveBeenCalledTimes(1);expect(cancel).toHaveBeenCalledTimes(1);
  } finally {vi.unstubAllGlobals()}
 }
});

it("reclaims the selected duplicate blob on navigation and reclaims every later allocation", async () => {
 const host=document.createElement("div");document.body.append(host);
 const fetcher=vi.fn(async()=>new Response(new Uint8Array([137,80,78,71])));
 vi.stubGlobal("fetch",fetcher);
 const create=vi.fn(()=>"blob:fixture-"+create.mock.calls.length);const revoke=vi.fn();
 const oldCreate=URL.createObjectURL,oldRevoke=URL.revokeObjectURL;URL.createObjectURL=create;URL.revokeObjectURL=revoke;
 try {
  const parts=[raster("one","logo"),raster("duplicate","logo"),raster("unreferenced")];
  await act(async()=>render(<MessageBody html='<img src="cid:logo">' text="" messageId="first" account="account" sender="sender" inlineParts={parts}/>,host));
  await vi.waitFor(()=>expect(create).toHaveBeenCalledTimes(1));expect(fetcher).toHaveBeenCalledTimes(1);
  await act(async()=>render(<MessageBody html='<img src="cid:logo">' text="" messageId="second" account="other-account" sender="sender" inlineParts={parts}/>,host));
  await vi.waitFor(()=>expect(create).toHaveBeenCalledTimes(2));expect(revoke).toHaveBeenCalledWith("blob:fixture-1");
  await act(async()=>render(null,host));expect(revoke).toHaveBeenCalledWith("blob:fixture-2");expect(revoke).toHaveBeenCalledTimes(2);
 } finally {render(null,host);host.remove();URL.createObjectURL=oldCreate;URL.revokeObjectURL=oldRevoke;vi.unstubAllGlobals()}
});
