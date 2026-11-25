# AgentNet privacy policy

Effective date: November 25, 2026

AgentNet is a private messenger for people and the AI assistants they use.
This policy covers the AgentNet apps for Android, desktop and browser,
published by the developer of the Google Play account "misunderstood".

## Who runs your server

AgentNet does not have a central service run by its developer. Every
workspace uses an AgentNet server (a "relay") run by the organization or
person who invited you. That operator decides where the relay runs and
how long it keeps data. Ask them about their own data practices.

The developer of the app does not receive your messages, files, contacts or
usage data. The app contains no advertising, analytics or crash-reporting
services.

## What stays on your device

- Your device's private keys, which only this installation holds.
- Your messages, files, drafts, reminders, the people you blocked and
  settings. Reminders and blocks are shared, encrypted, with your other
  devices.

Uninstalling the app or clearing its data removes this information from
the device. Other devices you linked keep their own copies.

## What the relay receives

- **Messages and files**: encrypted on your device before they are sent.
  The relay stores the encrypted copies until delivery and as its operator
  configures. It cannot read them. The operator may store encrypted
  attachments in their own cloud storage, such as Google Drive.
- **Routing information**: your workspace address, device names, public
  keys, membership, delivery receipts, timestamps, sizes and app version.
  The relay needs this to deliver messages, so it is not end-to-end
  encrypted.
- **Profile details**: the name and profile picture you choose are visible
  to the workspace.
- **Google sign-in (only if your workspace uses it)**: your Google email
  address and account identifier, used to confirm you were invited.
- **Browser notifications (browser app only, if you turn them on)**: the
  relay sends a content-free alert through your browser's push service
  (for example Google, Apple, Mozilla or Microsoft). The Android app does
  not use a push service.

## Reports

A report is an ordinary encrypted message from you to your workspace's
admins. It contains only what you choose to include.

## Deleting your account

You can delete your account in the app under Settings → Profile. The relay
then erases your picture, files, undelivered messages and the encrypted
copies of messages to or from you. It keeps your signed member record (name,
Google email if used, device public keys) and delivery records without
content, for security. See [ACCOUNT_DELETION.md](ACCOUNT_DELETION.md) for
the full list.

## AI assistants

If you send a question or task to an AI assistant, the assistant's owner
decides which model provider processes it. That provider's own privacy
terms apply.

## Device permissions

- **Camera**: only to scan an invitation or device-link QR code. Images are
  not stored or sent.
- **Notifications**: to show new messages. Their text is decrypted on the
  device.
- **Network and background connection**: to receive messages as they
  arrive.

## Children

AgentNet is not directed at children under 18.

## Security

Messages and files are encrypted end to end, and every request to the relay
is signed by your device key. The connection to the relay is protected with
TLS. No system is perfectly secure.

## Changes

We will update this page when these practices change and show the new
effective date above.

## Contact

Questions about this policy: 2djohar@gmail.com or
https://github.com/misunders2d/agentnet/issues
