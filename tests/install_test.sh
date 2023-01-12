#!/bin/sh

sudo modprobe openvswitch && \
  sudo modprobe 8021q && \
  export DEBIAN_FRONTEND=noninteractive && \
  echo 'debconf debconf/frontend select Noninteractive' | sudo debconf-set-selections && \
  sudo apt-get update && \
  sudo apt-get purge docker docker-engine docker.io containerd runc python3-yaml nftables* iptables* && \
  sudo lsmod && \
  sudo apt-get install apt-transport-https ca-certificates curl gnupg-agent software-properties-common graphviz wget python3-dev udhcpd jq iptables && \
  sudo update-alternatives --set iptables /usr/sbin/iptables-legacy && \
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo apt-key add - && \
  sudo add-apt-repository "deb [arch=amd64] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" && \
  sudo apt-get update && sudo apt-get install docker-ce docker-ce-cli containerd.io && \
  ./poetrybuild.sh &&
  cd openvswitch && docker build -f Dockerfile . -t iqtlabs/openvswitch:v3.0.2 && cd .. && \
  sudo ip link && sudo ip addr && iptables --version && docker --version && \
  sudo sudo sysctl -a |grep forward && \
  sudo iptables -L && \
  sudo lsmod|grep -i masq
