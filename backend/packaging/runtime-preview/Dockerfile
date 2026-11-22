# syntax=docker/dockerfile:1
# Opt-in local/CI candidate, not the production image or a redistribution release.
# Only linux/amd64 has pinned native image/vector assets at this checkpoint.
# Trixie supplies Python 3.13 for the ICU builder's filtered tar extraction.
# Keep the CGo/ICU build and runtime on the same libc/libstdc++ distribution.
FROM golang:1.27.3-trixie AS go-toolchain
FROM node:24.22.1-trixie-slim AS build
COPY --from=go-toolchain /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH
WORKDIR /src
RUN apt-get update && apt-get install -y --no-install-recommends python3 make g++ ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY . .
RUN test "$(uname -m)" = x86_64 && npm ci
# Generate only the client UI. No TypeScript engine workers, credentials, or
# database are needed at build time, and only the public result is shipped.
RUN HUMANENGINE_BUILD_GO_PREVIEW_UI=1 ./node_modules/.bin/nuxt generate \
    && test -s .output/public/index.html && test -s .output/public/200.html \
    && test -d .output/public/_nuxt && test ! -e .output/public/api \
    && test ! -e .output/server && test ! -e data
RUN make -C backend plugin-layout-check && make -C backend wasm && make -C backend native-icu-build
RUN python3 backend/scripts/prepare-sqlite-vec.py --repository /src
RUN mkdir -p /out && cd backend && . .native-build/icu/env.sh \
    && go build -trimpath -tags sqlite_fts5,humanengine_icu,bundled_wasm,bundled_sqlite_vec \
       -o /out/humanengine ./cmd/humanengine
# Copy the existing provenance/source/notices without claiming its still-open
# redistribution gate has been satisfied. ICU data and V1 workers are embedded.
RUN mkdir -p /out/licenses /out/lib \
    && cp node_modules/@img/sharp-libvips-linux-x64/lib/libvips-cpp.so.8.18.7 /out/lib/ \
    && cp -a backend/internal/imagecodec/distribution /out/licenses/imagecodec \
    && cp backend/native/icu/LICENSE /out/licenses/ICU-LICENSE \
    && cp backend/.native-build/icu/build.json /out/licenses/ICU-build.json \
    && cp backend/internal/vectorasset/LICENSE-MIT /out/licenses/sqlite-vec-LICENSE \
    && cp backend/internal/vectorasset/manifest.json /out/licenses/sqlite-vec-manifest.json \
    && cp package-lock.json /out/licenses/dependency-lock.json \
    && sha256sum /out/humanengine /out/lib/* > /out/artifact-sha256.txt

FROM node:24.22.1-trixie-slim AS runtime
ARG SOURCE_REVISION=unknown
LABEL org.opencontainers.image.revision=$SOURCE_REVISION
RUN apt-get update && apt-get install -y --no-install-recommends libstdc++6 libgomp1 ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /out /opt/humanengine
COPY --from=build /src/.output/public /opt/humanengine/ui
RUN mkdir -p /app/data /app/authority /app/plugins \
    && chown -R node:node /app/data /app/authority /app/plugins
USER node
EXPOSE 3001
STOPSIGNAL SIGTERM
# No Node healthcheck: zero enabled V1 plugins must not spawn a Node process.
ENTRYPOINT ["/opt/humanengine/humanengine"]
CMD ["--runtime-preview", "--database", "/app/data/main.sqlite", "--authority", "/app/authority", "--listen", "0.0.0.0:3001", "--plugin-root", "/app/plugins", "--ui-root", "/opt/humanengine/ui", "--image-library", "/opt/humanengine/lib/libvips-cpp.so.8.18.7"]
