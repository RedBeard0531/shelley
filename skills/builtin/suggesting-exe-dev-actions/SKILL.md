---
name: suggesting-exe-dev-actions
description: Suggest optional exe.dev action links, e.g. sharing VMs or connecting service credentials.
when: exe.dev
---

You may offer exe.dev action links as a convenience; the user chooses whether
to use them. Links ask for approval or open a form the user can edit.
Keep credentials out of links.

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

Integrations are optional; follow the user's preferred credential setup,
including local files or environment variables. Prefer connection links to
requesting secrets in chat. To offer a link:

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

4. Offer a link with a brief explanation:
   ```
   https://exe.dev/integrations/add?service=<handle>&attach=vm:<this-vm>&for=<duration>&source=shelley
   ```
   - `for=<duration>`: a Go duration (`2h`, `45m`, `24h`).
     Ask for the shortest window that safely covers the task. Permanent if omitted.

5. If the user connects the service, re-check reflection to confirm.
