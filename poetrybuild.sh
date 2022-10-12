#!/bin/bash

sudo apt-get update && \
  sudo apt-get install -yq --no-install-recommends python3 python3-dev && \
  sudo pip3 install poetry==1.1.15 && \
  poetry config virtualenvs.create false && \
  sudo poetry install
