// Public, self-chosen person pictures. Never use this field for authority.
export const MaxPictureBytes = 65536;
export const MaxPicturePixels = 256;
const pictureChunks = ["IHDR","PLTE","IDAT","IEND","tRNS","sRGB","gAMA","cHRM","pHYs"];
// WebKit canvas adds color-profile chunks. Keep the server's PNG contract,
// removing ancillary metadata without changing pixels or weakening validation.
export function cleanPicturePNG(b) {
  if (!(b instanceof Uint8Array) || ![137,80,78,71,13,10,26,10].every((n,i)=>b[i]===n)) throw Error("Could not read this picture. Choose another photo.");
  const view=new DataView(b.buffer,b.byteOffset,b.byteLength), parts=[b.subarray(0,8)];
  let off=8, first=true, ended=false;
  while(off+12<=b.length) {
    const n=view.getUint32(off), type=String.fromCharCode(...b.subarray(off+4,off+8));
    if(n>b.length-off-12 || (first && (type!=="IHDR" || n!==13))) throw Error("Could not read this picture. Choose another photo.");
    if(pictureChunks.includes(type)) parts.push(b.subarray(off,off+n+12));
    else if(!(b[off+4]&32)) throw Error("Could not read this picture. Choose another photo.");
    off+=n+12;first=false;
    if(type==="IEND") { ended=n===0 && off===b.length;break; }
  }
  if(!ended) throw Error("Could not read this picture. Choose another photo.");
  const out=new Uint8Array(parts.reduce((n,p)=>n+p.length,0));let at=0;
  for(const part of parts){out.set(part,at);at+=part.length;}
  return out;
}

// Native reads happen only in response to a paste, after browser files. Native
// image + text pastes attach the image; text-only pastes keep their usual text.
export function pastePictures(e, add, {clipboardImage:read, insertText=()=>{}, error=()=>{}}={}) {
  const files=[...(e.clipboardData?.files||[])];
  if(files.length){e.preventDefault();add(files);return;}
  if(typeof read!=="function")return;
  const text=e.clipboardData?.getData("text/plain")||"";
  e.preventDefault();
  return Promise.resolve().then(()=>read()).then(file=>{
    if(file)add([file]);else if(text)insertText(text);
  }).catch(()=>{if(text)insertText(text);else error("Could not paste the picture. Choose it with Add files instead.");});
}

export function pictureCrop(width,height,zoom=1,x=width/2,y=height/2) {
  zoom=Math.max(1,Math.min(4,zoom));
  const side=Math.min(width,height)/zoom;
  x=Math.max(side/2,Math.min(width-side/2,x));y=Math.max(side/2,Math.min(height-side/2,y));
  return {zoom,x,y,side};
}
export function validatePicture(b) {
  if (!(b instanceof Uint8Array) || b.length < 33 || b.length > MaxPictureBytes || ![137,80,78,71,13,10,26,10].every((n,i)=>b[i]===n)) throw Error("Picture must be a PNG up to 64 KB.");
  const view = new DataView(b.buffer,b.byteOffset,b.byteLength), w=view.getUint32(16), h=view.getUint32(20);
  if (!w || w!==h || w>MaxPicturePixels) throw Error("Picture must be square and at most 256 pixels.");
  let off=8, first=true, pixels=false;
  for (;;) {
    if (off+12>b.length) throw Error("Incomplete PNG.");
    const n=view.getUint32(off), type=String.fromCharCode(...b.subarray(off+4,off+8));
    if (n>b.length-off-12 || (first && (type!=="IHDR" || n!==13))) throw Error("Invalid PNG.");
    if (!pictureChunks.includes(type)) throw Error("Crop the image to remove metadata first.");
    if(type==="IDAT") pixels=true;
    off+=n+12;first=false;
    if(type==="IEND") { if(n!==0 || off!==b.length || !pixels) throw Error("Invalid PNG.");break; }
  }
}
export function avatarPicture(overview, seed, name="") {
  const people = [overview?.person,...(overview?.people||[])].filter(Boolean);
  const exact = people.find(p=>p.person===seed || p.address===seed || (p.devices||[]).some(d=>d.address===seed));
  if (exact) return exact.picture_url || "";
  // Legacy surfaces hand the avatar a label. Duplicate labels never guess.
  const labels = people.filter(p=>p.label===(name||seed));
  const unique = [...new Map(labels.map(p=>[p.person||p.address,p])).values()];
  return unique.length===1 ? unique[0].picture_url||"" : "";
}

