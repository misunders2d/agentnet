import {holdChoices,pagedChoices,placeChoices,LIST_CHANGED} from './choices.mjs';

// One root/host's unread projection only. Exact navigation stays with the entry.
export function unreadReview(root,{api,onChoose}) {
  const doc=root.ownerDocument,make=(tag,text='')=>{const n=doc.createElement(tag);n.textContent=text;return n;};
  const section=make('section'),title=make('h2','Unread'),intro=make('p','Open a message in its chat. Decisions waiting for you stay separate.'),status=make('p'),list=make('ul'),retry=make('button','Try again'),more=make('button','Show more unread messages');
  section.className='unread-review';section.setAttribute('aria-label','Unread');section.hidden=true;
  list.setAttribute('aria-label','Unread messages');status.setAttribute('role','status');
  for(const b of [retry,more]){b.type='button';b.className='an-hit';}
  section.append(title,intro,status,retry,list,more);root.append(section);
  let dead=false,visible=false,overview=null,revision=0,scope='',cards=new Map();
  const label=r=>{
    if(r.kind==='device')return (r.peer||'Agent chat')+(r.topic_title?' · '+r.topic_title:'');
    const chat=overview?.dms?.find(d=>d.id===r.conv);
    return ((chat?.kind==='group'?chat.title:chat?.peer?.label)||'Chat')+' · '+(r.topic?r.topic_title||'Topic':'Main flow');
  };
  const stable=holdChoices(list,{key:r=>r.key,onChange:()=>render()});
  const page=pagedChoices({key:r=>r.key,onChange:()=>render(),load:async(after,limit)=>{
    const p=await api('/api/unread?'+new URLSearchParams({limit:String(limit),...(after?{after}:{})}));
    if(p.seq<(overview?.seq||0))throw Error(LIST_CHANGED);
    return {...p,rows:p.rows||[]};
  }});
  function render(){
    if(dead)return;
    const p=page.state,supported=typeof overview?.unread_total==='number';
    title.textContent='Unread'+(supported?' · '+(p.loading||p.error?overview.unread_total:p.total):'');
    status.textContent=!supported?'Unread messages are not available from this connection.':p.error|| (p.loading?'Loading unread messages…':!p.rows.length?'No unread messages.':'');
    status.setAttribute('role',p.error?'alert':'status');
    status.classList.toggle('sr-only',!!(p.loading&&p.rows.length));
    retry.hidden=!p.error;more.hidden=!p.next;more.disabled=p.loading;list.setAttribute('aria-busy',String(p.loading));
    const choices=stable.update(p.rows),keep=new Set(),nodes=[];
    for(const {row:r,present} of choices){
      let card=cards.get(r.key);
      if(!card){
        const item=make('li'),button=make('button'),where=make('strong'),author=make('span'),preview=make('span'),time=make('time');
        item.setAttribute('data-unread-key',r.key);button.type='button';button.className='an-hit an-row-button';
        button.append(where,author,preview,time);item.append(button);card={item,button,where,author,preview,time};cards.set(r.key,card);
        button.onclick=()=>{if(!dead&&visible&&cards.get(card.row.key)===card&&card.present){const chosen=card.row;stable.release();void onChoose(chosen);}};
      }
      card.row=r;card.present=present;card.where.textContent=label(r);card.author.textContent=r.dir==='out'?'You':r.label||r.author;
      card.preview.textContent=present?r.preview||'Attachment':'No longer unread';card.time.textContent=r.at;card.time.dateTime=r.at;
      card.button.setAttribute('aria-disabled',String(!present));card.button.setAttribute('aria-label',label(r)+', '+(r.label||r.author)+', '+(r.preview||'Attachment')+(present?'. Open message.':'. No longer unread.'));
      keep.add(r.key);nodes.push(card.item);
    }
    for(const key of cards.keys())if(!keep.has(key))cards.delete(key);
    placeChoices(list,nodes);
  }
  const sync=()=>page.update(scope,revision,visible&&typeof overview?.unread_total==='number');
  retry.onclick=()=>page.retry();more.onclick=()=>page.more();
  return {
    update(next){if(dead)return;if(scope!==(next.scope||''))stable.reset();overview=next.overview;revision=next.revision;scope=next.scope||'';sync();render();},
    show(on){if(dead)return;visible=!!on;section.hidden=!visible;if(!visible)stable.reset();sync();},
    stop(){if(dead)return;dead=true;page.stop();stable.stop();cards.clear();section.remove();}
  };
}
