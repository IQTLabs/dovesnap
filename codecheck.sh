#!/bin/sh

GOFMTOUT=$(gofmt -l *go */*go)

if [ "$GOFMTOUT" != "" ] ; then
	echo gofmt reports files with formatting inconsistencies: $GOFMTOUT
	exit 1
fi

exit 0
