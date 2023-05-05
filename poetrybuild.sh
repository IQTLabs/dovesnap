#!/bin/bash

sudo apt-get update && \
  sudo apt-get install -yq --no-install-recommends python3 python3-dev && \
  sudo apt-get remove python3-yaml python3-pexpect && \
  sudo pip3 install -U pip && \
  sudo pip3 install poetry==1.4.2 && \
  poetry config virtualenvs.create false && \
  sudo poetry install
