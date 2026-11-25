# Operations runbook

## Before you start

- Go (to build). A ThousandEyes API token with read access to the account group. A Forward login
  with rights to read the network and to create checks.
- Secrets come from the environment only:

| Variable | Meaning |
| :--- | :--- |
| `TE_API_TOKEN` | ThousandEyes API token, sent only to `api.thousandeyes.com` |
| `FWD_URL` | Forward base URL (https) |
| `FWD_USER`, `FWD_PASS` | Forward credentials, sent only to that host's `/api` |

Keep them in a file with mode 600 outside the repository, and load it with
`set -a; . FILE; set +a`.

    go build -o forward-1keyes ./src/cmd/forward-1keyes

## Routine run

1. **Plan.** Reads only.

       forward-1keyes plan --network "<Forward network>" --te-account-group "<account group>"

   Useful flags: `--test-id`, `--include-name`, `--exclude-name`, `--include-label`,
   `--exclude-label`, `--max-flows` (default 500), `--resolver ip:port`, `--json`.
   `forward-1keyes plan --help` lists them all.

2. **Review the table.** For every row check the outcome, and the source prefix in the name.

   | Outcome | Meaning |
   | :--- | :--- |
   | create | a new check |
   | update | an existing managed check whose content changed |
   | unchanged | nothing to do |
   | stale | a managed check with no test behind it any more; it is left in place |
   | collision | something stands in the way: the name is already taken, two managed checks share a key, or a managed check's tags are malformed or from a newer version |
   | not_importable | the test cannot be made into a check; the row says why |

3. **Approve and apply** with the digest the plan printed:

       forward-1keyes apply --plan forward-1keyes.plan.json --digest sha256:<hex>

   `apply` reads both systems again and refuses if the plan file, the digest or the world
   changed since the plan. If it refuses, plan again and review again.

4. **Verify.**

       forward-1keyes status --network "<Forward network>"

   Then run `plan` once more: applied checks read `unchanged`.

5. **Read the results** in Forward under Verify, Intent. Managed checks are named
   `1keyes: <test name> [<test id>] <source> #<key>`.

## Removing or backing out

The tool never deletes or deactivates a check, and its HTTP client refuses DELETE. To remove
checks, delete them in the Forward UI. They are identified by the tag
`managed-by:com.forward.1keyes`.

## Troubleshooting

| What you see | Likely cause and action |
| :--- | :--- |
| `not_importable`: the destination is outside the modelled network | No collected interface has a subnet holding the address. Collect the device that connects it. |
| `not_importable`: the network's internet node has no connection to the model | The test runs from a ThousandEyes cloud agent. See below. |
| `not_importable`: the agent is not in the modelled network | The agent's address is in no collected subnet. |
| `not_importable`: a test is disabled | Enable it in ThousandEyes if you want it checked. |
| ThousandEyes refuses a test to a private address: "Can only use private BGP Monitors for a local address" | Create the test with BGP measurements off. |
| `apply` refuses with a digest or drift message | Something changed after the plan. Plan again. |
| A check FAILs while ThousandEyes shows the path working | Check the source prefix in the name first, then whether the model is current (collect again). |

Add `--debug` to log each request's method, path, status and duration. `--debug-bodies` adds
bodies with secrets removed.

### A check from an address the test does not use

ThousandEyes reports every address of an agent. If more than one of them is in the Forward model,
for example a management address on a collected management network, each one gives a check. Look
at the source prefix in the plan table before applying. There is no option to ignore a subnet
yet.

### Cloud agents

A cloud agent's traffic starts on the internet. Turning that into a check needs the Forward
network's internet node to be connected to the model, and a verified way to express an internet
source. Neither is established, so these tests are reported and never turned into checks.

## Running it on a schedule

`plan` only reads, so it is safe to schedule and compare. Keep `apply` behind a person who
has read the plan: it needs the digest of exactly what was reviewed.
