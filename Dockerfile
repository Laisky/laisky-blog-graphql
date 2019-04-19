FROM golang:1.12.1-alpine3.9 AS gobuild

# run dependencies
RUN apk update && apk upgrade && \
    apk add --no-cache gcc git build-base ca-certificates curl && \
    update-ca-certificates

ADD . /goapp
WORKDIR /goapp

# static build
RUN go mod download
RUN go build -a --ldflags '-extldflags "-static"' entrypoints/main.go


# copy executable file and certs to a pure container
FROM alpine:3.9
COPY --from=gobuild /etc/ssl/certs /etc/ssl/certs
COPY --from=gobuild /go-fluentd/main go-graphql-srv

CMD ./go-graphql-srv --debug --addr=127.0.0.1:8080 --dbaddr=127.0.0.1:27017
