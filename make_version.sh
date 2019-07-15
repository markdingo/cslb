#! /bin/sh

# Extract version and release date info from the ChangeLog.md

cl=$1
if [ -z "$cl" ]; then
    echo Error: Need the changelog file as parameter one >&2
    exit 1
fi

# Looking for '### version -- date'
#              $1  $2      $3 $4

recent=`grep '^### v' $cl | head -1`
if [ $? -ne 0 ]; then
    echo Error: changelog $cl does not contain a version heading >&2
    exit 1
fi

set -- $recent
version=$2
date=$4
printf 'package cslb\n\nconst (\n'
printf '\t// Version is auto-generated from ChangeLog.md\n'
printf '\tVersion = "%s"\n' "${version}"
printf '\t// ReleaseDate is also auto-generated from ChangeLog.md\n'
printf '\tReleaseDate = "%s"\n' "${date}"
printf ')\n'

