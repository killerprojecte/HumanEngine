# syntax=docker/dockerfile:1
FROM node:24.22.1-bookworm-slim AS build
WORKDIR /app
# Native dependencies use prebuilds when available and can compile on this ABI otherwise.
RUN apt-get update && apt-get install -y --no-install-recommends python3 make g++ \
    && rm -rf /var/lib/apt/lists/*
COPY package.json package-lock.json ./
# Root postinstall needs the Nuxt configuration, so install after copying safe sources.
COPY . .
RUN npm ci && npm run check && npm run build
# Supplement tracing only for native runtime packages and dynamically selected binaries.
# No full project dependency tree: Nuxt development/server tooling stays in the build stage.
RUN node --input-type=module -e "import {cpSync} from 'node:fs'; for(const p of ['better-sqlite3','sqlite-vec','sqlite-vec-linux-'+process.arch,'sharp','@img','detect-libc','semver']) cpSync('node_modules/'+p,'.output/server/node_modules/'+p,{recursive:true})"

FROM node:24.22.1-bookworm-slim AS runtime
ENV NODE_ENV=production HOST=0.0.0.0 PORT=3000 \
    HUMANENGINE_DB=/app/data/humanengine.sqlite HUMANENGINE_LOG_DIR=/app/data/logs \
    HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY=/app/authority
WORKDIR /app
COPY --from=build --chown=node:node /app/.output ./.output
RUN mkdir -p /app/data/media /app/data/logs /app/plugins /app/authority \
    && chown -R node:node /app/data /app/plugins /app/authority
USER node
EXPOSE 3000
STOPSIGNAL SIGTERM
HEALTHCHECK --interval=15s --timeout=5s --start-period=45s --retries=4 \
  CMD node --input-type=module -e "const token=process.env.HUMANENGINE_ADMIN_TOKEN;if(!token)process.exit(1);try{const r=await fetch('http://127.0.0.1:3000/api/status',{headers:{Authorization:'Bearer '+token},signal:AbortSignal.timeout(4000)});if(!r.ok)process.exit(1)}catch{process.exit(1)}"
CMD ["node", ".output/server/index.mjs"]
