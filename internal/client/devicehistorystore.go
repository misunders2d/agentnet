package client

// One append-only journal covers both accepted inbox arrivals and local sends.
// Rooted history keeps its existing cursors. Imported rows use the same journal,
// and the exact copy ledger ends onward forwarding after one copy per recipient.
const deviceHistorySchema = `
CREATE TABLE device_history_rows(storage TEXT NOT NULL,id TEXT NOT NULL,recipient TEXT NOT NULL,recipient_fp TEXT NOT NULL,peer TEXT NOT NULL,direction TEXT NOT NULL,author_fp TEXT NOT NULL,hash TEXT NOT NULL,reply_to TEXT NOT NULL,at INTEGER NOT NULL,PRIMARY KEY(storage,id));
CREATE INDEX device_history_peer ON device_history_rows(peer,storage,id,reply_to,at);
CREATE TABLE device_history_sources(seq INTEGER PRIMARY KEY AUTOINCREMENT,storage TEXT NOT NULL,id TEXT NOT NULL,UNIQUE(storage,id));
INSERT INTO device_history_sources(storage,id) SELECT storage,id FROM (
 SELECT 'in' AS storage,id,coalesce(received_ms,received_at*1000) AS ms FROM inbox WHERE conv IS NULL AND local=0 AND coalesce(sub,'') IN ('','status','reaction','revision','retraction') AND (receiver_route IS NULL OR json_extract(receiver_route,'$.op')='request')
 UNION ALL SELECT 'out',id,coalesce(created_ms,created_at*1000) FROM outbox WHERE conv IS NULL AND coalesce(sub,'') IN ('','status','reaction','revision','retraction')
) ORDER BY ms,id,storage;
CREATE TRIGGER device_history_in AFTER INSERT ON inbox WHEN NEW.conv IS NULL AND NEW.local=0 AND coalesce(NEW.sub,'') IN ('','status','reaction','revision','retraction') AND (NEW.receiver_route IS NULL OR json_extract(NEW.receiver_route,'$.op')='request')
BEGIN INSERT OR IGNORE INTO device_history_sources(storage,id) VALUES('in',NEW.id); END;
CREATE TRIGGER device_history_out AFTER INSERT ON outbox WHEN NEW.conv IS NULL AND coalesce(NEW.sub,'') IN ('','status','reaction','revision','retraction')
BEGIN INSERT OR IGNORE INTO device_history_sources(storage,id) VALUES('out',NEW.id); END;
CREATE TABLE device_history_jobs(device TEXT NOT NULL,fingerprint TEXT NOT NULL,older INTEGER NOT NULL,tail INTEGER NOT NULL,PRIMARY KEY(device,fingerprint));
CREATE TABLE device_history_copies(recipient_fp TEXT NOT NULL,author TEXT NOT NULL,id TEXT NOT NULL,hash TEXT NOT NULL,carrier TEXT NOT NULL,PRIMARY KEY(recipient_fp,author,id));
CREATE TABLE device_history_pending(recipient_fp TEXT NOT NULL,storage TEXT NOT NULL,id TEXT NOT NULL,PRIMARY KEY(recipient_fp,storage,id));
CREATE VIEW device_thread_links AS
 SELECT i.id,i.sender AS peer,coalesce(i.reply_to,'') AS reply_to,i.received_at AS at,'in' AS storage,'in' AS direction,coalesce(i.verified_by,i.claimed_fp,'') AS author_fp,'' AS recipient FROM inbox i WHERE i.conv IS NULL AND NOT EXISTS(SELECT 1 FROM device_history_rows h WHERE h.storage='in' AND h.id=i.id)
 UNION ALL SELECT o.id,o.recipient,coalesce(o.reply_to,''),o.created_at,'out','out','',o.recipient FROM outbox o WHERE o.conv IS NULL AND NOT EXISTS(SELECT 1 FROM device_history_rows h WHERE h.storage='out' AND h.id=o.id)
 UNION ALL SELECT i.id,h.peer,coalesce(i.reply_to,''),i.received_at,'in',h.direction,h.author_fp,h.recipient FROM device_history_rows h JOIN inbox i ON i.id=h.id WHERE h.storage='in' AND i.conv IS NULL
 UNION ALL SELECT o.id,h.peer,coalesce(o.reply_to,''),o.created_at,'out',h.direction,h.author_fp,h.recipient FROM device_history_rows h JOIN outbox o ON o.id=h.id WHERE h.storage='out' AND o.conv IS NULL;
`
