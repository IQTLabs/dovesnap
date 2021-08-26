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
  sudo apt-get update && sudo apt-get install docker-ce docker-ce-cli containerd.io wget graphviz python3-setuptools python3-dev udhcpd jq && \
  sudo pip3 install -r requirements.txt && pip3 install -r codecheck-requirements.txt
