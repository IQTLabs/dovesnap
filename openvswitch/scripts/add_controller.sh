#!/bin/bash

# TODO handle when more than one?
BR_NAME=$(ovs-vsctl list-br)

ovs-vsctl set-controller $BR_NAME $2 $3
