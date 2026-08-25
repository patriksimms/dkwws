# dkwws

Upload one self-contained file — usually an HTML plan an agent just wrote — to
S3-compatible object storage and get back a link you can paste to someone else.

```console
$ dkwws upload plan.html
uploading plan.html (41231 bytes) to dkwws
https://dkwws.psimms.de/public/ycdpbvvbdtentlk57ixealxnhwixeg6h/plan.html
```

Stdout is the URL and nothing else, so an agent can capture it directly.
Progress and errors go to stderr.

There is no service to run and no database. The file is served straight out of
the bucket. A link is unguessable — 160 bits of randomness in the path — and
that is the only thing protecting it.

## How it works

One binary and one bucket. Uploads land under a publicly readable prefix:

```
public/<object-id>/<filename>
```

The random id sits in the path rather than the filename, so the URL still ends
in a real name and two uploads of `plan.html` cannot collide. Everything
outside `public/` stays private.

After storing the object, `dkwws` fetches the new URL with no credentials at
all. A bucket whose policy was never applied is the main way this setup fails,
and the check turns that into an immediate error instead of a colleague
getting a 403 from a link you already sent them.

## Install

Download a release archive for your platform from the
[releases page](https://github.com/patriksimms/dkwws/releases) and put `dkwws`
on your `PATH`. Builds are published for Linux amd64, Linux arm64 and macOS
arm64.

To build from source with Go 1.27 or newer:

```console
$ go build ./cmd/dkwws
```

## Configure

Configuration comes from the environment, falling back to a `KEY=value` file at
`${XDG_CONFIG_HOME:-~/.config}/dkwws/config`. The environment always wins,
which keeps CI and `direnv` working without touching the file.

| Setting | Meaning |
| --- | --- |
| `DKWWS_S3_ENDPOINT` | HTTPS endpoint of the backend |
| `DKWWS_S3_REGION` | signing region, default `us-east-1` |
| `DKWWS_S3_BUCKET` | the bucket to upload into |
| `DKWWS_S3_ACCESS_KEY_ID` | this machine's access key |
| `DKWWS_S3_SECRET_ACCESS_KEY` | this machine's secret key |
| `DKWWS_S3_PATH_STYLE` | path-style addressing, default `true` |
| `DKWWS_PUBLIC_BASE_URL` | optional, see below |

`DKWWS_PUBLIC_BASE_URL` is only needed when the bucket is published somewhere
other than the S3 endpoint — its own domain, or a CDN in front. Left unset it
is derived from the endpoint and bucket, so the two cannot drift apart.

The configuration file holds long-lived credentials, so `dkwws` refuses to read
it unless it is mode `0600`:

```console
$ mkdir -p ~/.config/dkwws
$ install -m 600 /dev/null ~/.config/dkwws/config
$ cat > ~/.config/dkwws/config <<'EOF'
DKWWS_S3_ENDPOINT=https://s3.psimms.de
DKWWS_S3_BUCKET=dkwws
DKWWS_S3_ACCESS_KEY_ID=...
DKWWS_S3_SECRET_ACCESS_KEY=...
EOF
```

Credentials belong on the machine, never in this repository.

An endpoint on `http://` is refused for any non-loopback host, because the
secret key would go over the wire in the clear. Set
`DKWWS_S3_ALLOW_INSECURE=true` if you really do terminate TLS elsewhere.

## Use it

```console
$ dkwws upload plan.html            # print the URL
$ dkwws upload -json plan.html      # url, object, size, sha256
$ dkwws upload -no-verify plan.html # skip the public-readability check
```

`upload` refuses files over 25 MiB; pass `-max-size` in bytes to override.
`.html` and `.htm` are stored as `text/html; charset=utf-8` so they render in
the browser; anything else is guessed from the extension and can be overridden
with `-content-type`.

The stored content type is what a browser sees. Nothing sits in front of the
bucket to correct it afterwards, so getting it right at upload time is the
whole of the mechanism.

## Set up the bucket

Two things: a policy that publishes `public/*` to anonymous readers, and one
upload-scoped key pair per machine.

The bucket policy, in AWS syntax:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": "*",
      "Action": "s3:GetObject",
      "Resource": "arn:aws:s3:::dkwws/public/*"
    }
  ]
}
```

Publish the prefix, not the bucket. A bucket-wide grant would expose anything
else you ever put in it, and granting `s3:ListBucket` would turn unguessable
links into a directory listing.

An uploader may create objects under that prefix and do nothing else — not even
read them back:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": "s3:PutObject",
      "Resource": "arn:aws:s3:::dkwws/public/*"
    }
  ]
}
```

No listing, no deleting, no policy changes, no administrative APIs, no other
bucket. Give every machine its own key pair so it can be revoked on its own.

## S3 compatibility

dkwws depends on Signature Version 4 and one write operation:

- `PutObject`, including conditional creation with `If-None-Match: *`

plus a configurable endpoint, region, bucket and path-style addressing, and a
bucket policy that serves the public prefix to anonymous `GetObject`. No
provider-specific administration API is used. The flexible-checksum trailers
newer AWS SDKs add by default are switched off, because many S3-compatible
backends reject them.

`If-None-Match: *` is what makes two uploads of the same filename safe: every
upload picks a fresh random id and creates the object conditionally, so an
upload can never overwrite an existing one.

RustFS is the first integration target. AWS S3, MinIO, Garage and Cloudflare R2
should satisfy the contract but are only supported once they have been run
against the integration tests.

## What a link does and does not protect

A link is a permanent bearer URL. Anyone holding it can read the file until
you delete the object, and there is no way to revoke one link without deleting
what it points at. Treat the URL itself as the secret: it will sit in chat
logs, browser history and referrer headers.

Links do not expire. That was a deliberate trade — enforcing an expiry needs a
service in front of the bucket, because SigV4 caps presigned URLs at seven
days. If you need time-limited links later, a bucket lifecycle rule can delete
objects after N days, or a small viewer service can expire individual links
while keeping the file.

Uploaded HTML runs in whatever origin the bucket is served from. Do not publish
it on a domain that shares cookies with something that matters.

## Development

```console
$ go test ./...
$ go vet ./...
$ gofmt -l .
```

The tests run the whole upload path in-process against `internal/s3fake`, an
S3-compatible server that verifies every SigV4 signature with an independent
implementation and models both the bucket policy and the uploader's credential
scope. No container runtime is required.

## Scope

Single self-contained files only. No expiry, no renewal, no viewer service, no
bucket browser, no delete command, no multi-file sites. Uploaded objects stay
until you remove them.
