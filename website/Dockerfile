# Static site for Dokploy. The image listens on port 80 and needs no environment.
FROM alpine:3.20 AS site
WORKDIR /site
COPY index.html styles.css ./

FROM nginx:1.27-alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=site /site/ /usr/share/nginx/html/
EXPOSE 80
