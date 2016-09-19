#!/bin/sh

if [ "$1" == "" ]; then
	echo "Moves a resource between blue and green namespaces"
	echo "Syntax: %0 <resource-name>"
	echo "e.g. $0 routes/ruby-hello-world"
	exit 1
fi

RN="$1"

oc get $RN -n blue  > /dev/null 2>&1
if [ "$?" == "0" ]; then
	FROM="blue"
	TO="green"
else
	oc get $RN -n green > /dev/null  2>&1
	if [ "$?" == "0" ]; then
		FROM="green"
		TO="blue"
	else
		echo "Unable to find resource in blue or green namespace: $RN"
		exit 1
	fi
fi

R=`oc export $RN -o=json -n $FROM`
oc delete $RN -n $FROM > /dev/null
echo "$R" | oc create -f - -n $TO > /dev/null

echo "Moved $RN from namespace $FROM to $TO"


