#!/bin/sh

if [ "$(gofmt -l ovs)" != "" ] ; then
	echo gofmt must return no diff
	exit 1
fi

exit 0
