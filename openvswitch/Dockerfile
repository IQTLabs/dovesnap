FROM debian:bullseye-slim
RUN apt-get update && apt-get install -y --no-install-recommends python3-pip python3 python python-setuptools make autoconf wget gcc git libc6-dev supervisor \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/*
# Configure supervisord
RUN mkdir -p /var/log/supervisor/
COPY supervisord.conf /etc/
# Get Open vSwitch
WORKDIR /
RUN mkdir -p /etc/openvswitch /usr/local/var/run/openvswitch
RUN pip3 install six
ENV OVS_VERSION 2.17.1
RUN wget https://www.openvswitch.org/releases/openvswitch-$OVS_VERSION.tar.gz --no-check-certificate && \
 tar -xzvf openvswitch-$OVS_VERSION.tar.gz &&\
 mv openvswitch-$OVS_VERSION openvswitch &&\
 cd openvswitch && \
 ./configure && make && make install && cd .. && \
 rm -r openvswitch &&\
 rm openvswitch-$OVS_VERSION.tar.gz
COPY configure-ovs.sh /usr/local/share/openvswitch/
# Create the database
RUN ovsdb-tool create /etc/openvswitch/conf.db /usr/local/share/openvswitch/vswitch.ovsschema
CMD ["/usr/bin/supervisord", "-c", "/etc/supervisord.conf"]
HEALTHCHECK --interval=30s --timeout=5s CMD ovs-vsctl show
