FROM golang:1.16
LABEL maintainer="Charlie Lewis <clewis@iqt.org>"
RUN apt-get update && apt-get install -y --no-install-recommends \
    iptables dbus go-dep && \
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
COPY . /go/src/dovesnap
COPY udhcpclog.sh /udhcpclog.sh
WORKDIR /go/src/dovesnap
RUN go install -v
ENTRYPOINT ["dovesnap"]
