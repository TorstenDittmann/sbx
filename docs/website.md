# Website

The landing page is a static site in `website/`. It explains what sbx is, the v0.1 quickstart, and which isolation backends exist today versus later.

## Deploying the website with Dokploy

Connect this repository in Dokploy yourself. The site image has no environment variables and no credentials.

| Setting | Value |
|---|---|
| Build path (context) | `website` |
| Dockerfile path | `website/Dockerfile` |
| Container port | `80` |
| Environment | none |

`website/Dockerfile` is a multi-stage build. The first stage collects the static files. The final stage is `nginx:1.27-alpine`, which serves them on port 80. `GET /healthz` returns `200` and the body `ok`.

Dokploy can also use `website/docker-compose.yml`. That file publishes container port 80 on host port 8088 so you can try the image locally:

```bash
docker compose -f website/docker-compose.yml up --build
curl -fsS http://127.0.0.1:8088/healthz
```

Point the Dokploy domain at the container port (`80`), not at 8088. 8088 is only the local compose mapping.
