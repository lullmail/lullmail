FROM node:22-alpine AS dashboard
WORKDIR /src/dashboard
COPY dashboard/package.json dashboard/package-lock.json ./
RUN npm ci
COPY dashboard/ ./
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY mail-engine/go.mod mail-engine/go.sum ./mail-engine/
RUN go mod download
COPY . .
COPY --from=dashboard /src/dashboard/dist ./dashboard/dist
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /lullmail-bin .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates su-exec tzdata
RUN addgroup -S lull && adduser -S -G lull lull
WORKDIR /app
COPY --from=build /lullmail-bin /app/lullmail
COPY --chmod=755 docker-entrypoint.sh /app/docker-entrypoint.sh
EXPOSE 8080
ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["serve"]
