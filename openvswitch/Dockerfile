FROM debian:buster-slim
ENV OVS_VERSION 2.13.0
ENV SUPERVISOR_STDOUT_VERSION 0.1.1
RUN apt-get update && apt-get install -y python3-pip python3 python python-setuptools make autoconf wget gcc supervisor
# Configure supervisord
RUN mkdir -p /var/log/supervisor/
ADD supervisord.conf /etc/
# Install supervisor_stdout
WORKDIR /opt
RUN mkdir -p /var/log/supervisor/
RUN mkdir -p /etc/openvswitch
RUN wget https://pypi.python.org/packages/source/s/supervisor-stdout/supervisor-stdout-$SUPERVISOR_STDOUT_VERSION.tar.gz --no-check-certificate && \
	tar -xzvf supervisor-stdout-0.1.1.tar.gz && \
	mv supervisor-stdout-$SUPERVISOR_STDOUT_VERSION supervisor-stdout && \
	rm supervisor-stdout-0.1.1.tar.gz && \
	cd supervisor-stdout && \
	python setup.py install -q
# Get Open vSwitch
WORKDIR /
RUN pip3 install six
RUN wget https://www.openvswitch.org/releases/openvswitch-$OVS_VERSION.tar.gz --no-check-certificate && \
	tar -xzvf openvswitch-$OVS_VERSION.tar.gz &&\
	mv openvswitch-$OVS_VERSION openvswitch &&\
	cd openvswitch && \
	./configure && make && make install && cd .. && \
	cp -r openvswitch/* / &&\
	rm -r openvswitch &&\
	rm openvswitch-$OVS_VERSION.tar.gz
ADD configure-ovs.sh /usr/local/share/openvswitch/
RUN mkdir -p /usr/local/var/run/openvswitch
# Create the database
RUN ovsdb-tool create /etc/openvswitch/conf.db /usr/local/share/openvswitch/vswitch.ovsschema
# Put the OVS Python modules on the Python Path
#RUN cp -r /usr/local/share/openvswitch/python/ovs /usr/lib/python2.7/site-packages/ovs
CMD ["/usr/bin/supervisord"]
