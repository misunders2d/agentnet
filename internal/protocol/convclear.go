package protocol

// CapConvClear marks a device that applies its own person's conversation
// deletions (client convclear.go). A deletion waits for each other own
// device until that device advertises it.
const CapConvClear = "clr1"
