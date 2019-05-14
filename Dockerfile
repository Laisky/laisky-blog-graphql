FROM golang:1.12.1-alpine3.9 AS gobuild

# run dependencies
RUN apk update && apk upgrade && \
    apk add --no-cache gcc git build-base ca-certificates curl && \
    update-ca-certificates

ENV GO111MODULE=on
WORKDIR /goapp

ENV HTTP_PROXY=http://172.16.4.26:17777
ENV HTTPS_PROXY=http://172.16.4.26:17777


COPY go.mod .
COPY go.sum .
RUN go mod download

# static build
ADD . .
RUN go build -a --ldflags '-extldflags "-static"' entrypoints/main.go


# copy executable file and certs to a pure container
FROM alpine:3.9
COPY --from=gobuild /etc/ssl/certs /etc/ssl/certs
COPY --from=gobuild /goapp/main go-graphql-srv

CMD ./go-graphql-srv --debug --addr=127.0.0.1:8080 --dbaddr=127.0.0.1:27017
