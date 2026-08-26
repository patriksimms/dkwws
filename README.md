# dkwws

> dkwws (short for german: DisKettenWeitWurfSystem), a small CLI for uploading arbitrary files, mostly HTML plans to s3-compatibile storage

Upload one self-contained file — usually an HTML plan an agent just wrote — to
S3-compatible object storage and get back a link you can paste to someone else.

```console
$ dkwws upload plan.html
uploading plan.html (41231 bytes) to dkwws
https://example.com/public/ycdpbvvbdtentlk57ixealxnhwixeg6h/plan.html
```

Expecially useul for agents to share HTML plans which you (and others) can acces via unauthenticated browser.

There is no service to run and no database. The file is served straight out of
the bucket. A link is unguessable — 160 bits of randomness in the path.

## Getting started quickly

```sh
curl -fsSL https://raw.githubusercontent.com/patriksimms/dkwws/main/install.sh | sh
```

## How it works

Uploads land under a publicly readable prefix:

```
public/<object-id>/<filename>
```

The random id sits in the path rather than the filename, so the URL still ends
in a real name and two uploads of `plan.html` cannot collide. Everything
outside `public/` stays private.


## Install

The installer downloads the latest release for Linux amd64, Linux arm64 or
macOS arm64, verifies its checksum, and installs `dkwws` to `~/.local/bin`.
Set `DKWWS_INSTALL_DIR` to use another directory:

```sh
curl -fsSL https://raw.githubusercontent.com/patriksimms/dkwws/main/install.sh | DKWWS_INSTALL_DIR=/usr/local/bin sh
```

Pin a release with `DKWWS_VERSION`:

```sh
curl -fsSL https://raw.githubusercontent.com/patriksimms/dkwws/main/install.sh | DKWWS_VERSION=v0.2.0 sh
```

Release archives are also available from the
[releases page](https://github.com/patriksimms/dkwws/releases).

To build from source with Go 1.27 or newer:

```console
$ go build ./cmd/dkwws
```

## Configure

Configuration comes from environment variables.

Copy this template into your shell configuration and replace its placeholder
values:

```sh
export DKWWS_S3_ENDPOINT=https://your-objectstorage.com # HTTPS endpoint of the backend
export DKWWS_S3_REGION=eu-central-1 # signing region, defaults to us-east-1
export DKWWS_S3_BUCKET=your-bucket # bucket to upload into
export DKWWS_S3_ACCESS_KEY_ID=your-access-key # this machine's access key
export DKWWS_S3_SECRET_ACCESS_KEY=your-secret-key # this machine's secret key
export DKWWS_S3_PATH_STYLE=false # path-style addressing, defaults to true
export DKWWS_PUBLIC_BASE_URL= # optional, see below
```

`DKWWS_PUBLIC_BASE_URL` is only needed when the bucket is published somewhere
other than the S3 endpoint, such as its own domain or a CDN. Left unset it is
derived from the endpoint and bucket, so the two cannot drift apart.

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

## Development

```console
$ go test ./...
$ go vet ./...
$ gofmt -l .
```
