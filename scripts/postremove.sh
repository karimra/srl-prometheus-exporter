#!/bin/bash

appmgr=$(/opt/srlinux/bin/sr_linux --status | grep app_mgr | cut -d: -f 2 | tr -s ' ')

if [[ $appmgr != "not running" ]]
then
    /opt/srlinux/bin/sr_cli tools system app-management application app_mgr reload
fi
