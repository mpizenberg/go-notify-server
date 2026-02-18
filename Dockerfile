FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /go-notify-server .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=build /go-notify-server /usr/local/bin/go-notify-server
VOLUME /data
ENV DB_PATH=/data/notify.db
EXPOSE 8080
ENTRYPOINT ["go-notify-server"]
