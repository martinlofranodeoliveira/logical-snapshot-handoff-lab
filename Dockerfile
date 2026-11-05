FROM golang:1.27-bookworm@sha256:d9b6a1beb9474adc2a1ce842573639b44b2a0dd49b6669f51d3821baf9ec37c9
USER root
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl gnupg && rm -rf /var/lib/apt/lists/*
RUN install -d /usr/share/postgresql-common/pgdg /etc/postgresql-common \
 && curl --fail --silent --show-error --location https://www.postgresql.org/media/keys/ACCC4CF8.asc -o /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc \
 && gpg --batch --show-keys --with-colons /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc | grep -F 'B97B0AFCAA1A47F044F244A07FCC7D46ACCC4CF8' \
 && printf 'create_main_cluster = false\n' > /etc/postgresql-common/createcluster.conf \
 && printf 'Types: deb\nURIs: https://apt.postgresql.org/pub/repos/apt\nSuites: bookworm-pgdg\nArchitectures: amd64\nComponents: main\nSigned-By: /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc\n' > /etc/apt/sources.list.d/pgdg.sources
RUN apt-get update \
 && apt-get install -y --no-install-recommends postgresql-17=17.11-1.pgdg12+2 postgresql-client-17=17.11-1.pgdg12+2 libpq-dev=18.6-1.pgdg12+2 libpq5=18.6-1.pgdg12+2 \
 && dpkg-query -W > /usr/local/share/wave8-package-versions.txt \
 && rm -rf /var/lib/apt/lists/*
COPY tools/build-sqlite.sh /tmp/build-sqlite.sh
RUN sh /tmp/build-sqlite.sh && rm /tmp/build-sqlite.sh
ENV PATH=/usr/lib/postgresql/17/bin:/usr/local/go/bin:/opt/sqlite/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
ENV CGO_ENABLED=1 GOFLAGS=-tags=libsqlite3 CGO_CFLAGS=-I/opt/sqlite/include CGO_LDFLAGS=-L/opt/sqlite/lib LD_LIBRARY_PATH=/opt/sqlite/lib GOTOOLCHAIN=local GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org GOMODCACHE=/opt/go-mod GOCACHE=/tmp/go-cache GOPATH=/tmp/go-path LAB_INTEGRATION=1
WORKDIR /app
COPY go.mod go.sum /app/
RUN go mod download all && go mod verify
COPY . /app
RUN groupadd --gid 1000 lab && useradd --uid 1000 --gid 1000 --create-home --shell /bin/bash lab
USER 1000:1000
CMD ["go", "test", "-p", "1", "./..."]
