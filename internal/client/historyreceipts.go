package client

// A history carrier is acknowledged only after its inert original has passed
// admission. Its transport ID differs from the visible original; no message
// bytes or execution state are duplicated here. Existing receipts send it.
const historyReceiptSchema = `CREATE TABLE history_receipts(id TEXT PRIMARY KEY, acked INTEGER NOT NULL DEFAULT 0);`

// A receipt response acknowledges only the local request it observed. A
// duplicate arriving while that HTTP request is in flight asks again, even
// when acked was already zero. Triggers cover every existing reset/upsert.
const receiptGenerationSchema = `
ALTER TABLE inbox ADD COLUMN receipt_gen INTEGER NOT NULL DEFAULT 0;
ALTER TABLE quarantine ADD COLUMN receipt_gen INTEGER NOT NULL DEFAULT 0;
ALTER TABLE history_receipts ADD COLUMN receipt_gen INTEGER NOT NULL DEFAULT 0;
CREATE TRIGGER inbox_receipt_reset AFTER UPDATE OF acked ON inbox WHEN NEW.acked=0
BEGIN UPDATE inbox SET receipt_gen=receipt_gen+1 WHERE id=NEW.id; END;
CREATE TRIGGER quarantine_receipt_reset AFTER UPDATE OF acked ON quarantine WHEN NEW.acked=0
BEGIN UPDATE quarantine SET receipt_gen=receipt_gen+1 WHERE id=NEW.id; END;
CREATE TRIGGER history_receipt_reset AFTER UPDATE OF acked ON history_receipts WHEN NEW.acked=0
BEGIN UPDATE history_receipts SET receipt_gen=receipt_gen+1 WHERE id=NEW.id; END;
`
