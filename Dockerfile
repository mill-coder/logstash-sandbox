# Stage 1: Build WASM module
FROM docker.io/library/golang:1.22 AS wasm-builder
WORKDIR /src
COPY go/ ./go/
RUN cd go && GOOS=js GOARCH=wasm go build -ldflags="-s -w" -o /out/parser.wasm .
RUN cp "$(go env GOROOT)/misc/wasm/wasm_exec.js" /out/wasm_exec.js

# Stage 2: Build frontend
FROM docker.io/library/node:22-alpine AS web-builder
WORKDIR /src
COPY web/package.json web/package-lock.json* ./
RUN npm install
COPY web/ ./
COPY --from=wasm-builder /out/parser.wasm public/parser.wasm
COPY --from=wasm-builder /out/wasm_exec.js public/wasm_exec.js
RUN npx vite build --outDir /out

# Stage 3: Serve with nginx
FROM docker.io/library/nginx:alpine
COPY --from=web-builder /out /usr/share/nginx/html
EXPOSE 80
