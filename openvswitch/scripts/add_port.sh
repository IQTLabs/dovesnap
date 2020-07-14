#!/bin/bash

# TODO handle when more than one?
BR_NAME=$(ovs-vsctl list-br)
ovs-vsctl add-port $BR_NAME $1
