#!/bin/sh

sudo modprobe openvswitch && \
  sudo modprobe 8021q && \
  export DEBIAN_FRONTEND=noninteractive && \
  echo 'debconf debconf/frontend select Noninteractive' | sudo debconf-set-selections
  sudo apt-get update && \
  sudo apt-get remove docker docker-engine docker.io containerd runc python3-yaml && \
  sudo apt-get install apt-transport-https ca-certificates curl gnupg-agent software-properties-common && \
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo apt-key add - && \
  sudo add-apt-repository "deb [arch=amd64] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" && \
  sudo apt-get update && sudo apt-get install docker-ce docker-ce-cli containerd.io libperl-dev wget python3-setuptools python3-dev udhcpd jq && \
  curl -fsSLO https://gitlab.com/api/v4/projects/4207231/packages/generic/graphviz-releases/2.47.3/graphviz-2.47.3.tar.gz && \
  tar -xf graphviz-2.47.3.tar.gz && cd graphviz-2.47.3 && ./configure && make && sudo make install && \
  sudo pip3 install -r graph_dovesnap/requirements.txt
