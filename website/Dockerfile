# Static site for Dokploy. The image listens on port 80 and needs no environment.
# Build context is this directory (Dokploy build path /website, context .).
FROM oven/bun:1.4.0 AS build
WORKDIR /site
COPY package.json bun.lock ./
RUN bun install --frozen-lockfile
COPY tsconfig.json vite.config.ts ./
COPY src ./src
COPY static ./static
RUN bun run check && bun run build:site && rm -rf dist/404

FROM nginx:1.27-alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /site/dist /usr/share/nginx/html
EXPOSE 80
