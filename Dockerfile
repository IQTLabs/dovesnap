FROM ubuntu:24.04 AS builder
LABEL maintainer="Charlie Lewis <clewis@iqt.org>"
RUN apt-get update && apt-get install -y --no-install-recommends \
    golang ca-certificates
COPY . /go/src/dovesnap
WORKDIR /go/src/dovesnap
RUN go build -o /dovesnap .

FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends \
    iptables dbus && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*
RUN update-alternatives --set iptables /usr/sbin/iptables-legacy
RUN apt-get update && apt-get install -y --no-install-recommends \
    ethtool iproute2 openvswitch-common openvswitch-switch \
    udhcpc golang && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*
WORKDIR /
COPY --from=builder /dovesnap/ .
COPY udhcpclog.sh /udhcpclog.sh
ENTRYPOINT ["/dovesnap"]
