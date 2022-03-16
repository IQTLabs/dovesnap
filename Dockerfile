FROM golang:1.17 AS build
LABEL maintainer="Charlie Lewis <clewis@iqt.org>"
COPY . /go/src/dovesnap
WORKDIR /go/src/dovesnap
RUN go build -o /out/dovesnap .
FROM debian:bullseye
COPY --from=build /out/dovesnap /
RUN apt-get update && apt-get install -y --no-install-recommends \
    iptables dbus && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*
RUN update-alternatives --set iptables /usr/sbin/iptables-legacy
RUN apt-get update && apt-get install -y --no-install-recommends \
    ethtool \
    openvswitch-common \
    openvswitch-switch \
    udhcpc && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*
COPY udhcpclog.sh /udhcpclog.sh
ENTRYPOINT ["/dovesnap"]
