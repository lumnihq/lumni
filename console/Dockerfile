# syntax=docker/dockerfile:1.7
# The Lumni console: the web app people sign in to, and the server behind it
# (accounts, the views every screen reads, the onboarding command). Built from
# the repository root: docker build -f console/Dockerfile .
FROM oven/bun:1-alpine AS build
WORKDIR /app
COPY console/ ./
RUN bun scripts/build.ts \
 && rm -rf test shots data node_modules .env

FROM oven/bun:1-alpine
WORKDIR /app
COPY --from=build /app ./
# Accounts live in /data (SQLite). Mount a volume there and back it up.
RUN mkdir -p /data && chown -R bun:bun /data
ENV HOST=0.0.0.0 \
    PORT=5200 \
    LUMNI_CONSOLE_AUTH=accounts \
    LUMNI_CONSOLE_DB=/data/console.db \
    LUMNI_CONSOLE_DATA_MODE=real
VOLUME ["/data"]
EXPOSE 5200
USER bun
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=6 \
  CMD wget -q -O /dev/null http://127.0.0.1:5200/console/config.json || exit 1
CMD ["bun", "serve.ts", "--prod"]
