FROM ubuntu:22.04
LABEL maintainer="Charlie Lewis <clewis@iqt.org>"
RUN apt-get update && apt-get install -y --no-install-recommends \
    iptables dbus && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*
RUN update-alternatives --set iptables /usr/sbin/iptables-legacy
RUN apt-get update && apt-get install -y --no-install-recommends \
    ethtool iproute2 openvswitch-common openvswitch-switch \
    udhcpc ca-certificates golang && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*
COPY . /go/src/dovesnap
WORKDIR /go/src/dovesnap
RUN go build -o / .
COPY udhcpclog.sh /udhcpclog.sh
ENTRYPOINT ["/dovesnap"]
