#!/bin/bash -eu

DEBIAN_FRONTEND=noninteractive TZ=Etc/UTC apt install -y --no-install-recommends build-essential automake libtool gettext autopoint tclsh tcl libsqlite3-dev pkg-config git > /dev/null
