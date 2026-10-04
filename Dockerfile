# syntax=docker/dockerfile:1
#
# The Next.js frontend: install with Bun (the repo's package manager, see bun.lock),
# build and run on Node. `next build` must run on real Node: in the Bun image the
# `node` command is a shim for Bun itself, which crashes (SIGILL) on CPUs without
# the instructions Bun's release binary assumes. The final image is plain Node
# running the traced standalone server: no node_modules, source or Bun.

FROM oven/bun:1.3-alpine AS deps
WORKDIR /app
COPY package.json bun.lock ./
RUN --mount=type=cache,target=/root/.bun/install/cache bun install --frozen-lockfile

FROM node:22-alpine AS builder
WORKDIR /app
ENV NEXT_TELEMETRY_DISABLED=1
COPY --from=deps /app/node_modules ./node_modules
COPY . .
RUN npm run build

FROM node:22-alpine AS runner
ARG REVISION=unknown
LABEL org.opencontainers.image.title="heatwave-monitor frontend" \
      org.opencontainers.image.source="https://github.com/dharmikchandel/heatwave-monitor" \
      org.opencontainers.image.revision="${REVISION}"
WORKDIR /app
ENV NODE_ENV=production \
    NEXT_TELEMETRY_DISABLED=1 \
    PORT=3000 \
    HOSTNAME=0.0.0.0

# next.config.ts sets output: "standalone", which emits a self-contained server.
COPY --from=builder --chown=node:node /app/.next/standalone ./
COPY --from=builder --chown=node:node /app/.next/static ./.next/static
COPY --from=builder --chown=node:node /app/public ./public

USER node
EXPOSE 3000
HEALTHCHECK --interval=10s --timeout=3s --start-period=15s --retries=3 \
    CMD wget -q -O /dev/null "http://127.0.0.1:${PORT}/" || exit 1
CMD ["node", "server.js"]
