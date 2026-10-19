# Website

The landing page is a prerendered [SvelteKit](https://svelte.dev/docs/kit) app in `website/`. It uses the same toolchain as [orch](https://github.com/TorstenDittmann/orch): Bun, Vite, `@sveltejs/adapter-static`, the Geist fonts, and Shiki. The page explains what sbx is, the quickstart, which isolation backends exist today versus later, and the API and TypeScript SDK.

From `website/`:

```bash
bun install --frozen-lockfile
bun run check
bun run dev
```

`./build.sh` type-checks the site and prerenders it into `dist`. Generated output is ignored and must not be edited.

## Deploying the website with Dokploy

Connect this repository in Dokploy yourself. The site image has no environment variables and no credentials.

| Setting | Value |
|---|---|
| Build path | `/website` |
| Dockerfile | `Dockerfile` (this is `website/Dockerfile` from the repository root) |
| Docker context | `.` |
| Container port | `80` |
| Environment | none |

The context is the `website/` directory, not the repository root. `website/Dockerfile` is a multi-stage build. The first stage uses Bun to type-check and prerender the site. The final stage is `nginx:1.27-alpine`, which serves `dist` on port 80. `GET /healthz` returns `200` and the body `ok`.

Dokploy can also use `website/docker-compose.yml`. That file publishes container port 80 on host port 8088 so you can try the image locally:

```bash
docker compose -f website/docker-compose.yml up --build
curl -fsS http://127.0.0.1:8088/healthz
```

Point the Dokploy domain at the container port (`80`), not at 8088. 8088 is only the local compose mapping.

To test the image from the repository root:

```bash
docker build -t sbx-website ./website
docker run --rm -p 127.0.0.1:8088:80 sbx-website
```
