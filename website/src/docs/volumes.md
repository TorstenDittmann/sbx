# Volumes

A volume is a named Docker volume that outlives the sandboxes that mount it. Use one to keep a project directory, a package cache, or a model download between sandboxes.

## Create and mount

```bash
curl -s $SBX_URL/v1/volumes \
  -H "Authorization: Bearer $SBX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name":"project"}'
```

```json
{ "id": "vol_8b1d2e4f6a0c3b5d", "name": "project", "backend": "docker", "created_at": "2026-10-20T12:00:00Z" }
```

Mount it when you create a sandbox:

```json
{
  "template": "node",
  "mounts": [{ "volume_id": "vol_8b1d2e4f6a0c3b5d", "path": "/workspace" }]
}
```

Anything the sandbox writes under `/workspace` is there again for the next sandbox that mounts the same volume.

## Rules

- A volume name is 1 to 64 characters, starts with a letter or digit, and is unique per API key.
- `backend` is `docker`, `gvisor`, or `kata`. Firecracker does not attach Docker volumes.
- A sandbox can have up to 8 mounts. Paths are absolute and contain no `..`.
- Mount at `/workspace` to keep the project directory. A volume there replaces the `/workspace` tmpfs, so `disk_mb` does not cap it. Paths under `/workspace/` are rejected.
- `/tmp`, `/opt/sbx`, `/proc`, `/sys`, `/dev`, and `/etc` are reserved.
- A sandbox key only sees and mounts its own volumes. An admin key sees all of them.

## Delete

```bash
curl -s -X DELETE $SBX_URL/v1/volumes/$VOLUME_ID -H "Authorization: Bearer $SBX_API_KEY"
```

Deleting a sandbox never deletes its volumes. Deleting a volume fails with `409 conflict` while a creating, running, or paused sandbox mounts it. A stopped sandbox does not hold on to it.

## In the SDK

```ts
const client = new SbxClient()
const volume = await client.createVolume("project", "docker")

const box = await Sandbox.create({
  template: "node",
  mounts: [{ volumeId: volume.id, path: "/workspace" }],
})
```

`client.listVolumes()`, `client.getVolume(id)`, and `client.deleteVolume(id)` cover the rest.
