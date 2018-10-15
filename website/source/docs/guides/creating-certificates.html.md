---
layout: "docs"
page_title: "Creating Certificates"
sidebar_current: "docs-guides-creating-certificates"
description: |-
  Learn how to create certificates for Consul.
---

# Creating Certificates

Correctly configuring TLS can be a complex process, especially given the wide
range of deployment methodologies. This guide will provide you with a
production ready TLS configuration.

~> Note that while Consul's TLS configuration will be production ready, key
   management and rotation is a complex subject not covered by this guide.
   [Vault][vault] is the suggested solution for key generation and management.

The first step to configuring TLS for Consul is generating certificates. In
order to prevent unauthorized cluster access, Consul requires all certificates
be signed by the same Certificate Authority (CA). This should be a _private_ CA
and not a public one like [Let's Encrypt][letsencrypt] as any certificate
signed by this CA will be allowed to communicate with the cluster.

~> Consul certificates may be signed by intermediate CAs as long as the root CA
   is the same. Append all intermediate CAs to the `cert_file`.


## Reference Material

- [Encryption](/docs/agent/encryption.html)

## Estimated Time to Complete

10 minutes

## Prerequisites

This guide assumes you have consul 1.4(or newer) in your PATH.

## Steps

### Step 1: Create Certificate Authority

There are a variety of tools for managing your own CA, [like the PKI secret
backend in Vault][vault-pki], but for the sake of simplicity this guide will
use consul's builtin TLS helpers:

```shell
$ consul tls ca create
==> Saved consul-ca.pem
==> Saved consul-ca-key.pem
```

The CA key (`consul-ca-key.pem`) will be used to sign certificates for Consul
nodes and must be kept private. The CA certificate (`consul-ca.pem`) contains
the public key necessary to validate Consul certificates and therefore must be
distributed to every node that requires access.

### Step 2: Create Server Certificates

Create a server certificate for datacenter `dc1` and domain `consul`, if your
datacenter or domain is different please use the appropriate flags:

```shell
$ consul tls cert create -server
==> Using consul-ca.pem and consul-ca-key.pem
==> Saved consul-server-dc1-0.pem
==> Saved consul-server-dc1-0-key.pem
```

In order to authenticate Consul servers, server certificates are provided with a
special certificate - one that contains `server.dc1.consul` in the `Subject
Alternative Name`. If you enable `verify_server_hostname`, only agents that can
provide such certificate are allowed to boot as a server. An attacker could
otherwise compromise a Consul agent and restart the agent as a server in order
to get access to all the data in your cluster! This is why server certificates
are special, and only servers should have them provisioned.

### Step 2: Create Client Certificates

Create a client certificate:

```shell
$ consul tls cert create -client
==> Using consul-ca.pem and consul-ca-key.pem
==> Saved consul-client-0.pem
==> Saved consul-client-0-key.pem
```

Client certificates are also signed by your CA, but they do not have that
special `Subject Alternative Name` which means that if `verify_server_hostname`
is enabled, they cannot start as a server.

### Step 3: Create CLI Certificates [Optional]

If you enforce HTTPS you will need a certificate in order to use consul commands
or curl to access the HTTPS API.

Create a CLI certificate:

```shell
$ consul tls cert create -cli
==> Using consul-ca.pem and consul-ca-key.pem
==> Saved consul-cli-0.pem
==> Saved consul-cli-0-key.pem
```

### Note on SANs for Server and Client Certificates

Using `localhost` and `127.0.0.1` as subject alternate names (SANs) allows
tools like `curl` to be able to communicate with Consul's HTTP API when run on
the same host. Other SANs may be added including a DNS resolvable hostname to
allow remote HTTP requests from third party tools.

### What goes where

You should now have the following files:

* `consul-ca.pem` - CA public certificate.
* `consul-ca-key.pem` - CA private key. Keep safe!
* `consul-server-dc1-0.pem` - Consul server node public certificate for the `dc1` datacenter.
* `consul-server-dc1-0-key.pem` - Consul server node private key for the `dc1` datacenter.
* `consul-client-0.pem` - Consul client node public certificate.
* `consul-client-0-key.pem` - Consul client node private key.
* `consul-cli-0.pem` - Consul CLI certificate.
* `consul-cli-0-key.pem` - Consul CLI private key.

Here is a config for a server:

```json
{
  "verify_incoming": true,
  "verify_outgoing": true,
  "verify_server_hostname": true,
  "ca_file": "consul-ca.pem",
  "cert_file": "consul-server-dc1-0.pem",
  "key_file": "consul-server-dc1-0-key.pem"
}
```

And a config for a client:

```json
{
  "verify_incoming": true,
  "verify_outgoing": true,
  "ca_file": "consul-ca.pem",
  "cert_file": "consul-client-0.pem",
  "key_file": "consul-client-0-key.pem"
}
```

Now you need to copy the CA to every machine, the server certificate to the
servers and the client certificates to the clients. The CA private key shouldn't
be on any machine! It should be somewhere safe!

### HTTPS

Please note you will need the keys for the CLI if you choose to disable HTTP (in
which case running the command `consul members` will return an error). This is
because the Consul CLI defaults to communicating via HTTP instead of HTTPS. We
can configure the local Consul client to connect using TLS and specify our
custom keys and certificates using the command line:

```shell
$ consul members -ca-file=consul-ca.pem -client-cert=consul-cli.pem -client-key=consul-cli-key.pem -http-addr="https://localhost:9090"
```

(The command is assuming HTTPS is configured to use port 9090. To see how
you can change this, visit the [Configuration](/docs/agent/options.html) page)

This process can be cumbersome to type each time, so the Consul CLI also
searches environment variables for default values. Set the following
environment variables in your shell:

```shell
$ export CONSUL_HTTP_ADDR=https://localhost:9090
$ export CONSUL_CACERT=consul-ca.pem
$ export CONSUL_CLIENT_CERT=consul-cli.pem
$ export CONSUL_CLIENT_KEY=consul-cli-key.pem
```

* `CONSUL_HTTP_ADDR` is the URL of the Consul agent and sets the default for
  `-http-addr`.
* `CONSUL_CACERT` is the location of your CA certificate and sets the default
  for `-ca-file`.
* `CONSUL_CLIENT_CERT` is the location of your CLI certificate and sets the
  default for `-client-cert`.
* `CONSUL_CLIENT_KEY` is the location of your CLI key and sets the default for
  `-client-key`.

After these environment variables are correctly configured, the CLI will
respond as expected.

[letsencrypt]: https://letsencrypt.org/
[vault]: https://www.vaultproject.io/
[vault-pki]: https://www.vaultproject.io/docs/secrets/pki/index.html
