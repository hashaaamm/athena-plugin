#!/usr/bin/env bash
# Create the Pulumi state backend, and the account's GitHub OIDC provider. Run once per AWS account
# and region, by a human, before the first `pulumi up`.
#
# This is deliberately NOT Pulumi. A stack cannot create the bucket that holds its own state — the
# first `pulumi up` would need somewhere to write before that somewhere exists. Every IaC tool has
# this bootstrap hole; the honest response is a short, idempotent script rather than a stack that
# pretends to manage its own backend.
#
# Three things, all account-level and all shared by every service generated into this account
# (ADR-0021 — state lives in the target cloud):
#
#   * the KMS key `alias/pulumi-state`, which encrypts the bucket and every stack's secrets;
#   * the S3 bucket `pulumi-state-<account>-<region>`, which holds every stack's state;
#   * the IAM OIDC provider for `token.actions.githubusercontent.com`, which every service's CI
#     roles trust. There is exactly one per account. A per-service stack that created it would
#     delete it from under its neighbours on `destroy` — the ownership problem ADR-0013 describes
#     for the bucket — so it lives here, and the stack only looks it up.
#
# Safe to re-run: every step checks before it creates, and every setting is re-applied, so a run
# also repairs a bucket somebody loosened by hand.
#
#   ./infra/bootstrap/state-bucket.sh
#
# This is the first command in the whole setup that creates something outside the repository. It is
# cheap — a bucket and a KMS key are pennies a month — but it is the line where "reading" stops.
set -euo pipefail

ACCOUNT="${AWS_ACCOUNT_ID:-{{ cookiecutter.aws_account_id }}}"
REGION="${AWS_REGION:-{{ cookiecutter.aws_region }}}"
BUCKET="pulumi-state-${ACCOUNT}-${REGION}"
KEY_ALIAS="alias/pulumi-state"
OIDC_URL="https://token.actions.githubusercontent.com"
OIDC_AUDIENCE="sts.amazonaws.com"

export AWS_REGION="${REGION}"

command -v aws >/dev/null || { echo "aws is not installed"; exit 1; }

echo "account = ${ACCOUNT}"
echo "region  = ${REGION}"
echo "bucket  = s3://${BUCKET}"
echo

echo "==> Checking which account these credentials are for"
# The wrong-account guard. Everything below is account-level and shared, so creating it in the
# account your shell happens to point at — a personal sandbox, last week's client — is exactly the
# mistake that is cheap to make and tedious to find. The stack has the same guard
# (`allowed_account_ids`); this is it for the one step the stack cannot run.
CALLER="$(aws sts get-caller-identity --query Account --output text)"
if [[ "${CALLER}" != "${ACCOUNT}" ]]; then
  cat >&2 <<EOF

Refusing to bootstrap: these credentials are for account ${CALLER}, not ${ACCOUNT}.

Point your shell at ${ACCOUNT} (AWS_PROFILE, or 'aws sso login --profile …'), or run this
against another account deliberately with AWS_ACCOUNT_ID=<id>.

EOF
  exit 1
fi
echo "    ${CALLER}"

echo "==> KMS key for state and stack secrets"
if KEY_ARN="$(aws kms describe-key --key-id "${KEY_ALIAS}" --query KeyMetadata.Arn --output text 2>/dev/null)"; then
  echo "    exists: ${KEY_ARN}"
else
  # Symmetric, the default: one key that both encrypts and decrypts, which is what S3 and the
  # Pulumi secrets provider both use.
  KEY_ARN="$(aws kms create-key \
    --description "Pulumi state and stack secrets (managed by bootstrap)" \
    --tags TagKey=managed-by,TagValue=bootstrap \
    --query KeyMetadata.Arn --output text)"
  aws kms create-alias --alias-name "${KEY_ALIAS}" --target-key-id "${KEY_ARN}"
  echo "    created: ${KEY_ARN}"
fi
KEY_STATE="$(aws kms describe-key --key-id "${KEY_ARN}" --query KeyMetadata.KeyState --output text)"
if [[ "${KEY_STATE}" != "Enabled" ]]; then
  # Most likely a teardown less than 30 days ago. A key pending deletion encrypts nothing.
  echo "    ${KEY_ALIAS} is ${KEY_STATE}, not Enabled — see the end of teardown.sh's output" >&2
  exit 1
fi
# Yearly rotation of the key material. Old material is kept for decryption, so nothing encrypted
# under it becomes unreadable; it only limits how much one version of the key ever protects.
aws kms enable-key-rotation --key-id "${KEY_ARN}"

echo "==> State bucket"
# `--expected-bucket-owner` turns "a bucket by that name exists" into "a bucket by that name exists
# *in this account*". Bucket names are global; a 403 here means somebody else owns this one.
if aws s3api head-bucket --bucket "${BUCKET}" --expected-bucket-owner "${ACCOUNT}" >/dev/null 2>&1; then
  echo "    exists"
elif aws s3api head-bucket --bucket "${BUCKET}" 2>&1 | grep -q "403"; then
  echo "    s3://${BUCKET} exists and is not this account's; refusing to guess" >&2
  exit 1
else
  # us-east-1 is the one region that refuses a LocationConstraint naming itself.
  if [[ "${REGION}" == "us-east-1" ]]; then
    aws s3api create-bucket --bucket "${BUCKET}" >/dev/null
  else
    aws s3api create-bucket --bucket "${BUCKET}" \
      --create-bucket-configuration "LocationConstraint=${REGION}" >/dev/null
  fi
  echo "    created"
