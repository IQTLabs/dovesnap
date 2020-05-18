#!/bin/bash

# TODO handle when more than one?
BR_NAME=$(ovs-vsctl list-br)

ovs-vsctl del-port $BR_NAME $1
