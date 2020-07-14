#!/bin/sh

echo running gofmt...
GOFMTOUT=$(gofmt -l *go */*go)
if [ "$GOFMTOUT" != "" ] ; then
	echo FAIL: gofmt reports files with formatting inconsistencies: $GOFMTOUT
	exit 1
fi

echo ok
exit 0
