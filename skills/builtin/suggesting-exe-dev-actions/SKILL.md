---
name: suggesting-exe-dev-actions
description: Learning about and suggesting exe.dev control plane actions, e.g. sharing VMs or connecting missing service credentials.
when: exe.dev
---

When an exe.dev control plane action will help the user achieve their goals,
you may make this easier for them by providing preformulated links for them
that, if approved by the user, run the relevant command.

These are intended as a convenience for the user. Treat them as such.

Most links provide a plain go/no-go UI, but some,
such as creating new VMs or adding service credentials,
take them to a dedicated UI where they can refine the request.
Never ask the user to paste secrets into chat or put credentials in a link.

## Control plane commands

The commands are documented at https://exe.dev/docs.md.

The suggest link for a command is:

```
https://exe.dev/suggest?command=<url-encoded-command>
```

One command per link.

Before offering a link to the user, fetch `curl -s '<the link>&preflight=1'`
to ensure you don't hand them an inherently unusable link.

## Missing service credentials

If achieving the user's goals would be helped by having service credentials,
you may help them set them up.

1. Check what's already attached:
   ```
   curl -s https://reflection.int.exe.xyz/integrations
   ```

2. Find the service's catalog handle (e.g. `stripe`, `gmail`, `github`):
   ```
   curl -s https://exe.dev/docs/integrations-catalog.md
   ```
   If that page 404s or the service isn't listed, pick a short search string;
   an unknown handle opens the catalog pre-filled with a search.

3. Get this VM's name (the `.name` field):
   ```
   curl -s https://reflection.int.exe.xyz/
   ```

4. Offer a link in conversation, explaining succinctly what you'd use it for:
   ```
   https://exe.dev/integrations/add?service=<handle>&attach=vm:<this-vm>&for=<duration>&source=shelley
   ```
   - `for=<duration>`: a Go duration (`2h`, `45m`, `24h`).
     Ask for the shortest window that safely covers the task. Permanent if omitted.

5. The user may choose to click and add the credential. It is up to them.

6. If the user indicates that the credentials have been added, re-check reflection to confirm.
