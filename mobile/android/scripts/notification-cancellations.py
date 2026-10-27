#!/usr/bin/env python3
"""Keep only numeric cancellation evidence from the owned emulator's event log.

Schema: AOSP services/core/java/com/android/server/EventLogTags.logtags,
notification_cancel / notification_cancel_all / notification_canceled.
Never emit notification keys, routes, contents, or listener identities.
"""
import json
import re
import sys

package = "io.github.misunders2d.agentnet.preview"
for line in sys.stdin:
    match = re.search(r"\b(notification_cancel_all|notification_canceled|notification_cancel)\s*:\s*\[(.*)\]\s*$", line)
    if not match:
        continue
    event, payload = match.groups()
    fields = payload.split(",")
    if event == "notification_canceled":
        key = fields[0].split("|")
        if len(key) < 3 or key[1] != package:
            continue
        reason_at, id_text = 1, key[2]
    else:
        if len(fields) < 3 or fields[2] != package:
            continue
        reason_at = 6 if event == "notification_cancel_all" else 8
        id_text = fields[3] if event == "notification_cancel" and len(fields) > 3 else None
    if len(fields) <= reason_at or not re.fullmatch(r"\d+", fields[reason_at]):
        continue
    stamp = re.match(r"\s*(\d\d-\d\d \d\d:\d\d:\d\d\.\d+)\s", line)
    record = {"event": event, "reason": int(fields[reason_at])}
    if stamp:
        record["at"] = stamp[1]
    if id_text is not None and re.fullmatch(r"-?\d+", id_text):
        record["id"] = int(id_text)
    print(json.dumps(record, separators=(",", ":")))
