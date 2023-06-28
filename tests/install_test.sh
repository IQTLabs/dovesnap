#!/bin/sh

sudo modprobe openvswitch && \
  sudo modprobe 8021q && \
  export DEBIAN_FRONTEND=noninteractive && \
  echo 'debconf debconf/frontend select Noninteractive' | sudo debconf-set-selections && \
  sudo apt-get update && \
  sudo apt-get remove docker docker-engine docker.io containerd runc python3-yaml && \
  sudo apt-get install apt-transport-https ca-certificates curl gnupg-agent software-properties-common graphviz wget udhcpd jq nftables && \
  sudo update-alternatives --set iptables /usr/sbin/iptables-legacy && \
  sudo nft flush ruleset && \
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo apt-key add - && \
  sudo add-apt-repository "deb [arch=amd64] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" && \
  sudo apt-get update && sudo apt-get install docker-ce docker-ce-cli containerd.io && \
  cd openvswitch && docker build -f Dockerfile . -t iqtlabs/openvswitch:v3.1.2 && cd .. && \
  sudo ip link && sudo ip addr
