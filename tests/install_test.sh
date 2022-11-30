#!/bin/sh

sudo modprobe openvswitch && \
  sudo modprobe 8021q && \
  export DEBIAN_FRONTEND=noninteractive && \
  echo 'debconf debconf/frontend select Noninteractive' | sudo debconf-set-selections && \
  sudo apt-get update && \
  sudo apt-get remove docker docker-engine docker.io containerd runc python3-yaml && \
  sudo apt-get install apt-transport-https ca-certificates curl gnupg-agent software-properties-common graphviz wget python3-dev udhcpd jq && \
  update-alternatives --display iptables && \
  sudo update-alternatives --set iptables /usr/sbin/iptables-legacy && \
  update-alternatives --display iptables && \
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo apt-key add - && \
  sudo add-apt-repository "deb [arch=amd64] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" && \
  sudo apt-get update && sudo apt-get install docker-ce docker-ce-cli containerd.io && \
  sudo service docker stop && \
  echo '{"bip": "172.16.0.1/24", "default-address-pools": [{"base":"172.16.1.0/24", "size":26}]}' > /tmp/docker.json && \
  sudo cp /tmp/docker.json /etc/docker && \
  sudo service docker start && \
  ./poetrybuild.sh &&
  cd openvswitch && docker build -f Dockerfile . -t iqtlabs/openvswitch:v3.0.2 && cd .. && \
  sudo ip link && sudo ip addr
