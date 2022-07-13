#!/bin/sh
set -e

echo running gofmt...
GOFMTOUT=$(gofmt -l *go */*go)
if [ "$GOFMTOUT" != "" ] ; then
	echo FAIL: gofmt reports files with formatting inconsistencies - fix with gofmt -w: $GOFMTOUT
	exit 1
fi

python3 -m pip install ".[codecheck]"
# pytype needs .py
cp graph_dovesnap/graph_dovesnap /tmp/graph_dovesnap.py && pytype /tmp/graph_dovesnap.py && rm -f /tmp/graph_dovesnap.py

echo ok
exit 0
