FROM golang:1.15
RUN apt-get update && apt-get -y --no-install-recommends install iptables dbus go-dep \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/*
RUN update-alternatives --set iptables /usr/sbin/iptables-legacy
RUN apt-get update && apt-get install -y --no-install-recommends \
    openvswitch-common \
    openvswitch-switch \
    udhcpc ethtool \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/*
COPY . /go/src/dovesnap
COPY udhcpclog.sh /udhcpclog.sh
WORKDIR /go/src/dovesnap
RUN go install -v
ENTRYPOINT ["dovesnap"]