fi

# Every setting below is applied on every run, not only on creation, so the script is also the
# repair for a bucket that drifted.

# All four Block Public Access settings: state lists every resource name in the account, and the
# stack's secrets are in it, encrypted.
aws s3api put-public-access-block --bucket "${BUCKET}" \
  --public-access-block-configuration \
  BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true

# No ACLs at all. Per-object ACLs on a state bucket are a way to grant access by accident.
aws s3api put-bucket-ownership-controls --bucket "${BUCKET}" \
  --ownership-controls 'Rules=[{ObjectOwnership=BucketOwnerEnforced}]'

# Versioning: state is the only record of what exists, and `pulumi up` overwrites it.
aws s3api put-bucket-versioning --bucket "${BUCKET}" --versioning-configuration Status=Enabled

# SSE-KMS with the state key, and an S3 Bucket Key so S3 asks KMS for a data key per bucket rather
# than per object — fewer KMS requests, which are billed.
aws s3api put-bucket-encryption --bucket "${BUCKET}" --server-side-encryption-configuration "$(cat <<EOF
{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"aws:kms","KMSMasterKeyID":"${KEY_ARN}"},"BucketKeyEnabled":true}]}
EOF
)"

# TLS or nothing, for every caller, including this account's own.
aws s3api put-bucket-policy --bucket "${BUCKET}" --policy "$(cat <<EOF
{"Version":"2012-10-17","Statement":[{"Sid":"DenyInsecureTransport","Effect":"Deny","Principal":"*","Action":"s3:*","Resource":["arn:aws:s3:::${BUCKET}","arn:aws:s3:::${BUCKET}/*"],"Condition":{"Bool":{"aws:SecureTransport":"false"}}}]}
EOF
)"

# Versioning keeps every state file `up` ever wrote. Ninety days of history is a quarter to notice
# and roll back a bad apply; past that, old versions are storage nobody reads. Incomplete multipart
# uploads are invisible and billed, so they go after a week.
aws s3api put-bucket-lifecycle-configuration --bucket "${BUCKET}" --lifecycle-configuration "$(cat <<'EOF'
{"Rules":[
  {"ID":"expire-old-state-versions","Status":"Enabled","Filter":{},"NoncurrentVersionExpiration":{"NoncurrentDays":90}},
  {"ID":"abort-incomplete-uploads","Status":"Enabled","Filter":{},"AbortIncompleteMultipartUpload":{"DaysAfterInitiation":7}}
]}
EOF
)" >/dev/null

aws s3api put-bucket-tagging --bucket "${BUCKET}" --tagging 'TagSet=[{Key=managed-by,Value=bootstrap}]'
echo "    block public access, owner-enforced, versioned, SSE-KMS, TLS-only, lifecycle"

echo "==> GitHub OIDC provider"
OIDC_ARN="arn:aws:iam::${ACCOUNT}:oidc-provider/${OIDC_URL#https://}"
if aws iam get-open-id-connect-provider --open-id-connect-provider-arn "${OIDC_ARN}" >/dev/null 2>&1; then
  echo "    exists: ${OIDC_ARN}"
else
  # No thumbprint: IAM verifies GitHub's certificate against its own trusted CAs for this issuer,
  # and a pinned thumbprint is one more thing that breaks when GitHub rotates its certificate.
  aws iam create-open-id-connect-provider \
    --url "${OIDC_URL}" \
    --client-id-list "${OIDC_AUDIENCE}" \
    --tags Key=managed-by,Value=bootstrap >/dev/null
  echo "    created: ${OIDC_ARN}"
fi

echo "==> Service-linked roles Express Mode needs"
# Express Mode's first deployment in an account creates the load balancer's and ECS auto scaling's
# service-linked roles itself, and then uses them in the same second — before IAM has made them
# usable. CreateLoadBalancer and RegisterScalableTarget fail, and the deployment rolls back with
# "the infrastructure role doesn't have enough permission", which reads as a policy problem and is
# not one. Created here, minutes earlier, they are usable by the time anything asks for them.
#
# Account-wide and shared with everything else in the account that runs a load balancer or scales
# an ECS service, so nothing in this repository ever deletes them — teardown included.
for service in ecs.amazonaws.com elasticloadbalancing.amazonaws.com ecs.application-autoscaling.amazonaws.com; do
  if output="$(aws iam create-service-linked-role --aws-service-name "${service}" 2>&1)"; then
    echo "    created: ${service}"
  elif grep -q "has been taken" <<<"${output}"; then
    echo "    exists:  ${service}"
  else
    echo "${output}" >&2
    exit 1
  fi
done

SECRETS_PROVIDER="awskms://${KEY_ALIAS}?region=${REGION}"

cat <<EOF

Bootstrap complete. Next, from the repository root:

  pulumi -C infra/pulumi stack init dev --secrets-provider="${SECRETS_PROVIDER}"
  (cd infra/pulumi && uv lock)
  just infra-test
  just infra-preview

No 'pulumi login' is needed: infra/pulumi/Pulumi.yaml names this bucket as its backend. And no
lock table: Pulumi's self-managed backend locks with objects under .pulumi/locks/ in the bucket.

The KMS key encrypts stack *configuration* and state at rest. It is not where application secrets
live — those are Secrets Manager secrets the stack creates, and a human populates the manual ones,
because a human-chosen secret value must never enter infrastructure code or state.

EOF
