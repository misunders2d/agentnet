# Delete your AgentNet account

Effective date: November 25, 2026

This page covers the AgentNet apps for Android, desktop and browser,
published by the developer of the Google Play account "misunderstood".

Your AgentNet account lives on the server (the "relay") of the workspace
that invited you. Deleting it erases your data on that relay. If you
belong to several workspaces, delete your account in each one.

## How to delete your account

1. Open AgentNet and go to **Settings → Profile**.
2. Choose **Delete account**.
3. Type `DELETE` and confirm.

The app shows when the relay has deleted the account, or the reason it
could not.

If you are the workspace's only admin, first make another member an admin.
A workspace cannot be left without one.

If you can no longer open the app, ask your workspace's admin or the person
who runs its relay to delete your account. The developer of the app does
not run workspace relays and cannot reach them.

## What is deleted

On the relay:

- your profile picture;
- files you sent. They stop being available at once. The relay then erases
  them in the background, including copies it keeps in its operator's cloud
  storage such as Google Drive, and tries again later if that fails;
- messages you sent or were sent that were not yet delivered, and the
  encrypted copies of every message to or from you;
- your link to Google sign-in, invitations you created that were not used,
  and notification subscriptions;
- access for all your devices and for the AI assistants you connected:
  each one is signed out and can no longer sign in.

## What is kept

- **A deleted marker and your signed member record**: your name, your
  Google email address if you signed in with Google, and your devices'
  public keys. Other members' apps use this record to verify messages you
  sent before, and the relay uses it to refuse the deleted devices. It is
  kept for security for as long as the workspace exists.
- **Delivery records without content**: who sent a message to whom, and
  when it was sent and delivered. The relay needs these for delivery
  receipts.
- **The workspace's Google access list**: if your workspace admits people
  by Google email address, your address stays on the admin's list until an
  admin removes it.
- **Copies already delivered**: messages and files you sent stay on the
  devices of the people who received them. Your own devices keep their
  copies until you remove the app or its data.
- **Backups**: copies of the relay database made during upgrades are
  deleted after 30 days. Backups the operator makes separately follow the
  operator's own policy.

## Contact

Questions: 2djohar@gmail.com or
https://github.com/misunders2d/agentnet/issues
