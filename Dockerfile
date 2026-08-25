# Multi-stage build for the Heatwave Monitor Next.js app.
# Stage 1 installs deps with Bun (the project's package manager, see bun.lock),
# Stage 2 builds the app, Stage 3 runs only the traced standalone output —
# no node_modules, no source, no Bun — on a plain Node runtime.

FROM oven/bun:1.3-alpine AS deps
WORKDIR /app
COPY package.json bun.lock ./
RUN bun install --frozen-lockfile

FROM oven/bun:1.3-alpine AS builder
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .
RUN bun run build

FROM node:22-alpine AS runner
WORKDIR /app
ENV NODE_ENV=production
ENV PORT=3000
ENV HOSTNAME=0.0.0.0

# Next.js standalone output is a self-contained server — see next.config.ts
# (output: "standalone") and node_modules/next/dist/docs/.../output.md.
COPY --from=builder /app/.next/standalone ./
COPY --from=builder /app/.next/static ./.next/static
COPY --from=builder /app/public ./public

EXPOSE 3000
CMD ["node", "server.js"]
