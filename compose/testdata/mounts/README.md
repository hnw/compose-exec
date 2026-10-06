# Docker Compose mount serialization captures

These JSON files were captured from **Docker Compose 5.0.1** by running
`docker compose run --no-deps --pull never probe true` with the fixtures in
`mount_compat_test.go`. Both Compose and compose-exec send real HTTP
`ContainerCreate` requests to a local fake Engine advertising API 1.53,
Engine 29.2.1, Linux/arm64. The server returns image/volume metadata and
rejects creation after capture; no actual containers, images or volumes
are created. These are client serialization observations, not daemon
acceptance or container runtime tests.

The comparison retains all mount fields, including empty option objects and
fields unknown to our Docker SDK. Only project-directory paths, entry ordering and empty top-level
collections are normalized. Non-mount request fields are excluded.

| Compose setting | HostConfig field |
| --- | --- |
| Bind, default/true `create_host_path`, no propagation/recursive | Binds |
| Bind, false `create_host_path` or propagation/recursive | Mounts |
| SELinux z/Z on a legacy bind | Binds, access followed by SELinux option |
| Named volume, no advanced volume options | Binds |
| Volume with nocopy, subpath or labels | Mounts |
| Anonymous volume | Mounts |
| Service `tmpfs:` (with or without options) | Tmpfs |
| `volumes:` entry with `type: tmpfs` | Mounts |

Top-level volume labels affect volume creation, not the choice of mount API.
Service-volume labels require Mounts.

Run against the stored captures (no Compose CLI needed):

```sh
go test ./compose -run '^TestMountSerialization$' -count=1
```

Compare the actual Compose CLI with compose-exec (no Docker daemon needed):

```sh
COMPOSE_EXEC_MOUNT_LIVE=1 go test ./compose -run '^TestMountSerialization$' -count=1
```

Regenerate the captures after reviewing a Compose version change:

```sh
COMPOSE_EXEC_MOUNT_LIVE=1 COMPOSE_EXEC_UPDATE_MOUNTS=1 \
  go test ./compose -run '^TestMountSerialization$' -count=1
```

Update the version and server metadata documented here when recapturing.
