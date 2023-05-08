#!/bin/bash

sudo apt-get update && \
  sudo apt-get remove python3-yaml python3-pexpect && \
  sudo pip3 install -U pip && \
  poetry config virtualenvs.create false && \
  sudo poetry install
