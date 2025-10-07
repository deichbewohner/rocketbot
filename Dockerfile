FROM --platform=$BUILDPLATFORM golang:1.25.1-alpine3.22 AS build
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-w -s" -o /rocketbot .

FROM alpine:3.22 AS userbase
RUN apk --no-cache add ca-certificates && \
    addgroup -S app && adduser -S app -G app

FROM scratch AS final
COPY --from=userbase /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=userbase /etc/passwd /etc/passwd
COPY --from=userbase /etc/group /etc/group
WORKDIR /app
USER app
COPY --from=build /rocketbot ./rocketbot
ENTRYPOINT ["./rocketbot"]
