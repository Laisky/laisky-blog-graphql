FROM golang:1.13.4-alpine3.10 AS gobuild

# run dependencies
RUN apk update && apk upgrade && \
    apk add --no-cache gcc git build-base ca-certificates curl && \
    update-ca-certificates

ENV GO111MODULE=on
WORKDIR /goapp

COPY go.mod .
COPY go.sum .
RUN go mod download

# static build
ADD . .
RUN go build -a --ldflags '-extldflags "-static"' entrypoints/main.go


# copy executable file and certs to a pure container
FROM alpine:3.10

WORKDIR /app

COPY --from=gobuild /etc/ssl/certs /etc/ssl/certs
COPY --from=gobuild /goapp/main /app/go-graphql-srv

RUN chmod +rx -R /app && \
    adduser -S laisky
USER laisky

ENTRYPOINT [ "./go-graphql-srv" ]
CMD [ "--debug", "--addr=127.0.0.1:8080", "--dbaddr=127.0.0.1:27017" ]
