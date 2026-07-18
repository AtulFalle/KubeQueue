FROM node:24.18.0-alpine AS dependencies
WORKDIR /workspace
RUN corepack enable
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY apps/web/package.json apps/web/package.json
COPY packages/api-client/package.json packages/api-client/package.json
RUN pnpm install --frozen-lockfile

FROM dependencies AS build
COPY nx.json tsconfig.base.json eslint.config.mjs ./
COPY apps/web apps/web
COPY packages/api-client packages/api-client
RUN pnpm --filter @kubequeue/api-client build
RUN pnpm --filter @kubequeue/web build

FROM node:24.18.0-alpine AS runtime
ENV NODE_ENV=production
WORKDIR /app
RUN addgroup --system --gid 1001 nodejs \
  && adduser --system --uid 1001 nextjs
COPY --from=build --chown=nextjs:nodejs /workspace/apps/web/.next/standalone ./
COPY --from=build --chown=nextjs:nodejs /workspace/apps/web/.next/static ./apps/web/.next/static
USER 1001:1001
EXPOSE 3000
ENV PORT=3000
CMD ["sh", "-c", "HOSTNAME=0.0.0.0 exec node apps/web/server.js"]
