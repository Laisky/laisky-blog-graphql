FROM golang:1.15.8-buster AS gobuild

# install dependencies
RUN apt-get update \
    && apt-get install -y --no-install-recommends g++ make gcc git build-essential ca-certificates curl \
    && update-ca-certificates

ENV GO111MODULE=on
WORKDIR /app
COPY go.mod .
COPY go.sum .
RUN go mod download

# static build
ADD . .
RUN go build -a -ldflags '-w -extldflags "-static"' -o main entrypoints/main.go


# copy executable file and certs to a pure container
FROM debian:buster

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates haveged \
    && update-ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=gobuild /etc/ssl/certs /etc/ssl/certs
COPY --from=gobuild /app/main /app/go-graphql-srv

WORKDIR /app
RUN chmod +rx -R /app && \
    adduser --disabled-password --gecos '' laisky
USER laisky

ENTRYPOINT [ "./go-graphql-srv" ]
CMD [ "--debug", "--addr=127.0.0.1:8080", "--dbaddr=127.0.0.1:27017" ]
