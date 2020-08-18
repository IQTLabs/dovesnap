FROM golang:1.15
RUN apt-get update && apt-get -y install iptables dbus go-dep
RUN update-alternatives --set iptables /usr/sbin/iptables-legacy
RUN apt-get update && apt-get install -y \
    openvswitch-common \
    openvswitch-switch \
    udhcpc
COPY . /go/src/dovesnap
WORKDIR /go/src/dovesnap
RUN dep ensure
RUN go install -v
ENTRYPOINT ["dovesnap"]
