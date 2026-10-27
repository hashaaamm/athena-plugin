#!/usr/bin/env bash
# Create the Pulumi state backend, and the account's GitHub OIDC provider. Run once per account
# and region, by a human, before the first `pulumi up`.
#
# This is deliberately NOT Pulumi. A stack cannot create the bucket that holds its own state — the
# first `pulumi up` would need somewhere to write before that somewhere exists. Every IaC tool has
# this bootstrap hole; the honest response is a short, idempotent script rather than a stack that
# pretends to manage its own backend.
#
# Three things, all shared by every service deployed into this account (ADR-0021):
#
#   * the bucket `pulumi-state-<account>-<region>` — one directory per Pulumi project inside it;
#   * the KMS key `alias/pulumi-state`, which encrypts the bucket and every stack's secrets;
#   * the GitHub Actions OIDC provider, which IAM allows exactly one of per issuer per account.
#
# The third is here rather than in a stack for the same reason as the first two: it is not one
# service's to own. A stack that created it would delete it on `pulumi destroy`, from under every
# other service whose CI trusts it. Stacks look it up instead.
#
# Safe to re-run: every step checks before it creates, and every setting is applied again, so a
# bucket somebody loosened by hand is put back.
#
#   ./infra/bootstrap/state-bucket.sh
#
# This is the first command in the whole setup that creates something outside the repository. It is
# cheap — a bucket and a KMS key are pennies a month, and the OIDC provider is free — but it is the
# line where "reading" stops.
set -euo pipefail

ACCOUNT="${AWS_ACCOUNT_ID:-{{ cookiecutter.aws_account_id }}}"
REGION="${AWS_REGION:-{{ cookiecutter.aws_region }}}"
BUCKET="pulumi-state-${ACCOUNT}-${REGION}"
KEY_ALIAS="alias/pulumi-state"
OIDC_HOST="token.actions.githubusercontent.com"
OIDC_AUDIENCE="sts.amazonaws.com"

# ARNs carry the partition, and `aws` is wrong in GovCloud.
PARTITION="aws"
[[ "${REGION}" == us-gov-* ]] && PARTITION="aws-us-gov"
OIDC_ARN="arn:${PARTITION}:iam::${ACCOUNT}:oidc-provider/${OIDC_HOST}"

command -v aws >/dev/null || { echo "aws is not installed"; exit 1; }
export AWS_REGION="${REGION}" AWS_DEFAULT_REGION="${REGION}"

echo "account = ${ACCOUNT}"
echo "region  = ${REGION}"
echo "bucket  = s3://${BUCKET}"
echo

echo "==> Checking which account these credentials are for"
# The wrong-account guard. Every name below is derived from the account id, and the stack's
# provider refuses any other account — so a bootstrap run with a different profile active creates a
# backend nothing will ever use, in an account nobody meant, and bills for it quietly.
CALLER="$(aws sts get-caller-identity --query Account --output text)"
if [[ "${CALLER}" != "${ACCOUNT}" ]]; then
  cat >&2 <<EOF

Refusing to bootstrap: these credentials are for account ${CALLER}, not ${ACCOUNT}.

Select the right profile (AWS_PROFILE=...) or set AWS_ACCOUNT_ID if this repository really
deploys into ${CALLER} — and then change accountId in infra/pulumi/Pulumi.<stack>.yaml and the
backend URL in infra/pulumi/Pulumi.yaml to match.

EOF
  exit 1
fi
echo "    ${CALLER}"

echo "==> KMS key for state and stack secrets"
if KEY_STATE="$(aws kms describe-key --key-id "${KEY_ALIAS}" --query KeyMetadata.KeyState --output text 2>/dev/null)"; then
  # An alias that outlived a teardown points at a key that is scheduled for deletion. Encrypting
  # new state with it would make that state unreadable on the day the key goes.
  if [[ "${KEY_STATE}" != "Enabled" ]]; then
    cat >&2 <<EOF

${KEY_ALIAS} exists but its key is ${KEY_STATE}.

If a teardown scheduled it for deletion and you want it back:

  aws kms cancel-key-deletion --key-id ${KEY_ALIAS}
  aws kms enable-key --key-id ${KEY_ALIAS}

Otherwise remove the alias (aws kms delete-alias --alias-name ${KEY_ALIAS}) and run this again.

EOF
    exit 1
  fi
  echo "    exists"
else
  # Symmetric, for encryption, with the default key policy: the account administers it, and
  # access is granted by IAM policy — which is how the preview role gets kms:Decrypt on exactly
  # this key and nothing else.
  KEY_ID="$(aws kms create-key \
    --description "Pulumi state and stack secrets (managed by infra/bootstrap/state-bucket.sh)" \
    --key-spec SYMMETRIC_DEFAULT \
    --key-usage ENCRYPT_DECRYPT \
    --tags TagKey=managed-by,TagValue=bootstrap \
    --query KeyMetadata.KeyId --output text)"
  aws kms create-alias --alias-name "${KEY_ALIAS}" --target-key-id "${KEY_ID}"
  echo "    created"
