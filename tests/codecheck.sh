#!/bin/sh
set -e

echo running gofmt...
GOFMTOUT=$(gofmt -l *go */*go)
if [ "$GOFMTOUT" != "" ] ; then
	echo FAIL: gofmt reports files with formatting inconsistencies - fix with gofmt -w: $GOFMTOUT
	exit 1
fi

pip3 install -r codecheck-requirements.txt
# pytype needs .py
cp graph_dovesnap/graph_dovesnap /tmp/graph_dovesnap.py && pytype /tmp/graph_dovesnap.py && rm -f /tmp/graph_dovesnap.py

echo ok
exit 0
