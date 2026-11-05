// Synthetic transport only. It deliberately delivers late/out-of-order answers;
// the real TeamPeople component must reject stale work, not this fixture.
function teamSnapshotFixture(config) {
  const copy=value=>structuredClone(value);
  const requireValue=(ok,message)=>{if(!ok)throw Error('C1 snapshot fixture: '+message);};
  requireValue(config && ['held','failing','normal'].includes(config.mode),'explicit backend mode required');
  requireValue(Array.isArray(config.teams)&&config.teams.length>0,'teams metadata missing');
  requireValue(new Set(config.teams.map(t=>t.id)).size===config.teams.length,'duplicate list ID');
  for(const t of config.teams)requireValue(typeof t.id==='string'&&t.id&&typeof t.name==='string'&&t.name,'list identity missing');
  requireValue(config.snapshot && Array.isArray(config.snapshot.persons),'snapshot persons missing');
  requireValue(Number.isSafeInteger(config.snapshot.at)&&config.snapshot.at>0,'snapshot timestamp missing');
  for(const p of config.snapshot.persons)requireValue(typeof p.id==='string'&&p.id,'snapshot person identity missing');
  const records=copy(config.teams),answer=copy(config.snapshot),mode=config.mode;
  const requests=[],pending=new Map();let teamReads=0,settlements=0;
  function settle(seq,kind,value) {
    const item=pending.get(seq);requireValue(item,'unknown or completed request');
    requireValue(kind==='resolve'||kind==='reject','unknown settlement');
    pending.delete(seq);item.record.state=kind==='resolve'?'resolved':'rejected';
    item.record.settled_ordinal=++settlements;
    if(kind==='resolve')item.resolve(copy(value===undefined?answer:value));
    else item.reject(Error(value===undefined?'Synthetic snapshot refusal':String(value)));
  }
  return {
    teams(){teamReads++;return Promise.resolve({tags:true,legacy_add:false,status:'available',current:true,at:'synthetic',truncated:false,teams:copy(records)});},
    snapshot(ids) {
      requireValue(Array.isArray(ids)&&ids.length===1&&records.some(t=>t.id===ids[0]),'exact known list required');
      const record={seq:requests.length+1,teams:copy(ids),state:'pending'};requests.push(record);
      const promise=new Promise((resolve,reject)=>pending.set(record.seq,{record,resolve,reject}));
      // A real Promise is held/rejected/resolved. Counters alone are not a backend.
      if(mode!=='held')queueMicrotask(()=>settle(record.seq,mode==='failing'?'reject':'resolve'));
      return promise;
    },
    settle,
    witness(){return{mode,team_reads:teamReads,requests:copy(requests),pending:pending.size,failed:requests.filter(r=>r.state==='rejected').length,resolved:requests.filter(r=>r.state==='resolved').length};},
  };
}

function snapshotConfig(mode='held') {
  const base={version:2,realm_id:'synthetic',seq:1,hash:'synthetic',managers:['self'],members:['person-1','person-2'],agents:[],archived:false,member:true,manager:true,conflict:false,listed:true};
  return {mode,teams:[{...base,id:'list-a',name:'List A'},{...base,id:'list-b',name:'List B'},
    {...base,id:'archived',name:'Excluded archived',archived:true},
    {...base,id:'conflict',name:'Excluded conflict',conflict:true},
    {...base,id:'unlisted',name:'Excluded unlisted',listed:false}],
    snapshot:{realm_id:'synthetic',sources:[{id:'list-a',seq:1,hash:'synthetic'}],persons:[{id:'person-1'},{id:'person-2'},{id:'self'}],at:1700000000}};
}

module.exports={teamSnapshotFixture,snapshotConfig};