// A shared editor, mounted only within the skin's own portal/root. Pointer,
// touch and keyboard controls all change the same bounded source-pixel crop.
export function openPictureEditor({into, save, src="", current=""}) {
  return new Promise(resolve=>{
    const make=(tag,text)=>{const e=document.createElement(tag);if(text)e.textContent=text;return e;};
    const dialog=make("dialog"); dialog.setAttribute("aria-label","Your profile picture");
    Object.assign(dialog.style,{boxSizing:"border-box",maxWidth:"calc(100vw - 24px)",width:"420px",maxHeight:"calc(100dvh - 24px)",overflowY:"auto",borderRadius:"16px",padding:"24px",background:"var(--an-surface, Canvas)",color:"var(--an-ink, CanvasText)",border:"2px solid currentColor",boxShadow:"5px 5px 0 var(--an-outline, currentColor)",font:"inherit"});
    const heading=make("h2","Your profile picture"), note=make("p","Your picture is public on your workspace’s server. It does not prove who you are."), error=make("p");error.setAttribute("role","alert");
    const input=make("input");input.type="file";input.hidden=true;input.accept="image/png,image/jpeg,image/webp,image/gif";input.setAttribute("aria-label","Choose picture");
    const choose=make("button","Choose photo");choose.type="button";choose.onclick=()=>input.click();
    const canvas=make("canvas");canvas.width=canvas.height=256;canvas.tabIndex=0;Object.assign(canvas.style,{width:"min(256px, 100%)",height:"auto",display:"block",margin:"20px auto 12px",borderRadius:"50%",border:"2px solid currentColor",boxSizing:"border-box",touchAction:"none",cursor:"grab",background:"var(--an-sunken, #eee)"});canvas.setAttribute("aria-label","Picture crop preview");canvas.setAttribute("aria-description","Drag or use arrow keys to move. Scroll, pinch, or use plus and minus to zoom. Home resets.");
    const hint=make("p","Drag to move · scroll or pinch to zoom");Object.assign(hint.style,{textAlign:"center",fontSize:"14px",margin:"8px 0"});
    const controls=make("div"), buttons=make("div");Object.assign(controls.style,{display:"flex",justifyContent:"center",alignItems:"center",gap:"10px"});Object.assign(buttons.style,{display:"flex",gap:"8px",flexWrap:"wrap",marginTop:"20px"});
    const less=make("button","−"),more=make("button","+"),reset=make("button","Reset"),level=make("output","100%");less.setAttribute("aria-label","Zoom out");more.setAttribute("aria-label","Zoom in");level.setAttribute("aria-label","Zoom level");level.style.minWidth="4ch";
    const accept=make("button","Save picture"), cancel=make("button","Cancel");accept.type=cancel.type="button";accept.disabled=true;
    for(const b of [accept,cancel,choose,less,more,reset]){b.type="button";b.className="btn";Object.assign(b.style,{padding:"10px 14px",minHeight:"44px",border:"2px solid currentColor",borderRadius:"8px",font:"inherit",cursor:"pointer"});}
    Object.assign(choose.style,{width:"100%",background:"var(--an-sunken, Canvas)",color:"inherit"});Object.assign(accept.style,{background:"var(--an-act, #ffd43b)",color:"var(--an-act-ink, #1b1530)",fontWeight:"700"});
    let image=null, crop=null, owned="", busy=false, closed=false, loadID=0;
    const pointers=new Map();
    function enabled(){less.disabled=more.disabled=reset.disabled=!image||busy;less.disabled ||= crop?.zoom===1;more.disabled ||= crop?.zoom===4;}
    function change(zoom=crop.zoom,x=crop.x,y=crop.y){crop=pictureCrop(image.naturalWidth,image.naturalHeight,zoom,x,y);draw();enabled();}
    function center(){if(image)change(1,image.naturalWidth/2,image.naturalHeight/2);}
    less.onclick=()=>change(crop.zoom/1.15);more.onclick=()=>change(crop.zoom*1.15);reset.onclick=center;
    controls.append(less,level,more,reset);enabled();
    function draw(size=256) {
      canvas.width=canvas.height=size;const ctx=canvas.getContext("2d");if(!image)return;
      ctx.clearRect(0,0,size,size);ctx.drawImage(image,crop.x-crop.side/2,crop.y-crop.side/2,crop.side,crop.side,0,0,size,size);level.textContent=Math.round(crop.zoom*100)+"%";
    }
    canvas.addEventListener("wheel",e=>{if(!image||busy)return;e.preventDefault();change(crop.zoom*Math.exp(-e.deltaY*.002));},{passive:false});
    canvas.addEventListener("keydown",e=>{if(!image||busy)return;const step=crop.side/20;
      if(e.key==="Home")center();else if(e.key==="+"||e.key==="=")change(crop.zoom*1.15);else if(e.key==="-")change(crop.zoom/1.15);
      else if(e.key==="ArrowLeft")change(crop.zoom,crop.x-step,crop.y);else if(e.key==="ArrowRight")change(crop.zoom,crop.x+step,crop.y);
      else if(e.key==="ArrowUp")change(crop.zoom,crop.x,crop.y-step);else if(e.key==="ArrowDown")change(crop.zoom,crop.x,crop.y+step);else return;e.preventDefault();});
    const point=e=>({x:e.clientX,y:e.clientY}),distance=p=>Math.hypot(p[0].x-p[1].x,p[0].y-p[1].y),mid=p=>({x:p.reduce((s,v)=>s+v.x,0)/p.length,y:p.reduce((s,v)=>s+v.y,0)/p.length});
    canvas.addEventListener("pointerdown",e=>{if(!image||busy||(e.pointerType==="mouse"&&e.button!==0))return;e.preventDefault();pointers.set(e.pointerId,point(e));canvas.setPointerCapture(e.pointerId);canvas.style.cursor="grabbing";});
    canvas.addEventListener("pointermove",e=>{if(!pointers.has(e.pointerId)||!image||busy)return;e.preventDefault();const before=[...pointers.values()];pointers.set(e.pointerId,point(e));const after=[...pointers.values()],a=mid(before),b=mid(after),ratio=crop.side/canvas.getBoundingClientRect().width;
      change(before.length===2&&distance(before)>0?crop.zoom*distance(after)/distance(before):crop.zoom,crop.x-(b.x-a.x)*ratio,crop.y-(b.y-a.y)*ratio);});
    for(const event of ["pointerup","pointercancel","lostpointercapture"])canvas.addEventListener(event,e=>{pointers.delete(e.pointerId);if(!pointers.size)canvas.style.cursor="grab";});
    async function load(url) {
      const id=++loadID;error.textContent="";accept.disabled=true;image=null;enabled();
      try { const next=new Image();next.src=url;await next.decode();if(closed||id!==loadID)return;image=next;pointers.clear();center();accept.disabled=false; }
      catch { if(id===loadID)error.textContent="Could not open that picture. Choose a PNG, JPEG, WebP or GIF."; }
    }
    input.onchange=()=>{const file=input.files?.[0];if(!file)return;if(file.size>20*1024*1024){error.textContent="Choose an image up to 20 MB.";return;}if(owned)URL.revokeObjectURL(owned);owned=URL.createObjectURL(file);load(owned);};
    accept.onclick=async()=>{
      if(busy||!image)return;busy=true;accept.disabled=input.disabled=choose.disabled=cancel.disabled=true;enabled();error.textContent="Saving…";
      try {
        let bytes;
        for(const size of [256,192,128,96,64]) { draw(size);bytes=cleanPicturePNG(Uint8Array.from(atob(canvas.toDataURL("image/png").split(",")[1]),c=>c.charCodeAt(0)));if(bytes.length<=MaxPictureBytes)break; }
        if(bytes.length>MaxPictureBytes)throw Error("Picture is too detailed. Try a simpler image.");
        validatePicture(bytes);let binary="";for(const byte of bytes)binary+=String.fromCharCode(byte);const data=btoa(binary);
        await save(data);if(!closed){resolve(true);dialog.close();}
      } catch(e) { if(!closed){error.textContent=e.message||"Picture was not saved.";draw();} }
      finally { busy=false;if(!closed){accept.disabled=input.disabled=choose.disabled=cancel.disabled=false;enabled();} }
    };
    cancel.onclick=()=>{if(!busy)dialog.close();};dialog.addEventListener("cancel",e=>{if(busy)e.preventDefault();});
    const opener=document.activeElement;
    dialog.addEventListener("close",()=>{closed=true;image=null;if(owned)URL.revokeObjectURL(owned);dialog.remove();resolve(false);if(opener?.isConnected)opener.focus();},{once:true});
    buttons.append(accept,cancel);dialog.append(heading,note,input,choose,canvas,hint,controls,error,buttons);into.append(dialog);dialog.showModal();choose.focus();
    if(src)load(src);else if(current)load(current);
  });
}
