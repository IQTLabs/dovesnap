FROM golang:1.14
RUN apt-get update && apt-get -y install iptables dbus go-dep
RUN update-alternatives --set iptables /usr/sbin/iptables-legacy
RUN apt-get update && apt-get install -y openvswitch-common openvswitch-switch python3-pip python3 python python-setuptools make autoconf wget gcc git
#ENV OVS_VERSION 2.13.0
#RUN wget https://www.openvswitch.org/releases/openvswitch-$OVS_VERSION.tar.gz --no-check-certificate && \
#        tar -xzvf openvswitch-$OVS_VERSION.tar.gz &&\
#        mv openvswitch-$OVS_VERSION openvswitch &&\
#        cd openvswitch && \
#        ./configure && make && make install && cd .. && \
#        cp -r openvswitch/* / &&\
#        rm -r openvswitch &&\
#        rm openvswitch-$OVS_VERSION.tar.gz
COPY . /go/src/dovesnap
WORKDIR /go/src/dovesnap
RUN dep ensure
RUN go install -v
ENTRYPOINT ["dovesnap"]
