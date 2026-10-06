// Public, self-chosen person pictures. Never use this field for authority.
export const MaxPictureBytes = 65536;
export const MaxPicturePixels = 256;
export function validatePicture(b) {
  if (!(b instanceof Uint8Array) || b.length < 33 || b.length > MaxPictureBytes || ![137,80,78,71,13,10,26,10].every((n,i)=>b[i]===n)) throw Error("Picture must be a PNG up to 64 KB.");
  const view = new DataView(b.buffer,b.byteOffset,b.byteLength), w=view.getUint32(16), h=view.getUint32(20);
  if (!w || w!==h || w>MaxPicturePixels) throw Error("Picture must be square and at most 256 pixels.");
  let off=8, first=true, pixels=false;
  for (;;) {
    if (off+12>b.length) throw Error("Incomplete PNG.");
    const n=view.getUint32(off), type=String.fromCharCode(...b.subarray(off+4,off+8));
    if (n>b.length-off-12 || (first && (type!=="IHDR" || n!==13))) throw Error("Invalid PNG.");
    if (!["IHDR","PLTE","IDAT","IEND","tRNS","sRGB","gAMA","cHRM","pHYs"].includes(type)) throw Error("Crop the image to remove metadata first.");
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

// A shared editor, mounted only within the skin's own portal/root. Sliders
// provide keyboard/touch crop controls; canvas produces only pixels.
export function openPictureEditor({into, save, src="", current=""}) {
  return new Promise(resolve=>{
    const make=(tag,text)=>{const e=document.createElement(tag);if(text)e.textContent=text;return e;};
    const dialog=make("dialog"); dialog.setAttribute("aria-label","Your profile picture");
    Object.assign(dialog.style,{maxWidth:"min(420px, calc(100vw - 24px))",width:"420px",borderRadius:"16px",padding:"20px",background:"var(--an-surface, Canvas)",color:"var(--an-ink, CanvasText)",border:"2px solid currentColor"});
    const heading=make("h2","Your profile picture"), note=make("p","Your picture is public on your workspace’s server. It does not prove who you are."), error=make("p");error.setAttribute("role","alert");
    const input=make("input");input.type="file";input.accept="image/png,image/jpeg,image/webp,image/gif";input.setAttribute("aria-label","Choose picture");
    const canvas=make("canvas");canvas.width=canvas.height=256;Object.assign(canvas.style,{width:"min(256px, 100%)",height:"auto",display:"block",margin:"12px auto",borderRadius:"8px"});canvas.setAttribute("aria-label","Square picture preview");
    const controls=make("div"), buttons=make("div");Object.assign(buttons.style,{display:"flex",gap:"8px",flexWrap:"wrap",marginTop:"12px"});
    const accept=make("button","Save picture"), cancel=make("button","Cancel");accept.type=cancel.type="button";accept.disabled=true;
    for(const b of [accept,cancel]){b.className="btn";Object.assign(b.style,{padding:"10px 14px",border:"1px solid currentColor",borderRadius:"8px",font:"inherit"});}
    let image=null, owned="", busy=false, closed=false;
    const sliders=[];
    for(const [text,min,max,value] of [["Zoom",1,4,1],["Horizontal crop",0,1,.5],["Vertical crop",0,1,.5]]) {
      const label=make("label",text), range=make("input");range.type="range";range.min=String(min);range.max=String(max);range.step="0.01";range.value=String(value);range.disabled=true;range.setAttribute("aria-label",text);
      Object.assign(label.style,{display:"block",marginTop:"8px"});range.style.width="100%";label.append(range);controls.append(label);sliders.push(range);range.oninput=()=>draw();
    }
    function draw(size=256) {
      canvas.width=canvas.height=size;const ctx=canvas.getContext("2d");if(!image)return;
      const side=Math.min(image.naturalWidth,image.naturalHeight)/Number(sliders[0].value);
      const x=(image.naturalWidth-side)*Number(sliders[1].value),y=(image.naturalHeight-side)*Number(sliders[2].value);
      ctx.drawImage(image,x,y,side,side,0,0,size,size);
    }
    async function load(url) {
      error.textContent="";accept.disabled=true;
      try { const next=new Image();next.src=url;await next.decode();if(closed)return;image=next;sliders.forEach((r,i)=>{r.disabled=false;r.value=i?"0.5":"1";});draw();accept.disabled=false; }
      catch { error.textContent="Could not open that picture. Choose a PNG, JPEG, WebP or GIF."; }
    }
    input.onchange=()=>{const file=input.files?.[0];if(!file)return;if(file.size>20*1024*1024){error.textContent="Choose an image up to 20 MB.";return;}if(owned)URL.revokeObjectURL(owned);owned=URL.createObjectURL(file);load(owned);};
    accept.onclick=async()=>{
      if(busy||!image)return;busy=true;accept.disabled=input.disabled=cancel.disabled=true;sliders.forEach(r=>r.disabled=true);error.textContent="Saving…";
      try {
        let data;
        for(const size of [256,192,128,96,64]) { draw(size);data=canvas.toDataURL("image/png").split(",")[1];if(atob(data).length<=MaxPictureBytes)break; }
        if(atob(data).length>MaxPictureBytes)throw Error("Picture is too detailed. Try a simpler image.");
        await save(data);if(!closed){resolve(true);dialog.close();}
      } catch(e) { if(!closed){error.textContent=e.message||"Picture was not saved.";draw();} }
      finally { busy=false;if(!closed){accept.disabled=input.disabled=cancel.disabled=false;sliders.forEach(r=>r.disabled=false);} }
    };
    cancel.onclick=()=>{if(!busy)dialog.close();};dialog.addEventListener("cancel",e=>{if(busy)e.preventDefault();});
    const opener=document.activeElement;
    dialog.addEventListener("close",()=>{closed=true;image=null;if(owned)URL.revokeObjectURL(owned);dialog.remove();resolve(false);if(opener?.isConnected)opener.focus();},{once:true});
    buttons.append(accept,cancel);dialog.append(heading,note,input,canvas,controls,error,buttons);into.append(dialog);dialog.showModal();input.focus();
    if(src)load(src);else if(current)load(current);
  });
}
