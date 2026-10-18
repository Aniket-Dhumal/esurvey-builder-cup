#!/usr/bin/env sh
# Generate the secrets .env.example marks GENERATE.
#
#   ./scripts/gen-secrets.sh >> .env
#
# Then delete the blank originals from .env, or the later lines win by
# accident rather than on purpose.
#
# Values are read from /dev/urandom via openssl. They are printed to stdout and
# nowhere else: do not redirect this into a file that is committed, and do not
# paste the output into a chat window.

set -eu

if ! command -v openssl >/dev/null 2>&1; then
    echo "openssl is required" >&2
    exit 1
fi

# 32 bytes for each. The signing seed must be exactly that -- Ed25519 defines
# the seed size, and the keyring refuses anything else at startup rather than
# silently truncating.
printf 'POSTGRES_PASSWORD=%s\n' "$(openssl rand -base64 32 | tr -d '\n/+=' | cut -c1-32)"
printf 'REDIS_PASSWORD=%s\n' "$(openssl rand -base64 32 | tr -d '\n/+=' | cut -c1-32)"
printf 'ESURVEY_JWT_SEED=%s\n' "$(openssl rand -base64 32)"

# The bootstrap password is a real password: it must pass the same policy every
# officer password does, so it is generated at full length rather than trimmed.
printf 'ESURVEY_BOOTSTRAP_ADMIN_PASSWORD=%s\n' "$(openssl rand -base64 24)"

cat >&2 <<'EOF'

Written to stdout. Two things to do now:

  1. Remove the empty POSTGRES_PASSWORD / REDIS_PASSWORD / ESURVEY_JWT_SEED /
     ESURVEY_BOOTSTRAP_ADMIN_PASSWORD lines that .env.example put in .env, so
     the generated ones are the only definitions.

  2. After the first administrator has set their own password, delete
     ESURVEY_BOOTSTRAP_ADMIN_PASSWORD from .env entirely. It is a credential
     someone other than the account holder has seen.

EOF
