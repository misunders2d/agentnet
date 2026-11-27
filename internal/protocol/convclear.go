package protocol

// CapConvClear marks a device that applies its own person's conversation
// deletions (client convclear.go). A deletion waits for each other own
// device until that device advertises it.
const CapConvClear = "clr1"

// CapDeviceClear marks a device that also applies its own person's device
// chat deletions: a clear with no conversation, naming device-thread turns.
// Older devices refuse such a clear as malformed, so it waits for each other
// own device until that device reads it. It is rem3 (CapReminderSyncV3): the
// capability list is full, and both gate own-device copies only. Never
// implied by rm1.
const CapDeviceClear = CapReminderSyncV3
