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
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /email-soft .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
RUN addgroup -S es && adduser -S -G es es
WORKDIR /app
COPY --from=build /src/email-soft /app/email-soft
USER es
EXPOSE 8080
ENTRYPOINT ["/app/email-soft"]
CMD ["serve"]
