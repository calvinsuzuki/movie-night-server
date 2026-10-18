#!/bin/sh
# Refresh icon and menu caches so Movie night shows up immediately.
gtk-update-icon-cache -q -t -f /usr/share/icons/hicolor 2>/dev/null || true
update-desktop-database -q /usr/share/applications 2>/dev/null || true
exit 0
