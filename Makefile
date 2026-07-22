# Thin delegator to scripts/dev.sh (OD-16: `make` is absent on the Windows dev host, present
# in CI's Ubuntu image; both call the SAME script, so dev==CI holds). Prefer scripts/dev.sh
# directly on Windows/Git-Bash.
.PHONY: certs build up seed test reset down logs ps help
certs:  ; @bash scripts/dev.sh certs
build:  ; @bash scripts/dev.sh build
up:     ; @bash scripts/dev.sh up
seed:   ; @bash scripts/dev.sh seed
test:   ; @bash scripts/dev.sh test
reset:  ; @bash scripts/dev.sh reset
down:   ; @bash scripts/dev.sh down
logs:   ; @bash scripts/dev.sh logs
ps:     ; @bash scripts/dev.sh ps
help:   ; @bash scripts/dev.sh help