fi
KEY_ARN="$(aws kms describe-key --key-id "${KEY_ALIAS}" --query KeyMetadata.Arn --output text)"
# Yearly rotation of the key material. Old material is kept for decryption, so nothing encrypted
# before a rotation becomes unreadable after it.
aws kms enable-key-rotation --key-id "${KEY_ARN}"

echo "==> State bucket"
if aws s3api head-bucket --bucket "${BUCKET}" --expected-bucket-owner "${ACCOUNT}" >/dev/null 2>&1; then
  echo "    exists"
else
  # S3 refuses a LocationConstraint of us-east-1, and requires one everywhere else.
  location=()
  if [[ "${REGION}" != "us-east-1" ]]; then
    location=(--create-bucket-configuration "LocationConstraint=${REGION}")
  fi
  # The `+` form, because macOS ships bash 3.2, where an empty array under `set -u` is unbound.
  aws s3api create-bucket --bucket "${BUCKET}" --object-ownership BucketOwnerEnforced \
    ${location[@]+"${location[@]}"} >/dev/null
  aws s3api wait bucket-exists --bucket "${BUCKET}" --expected-bucket-owner "${ACCOUNT}"
  echo "    created"
fi

# Every setting below is applied on every run, not only at creation: each call is idempotent, and
# a bucket that drifted is put back the next time anyone bootstraps.

# State lists every resource name in the account. Nothing about it is public, ever.
aws s3api put-public-access-block --bucket "${BUCKET}" --public-access-block-configuration \
  BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true
# ACLs off: per-object ACLs on a state bucket are a way to grant access by accident.
aws s3api put-bucket-ownership-controls --bucket "${BUCKET}" \
  --ownership-controls 'Rules=[{ObjectOwnership=BucketOwnerEnforced}]'
# Versioning: state is the only record of what exists, and `pulumi up` overwrites it.
aws s3api put-bucket-versioning --bucket "${BUCKET}" --versioning-configuration Status=Enabled
# SSE-KMS with the state key, and an S3 Bucket Key so S3 asks KMS for a data key per bucket rather
# than per object — the difference between pennies and a KMS request charge on every state write.
aws s3api put-bucket-encryption --bucket "${BUCKET}" --server-side-encryption-configuration "$(cat <<EOF
{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"aws:kms","KMSMasterKeyID":"${KEY_ARN}"},"BucketKeyEnabled":true}]}
EOF
)"
# TLS only. The bucket policy is the one place that refuses a plaintext request whoever sent it.
aws s3api put-bucket-policy --bucket "${BUCKET}" --policy "$(cat <<EOF
{"Version":"2012-10-17","Statement":[{"Sid":"DenyInsecureTransport","Effect":"Deny","Principal":"*","Action":"s3:*","Resource":["arn:${PARTITION}:s3:::${BUCKET}","arn:${PARTITION}:s3:::${BUCKET}/*"],"Condition":{"Bool":{"aws:SecureTransport":"false"}}}]}
EOF
)"
# Every `pulumi up` writes a new version of the stack's state. Old versions are the undo for a bad
# apply, and ninety days is long enough to notice one; past that they are storage nobody reads.
aws s3api put-bucket-lifecycle-configuration --bucket "${BUCKET}" --lifecycle-configuration "$(cat <<EOF
{"Rules":[
  {"ID":"expire-noncurrent-state","Status":"Enabled","Filter":{"Prefix":""},"NoncurrentVersionExpiration":{"NoncurrentDays":90}},
  {"ID":"abort-incomplete-uploads","Status":"Enabled","Filter":{"Prefix":""},"AbortIncompleteMultipartUpload":{"DaysAfterInitiation":7}}
]}
EOF
)" >/dev/null
aws s3api put-bucket-tagging --bucket "${BUCKET}" --tagging 'TagSet=[{Key=managed-by,Value=bootstrap}]'

echo "==> GitHub Actions OIDC provider"
if aws iam get-open-id-connect-provider --open-id-connect-provider-arn "${OIDC_ARN}" >/dev/null 2>&1; then
  echo "    exists"
else
  # No thumbprint: IAM verifies GitHub's issuer against its own trusted CAs and no longer needs
  # one for this provider.
  aws iam create-open-id-connect-provider \
    --url "https://${OIDC_HOST}" \
    --client-id-list "${OIDC_AUDIENCE}" \
    --tags Key=managed-by,Value=bootstrap >/dev/null
  echo "    created"
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
  just infra-test
  just infra-preview

infra/pulumi/Pulumi.yaml already names s3://${BUCKET} as the backend, so there is no separate
\`pulumi login\`. Locking needs no DynamoDB table: Pulumi writes a lock object under .pulumi/locks/
in the same bucket for the length of every update.

The KMS key encrypts the state and stack *configuration* at rest. It is not where application
secrets live — those are Secrets Manager secrets the stack creates, and a human fills the ones the
stack does not generate, because secret values must never enter infrastructure code or state.

EOF
