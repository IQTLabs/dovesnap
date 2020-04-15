FROM golang
RUN apt-get update && apt-get -y install iptables dbus go-dep
COPY . /go/src/github.com/cglewis/dovesnap
WORKDIR /go/src/github.com/cglewis/dovesnap
RUN dep ensure
RUN go install -v
ENTRYPOINT ["dovesnap"]
