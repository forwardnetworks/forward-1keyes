# forward-1keyes

Turns ThousandEyes tests into Forward Networks intent checks.

It reads the tests in a ThousandEyes account group, works out which flow each one probes
(source agent, destination, protocol, port), locates both ends in a Forward network snapshot, and
creates the matching checks in Forward. Nothing is written until you have read a plan and approved
its digest.

Status: a prototype. Agent-to-server (ICMP, TCP, HTTP) and agent-to-agent tests are mapped.
Tests from ThousandEyes cloud agents are reported but not turned into checks yet.

## Build

    go build -o forward-1keyes ./src/cmd/forward-1keyes

## Configuration

Secrets come from the environment only. No flag takes a secret or a host.

| Variable | Meaning |
| :--- | :--- |
| `TE_API_TOKEN` | ThousandEyes API token. Sent only to `api.thousandeyes.com`. |
| `FWD_URL` | Forward base URL, https. |
| `FWD_USER`, `FWD_PASS` | Forward credentials (basic auth, sent only to that host's `/api`). |

## Use

    forward-1keyes plan --network "<Forward network>" --te-account-group "<account group>"
    forward-1keyes apply --plan forward-1keyes.plan.json --digest sha256:<hex>
    forward-1keyes status --network "<Forward network>"

- `plan` only reads. It writes a plan file (mode 600) and prints its digest. Selection flags
  (`--test-id`, `--include-name`, `--exclude-label`, ...) narrow the tests; `--max-flows` caps the size.
- `apply` re-reads ThousandEyes and Forward, refuses if the plan file, its digest, or the world
  has changed since you approved it, and then writes in batches.
- `status` lists the checks the tool manages and flags any with a malformed ownership tag set.

## How checks are owned

Each check carries tags that record where it came from: a contract version, the managing tool,
the ThousandEyes account, and a source key (a hash of test, slot, protocol and port). A later
plan matches on that key, so a renamed test updates its check and a removed test marks its check
stale. The tool never deletes a check and never deactivates one; HTTP DELETE is refused by the
client.

## Limits found against a live Forward

- A location filter takes one value, so a destination with several addresses, or an agent in
  several prefixes, becomes one check per address and per prefix. Such a check's key carries the
  destination address; every other key is unchanged.
- Forward's model has connected subnets, not end hosts, so a destination is located by subnet.
- "From the internet" needs the network's internet node to have a connection; its check form is
  not verified. A test from a cloud agent is reported as not importable, with the reason that the
  network's internet node has no connection when that is the case.

## Tests

    go vet ./... && go test -count=1 ./...
