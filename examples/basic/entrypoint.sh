#!/bin/sh
set -eu

docktinel daemon --startup-timeout 30s &

docktinel add job prepare --phase startup --timeout 5s \
    -- sh -c 'printf "Hello from Docktinel!\n" > /srv/index.html'
docktinel add check ready --phase after-start --deadline 20s --interval 1s \
    -- demo check
docktinel add check http --interval 1s --timeout 2s --failures 3 \
    -- demo check
docktinel add job maintenance --interval 30s --timeout 5s \
    -- sh -c 'echo "Maintenance: $(date -u)"'

docktinel startup
exec docktinel exec -- demo serve
