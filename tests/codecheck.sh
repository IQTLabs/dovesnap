#!/bin/sh
set -e

echo running gofmt...
GOFMTOUT=$(gofmt -l *go */*go)
if [ "$GOFMTOUT" != "" ] ; then
	echo FAIL: gofmt reports files with formatting inconsistencies - fix with gofmt -w: $GOFMTOUT
	exit 1
fi

# pytype needs .py
for i in bin/graph_dovesnap ; do
	b=$(basename $i)
	t=/tmp/${b}.py
	cp $i $t
	pytype $t
	rm -f $t
done

echo ok
exit 0
