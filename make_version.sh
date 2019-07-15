#! /bin/sh

# Extract version info from the change log

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
printf '\tVersion     = "%s" // See ChangeLog.md for history\n' "${version}"
printf '\tReleaseDate = "%s"\n' "${date}"
printf ')\n'

