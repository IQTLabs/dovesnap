FROM golang
RUN apt-get update && apt-get -y install iptables dbus go-dep
RUN update-alternatives --set iptables /usr/sbin/iptables-legacy
COPY . /go/src/github.com/cglewis/dovesnap
WORKDIR /go/src/github.com/cglewis/dovesnap
RUN dep ensure
RUN go install -v
ENTRYPOINT ["dovesnap"]
