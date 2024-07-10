FROM golang:1.22.5-bullseye AS gobuild

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
ENV GOOS=linux
ENV GOARCH=amd64
RUN go build -a -ldflags '-w -extldflags "-static"' -o main main.go

# copy executable file and certs to a pure container
FROM debian:bullseye

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

ENTRYPOINT [ "/app/go-graphql-srv" ]
CMD [ "api", "-c", "/etc/laisky-blog-graphql/settings.yml" ]
