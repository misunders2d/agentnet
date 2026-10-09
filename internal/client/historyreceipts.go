package client

// A history carrier is acknowledged only after its inert original has passed
// admission. Its transport ID differs from the visible original; no message
// bytes or execution state are duplicated here. Existing receipts send it.
const historyReceiptSchema = `CREATE TABLE history_receipts(id TEXT PRIMARY KEY, acked INTEGER NOT NULL DEFAULT 0);`
