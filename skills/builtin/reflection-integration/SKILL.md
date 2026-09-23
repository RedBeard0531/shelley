---
name: reflection-integration
description: Use to discover external services and APIs available on this exe.dev VM via network-edge-injected credentials and to discover VM metadata like owner email, VM tags, and default port.
when: exe.dev
---

Integrations attached to this VM:

```
curl https://reflection.int.exe.xyz/integrations
```

VM metadata:

```
curl https://reflection.int.exe.xyz/
```

If the reflection endpoint fails, the VM may be old, or the user may have removed the reflection integration or given it an unusual name.

Integrations CRUD (user only): https://exe.dev/integrations.
